---
name: verify-cpa-plugin
description: 验证 cpa-plugins 仓库插件与发布产物的可用性。涵盖本地沙箱运行、生产实例管理面检查、宿主版本门禁与安装态校验
---

# 验证 CPA 插件

真实可用性取决于两项指标：宿主成功装载动态库并报出模型，以及客户端调用链路返回正常响应

## Launch

沙箱环境就绪命令：

```bash
# 自动编译动态库、生成本地配置、启动宿主并执行基础断言
go run scripts/dev-sandbox.go -plugin workbuddy -timeout 120s
```

就绪判据为输出包含 `[+] 沙箱验证通过` 以及四项断言通过提示，默认监听端口 `18317`
需要保留宿主进程以便调试时传入 `-keep` 参数，脚本会在退出时输出宿主 pid 与端口

## Doctor

对指定实例执行单次只读探测，确认服务状态与版本：

```bash
# 探测生产实例
go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins | jq -c '.plugins[] | {id, registered, enabled, effective_enabled, supports_quota, config_fields}'

# 探测本地沙箱
go run scripts/management-api.go -path /v0/management/plugins | jq -c '.plugins[] | {id, registered, enabled, effective_enabled, supports_quota, config_fields}'
```

判据标准：
- `HTTP 200` 且能够返回插件 JSON 列表
- 目标插件 `registered` 为 `true`
- 目标插件 `enabled` 与 `effective_enabled` 为 `true`
- 宿主版本从响应头 `[host] X-CPA-VERSION` 读取，额度特性要求版本 `>= v7.2.159`

## Drive

统一使用仓库提供的管理面工具驱动，密钥自动通过 `sec-run` 提取 `CPA_TOKEN`：

```bash
# 读取插件列表与装载状态
go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins

# 读取已保存登录账号列表
go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/auth-files

# 读取指定插件当前配置
go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins/<id>/config

# 启用指定插件配置 (写操作须经确认)
go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins/<id>/config -method PUT -body '{"enabled":true}'
```

## Evidence

证据要求记录真实终端输出与命令原文，存放于临时目录 `{cache-dir}/evidence/<plugin>/`：

- 插件状态证据：包含管理面响应体及 `[host]` 版本头
- 账号归属证据：截取 `auth-files` 中脱敏后的 `provider`、`type` 与 `status` 字段
- 模型报送证据：调用 `/v1/models` 获取实际由宿主注册并对外暴露的模型列表

## Cleanup

- 清理沙箱：沙箱运行时未加 `-keep` 会自动回收进程；使用 `-keep` 启动的进程通过输出打印的 pid 执行 `kill <pid>` 终止
- 清理会话：测试中未完成的扫码登录会话调用 `DELETE /v0/management/oauth-session?state=<state>` 注销，避免残留悬挂会话
- 凭据保护：严禁在清理时删除真实已生效的账号凭据文件

## Helpers

仓库内置维护工具：

- `scripts/management-api.go`：管理面单点交互工具，自动由 `sec-run` 注入 `CPA_TOKEN` 执行鉴权，响应里的密钥与凭据字段默认打码
- `scripts/verify-chat.go`：对话链路验收入口，一次跑完流式帧合规、思维链、上下文记忆、缓存命中、思考深度与非流式聚合
- `scripts/dev-sandbox.go`：端到端沙箱启动与多项断言套件，只覆盖装载与报送，替代不了真机对话验收
- `scripts/check-plugins.go`：检查仓库清单、构建矩阵、声明平台一致性
- `scripts/verify-registry-install.go`：在线拉取已发布产物校验哈希与动态库 ELF、Mach-O 格式

## 刚发布的产物怎么读

`raw.githubusercontent.com` 上的 `registry.json` 走 CDN，推送后可能仍返回上一版，表现为清单里的 `version` 已是新版而 `sha256` 还是旧值。此时

- 要权威内容就直接读仓库，`gh api repos/<owner>/<repo>/contents/registry.json --jq .content | base64 -d`
- 要校验刚发布的产物就加 `-local` 对本地 `registry.json` 跑 `verify-registry-install.go`
- 要装到宿主就直接触发安装并看响应，宿主自己会拉到新鲜内容

## 特性地图

详细用例位于 `features/` 目录：

- [插件装载与可视化配置](features/plugins-config.md)：验证插件启用状态与配置字段声明
- [扫码登录与凭据保存](features/auth.md)：验证 OAuth 登录链路与凭据归属
- [模型注册与可用性](features/models.md)：验证静态模型清单向客户端的报送
- [对话链路](features/chat.md)：验证流式与非流式请求的真实回复
- [额度查询与版本门禁](features/quota.md)：验证额度接口与宿主版本依赖
