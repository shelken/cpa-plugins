# 013 · 插件凭据被宿主元数据合并回写旧值且永不进刷新调度

**日期**: 2026-09-21
**影响**: 生产 qwenworkcn 额度接口连续 502, 重新登录拿不到新 token; workbuddy 同源未暴露, 属静默陈旧
**发现人**: 用户报障额度页 502, 沙箱复现后定位

## 问题

管理页额度卡返回上游 `401 Invalid JWT token`, 插件以 502 透传。生产凭据在被手动刷新前已陈旧, 有效期顺延掩盖了陈旧问题。两个插件共享同一套凭据落盘与调度路径, 缺陷同源。

## 现象

管理面复现, 两个插件的 `quota` 接口都返回同一形状:

```bash
go run scripts/management-api.go -base http://<host>:8317 \
  -path "/v0/management/plugins/qwenworkcn/quota?auth_index=<n>"
# {"code":"invalid-credential","msg":"... upstream returned HTTP 401: {\"code\":\"Invalid JWT token\"}"}
```

沙箱用真实宿主 v7.2.159 加本地模拟上游复现出两个独立信号:

```bash
# 重新登录后磁盘文件里的 access 仍是旧 JWT, 只有 modtime 变化
go test -run TestReloginPreservesNewCredentials ./plugins/qwenworkcn/  # 修复前: old credentials overwrote new login
# 凭据过期后宿主不调插件刷新
go test -run TestExpiredCredentialAutomaticallyRefreshes ./plugins/qwenworkcn/  # 修复前: expired credential never reached executor refresh
```

## 根因

**覆盖**: 宿主持久化登录结果时先合并旧文件的元数据, 而写盘时 `Metadata` 优先于 `StorageJSON`。宿主只跳过凭据类键 (`{cli-proxy-api}/sdk/cliproxy/auth/metadata_merge.go:11-19` 的 `IsAuthTokenPayloadKey`), 其余旧键按「target 未定义才补」的规则回填 (`metadata_merge.go:38-42`)。插件原来只把 `{"type": ...}` 放进 `Metadata`, 凭据本体只在 `StorageJSON`, 于是 `credentials`、`machineId`、`sessionState` 全部命中回填, 旧值压过本次登录。

**不排期**: `nextRefreshCheckAt` 对插件凭据只有两条可用路径 (`{cli-proxy-api}/sdk/cliproxy/auth/auto_refresh_loop.go:356-420`)。一是凭据 `Metadata` 或 `Attributes` 里的 `refresh_interval_seconds` (`conductor_refresh.go:171-182`), 二是按 provider 取的 refresh lead。插件凭据两者都缺: lead 只由内置认证器实现并注册 (`sdk/auth/refresh_registry.go:17`, 实现类仅 antigravity、claude、codex、kimi、xai), 插件 provider 拿到 nil 后直接返回不排期。

- 错误假设: 插件写入了新 token, 凭据就是新的。实际约束: 宿主回填旧元数据后由 `Metadata` 决定写盘内容
- 错误假设: 插件实现了 `RefreshToken` 就会被宿主排期刷新。实际约束: 排期只看 `refresh_interval_seconds` 或 provider refresh lead
- 缺失检查点: 诊断初期只比对上游返回与登录响应, 没有先看磁盘文件的 access 与 modtime, 也就没发现写盘被回填

## 修复

- `ToAuthData` 把本次登录的完整凭据 JSON 提升为 `Metadata`, 让回填规则找不到空位 (`plugins/*/credential.go`)
- 按凭据实际剩余有效期声明 `refresh_interval_seconds`, 并写入 `expires_at` 供宿主判定过期 (`plugins/*/credential.go`)
- `NextRefreshAfter` 对已过期凭据立即返回可刷新, 不再把检查点推后固定 5 分钟
- 回归测试 `credential_merge_test.go` 覆盖两条路径, 修复前红, 修复后绿

## 预防

- 新增或改动插件的凭据落盘路径时, 先读 `MergeExistingAuthMetadata` 与 `ToAuthData` 两处, 确认 `Metadata` 已覆盖全部凭据相关键, 再写代码
- 插件 provider 必须显式声明 `refresh_interval_seconds`; 只实现 `RefreshToken` 不足以进入宿主刷新调度
- 诊断凭据陈旧问题, 第一步比对凭据文件自身内容与 modtime, 再比对上游响应, 不要先假设刚刚登录成功
- 验证用沙箱断言字符串必须写明失败语义 (`old credentials overwrote new login`), 便于修复前后直接对照
