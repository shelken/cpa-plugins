# 产物未发布就去做生产验证

**日期**: 2026-09-14
**影响**: 在生产实例上多轮无效探索（插件矩阵、账号列表、宿主版本、配置项）, 真正的前置是插件从未发布
**发现人**: shelken（要求直接做真机生产验证）

## 问题

插件合并进 main 后直接到生产实例找它准备验证, 反复查插件列表与凭据都不见该渠道。实际状态是产物尚未发布, 宿主无从安装。

## 现象

```bash
# 插件矩阵: 只有既有插件, 没有目标插件
# 商店列表: 有版本、未安装
{"id":"qwenworkcn","version":"0.1.0","installed":false,"platforms":3}
```

## 根因

- **错误假设**: 「代码合并进 main 就等于宿主可安装」。实际约束是宿主经 `plugins.store-sources` 订阅 registry, 安装时按 artifact url 拉 GitHub Release 压缩包并校验 sha256; sha256 未回填时安装不可行, registry 里的版本号只是声明
- **缺失检查点**: 动手前没有一次 `GET /v0/management/plugin-store`。这一次查询就能回答「有没有可装的东西」, 比逐项探测宿主快得多
- **实际约束**: 宿主安装响应里给出 `source_url` 指向订阅的 registry, 说明产物链是 标签 到 CI 到 Release 到 registry, 绕不开其中任何一环

## 修复

- 先补发布: 推标签 `<id>/v<X.Y.Z>` 触发 `release-plugin.yml`, 三平台产物与 Release 由 CI 建, `record` 作业自己下载产物回填真实 sha256 到 `plugin.json` 与 `registry.json` 并推回 main
- 拉 main 核对版本与哈希, 再对宿主装: `POST /v0/management/plugin-store/<id>/install`, 响应给出落盘路径与 `restart_required=false`
- 装完复查 `registered`、`enabled`、`effective_enabled` 与 `/v1/models` 里的前缀模型
- 生产账号登录后跑 `verify-chat.go` 得 19 项通过, 额度接口返回 Wallet 余额, 两层事实齐了才算可用

## 预防

- 生产验证前先 `GET /v0/management/plugin-store`; `installed=false` 就停手补发布三件套（标签、CI record、拉 main 核对 sha256）, 不在宿主上继续探索
- 安装期宿主热重载窗口内, 插件列表可能只返回部分条目、`/v1/models` 可能为空; 这类瞬时结果不作为故障判据, 隔一次复查再下结论
- 管理面路径只从路由源码 `internal/api/server_management.go` 或技能特性文件抄, 不凭记忆拼（额度入口是 `/v0/management/quota/fetch` 带 `auth_index`, 不是 `/v0/management/plugins/<id>/quota`）
- 生产上的写操作限定在可逆动作（安装、登录会话）, 配置声明走 gitops 仓库
