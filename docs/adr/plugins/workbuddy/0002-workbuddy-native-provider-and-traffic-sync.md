# WorkBuddy 原生 Provider 架构与流量驱动的字段同步

我们需要把 `pi-codebuddy-provider` 的核心能力移植进 CLIProxyAPI，同时与官方 WorkBuddy 客户端保持协议保真。决策：把 `plugins/workbuddy` 实现为一等原生 Provider 插件（实现 `AuthProvider`、`ModelProvider`、`ProviderExecutor`、`QuotaProvider` 四项能力），多账号持久化与轮转调度完全交给 CPA 宿主，并用开发仓的审计脚本对真实客户端抓包建立流量驱动的同步闭环。成长事件、每日签到、短信注册等流程全部丢弃，只保留纯代理运行时。

## Status

accepted

## Considered Options

- 做成轻量中间件拦截器（`RequestInterceptor` / `StreamChunkInterceptor`），不做完整原生 provider
- 在插件进程内自建账号池与调度器
- 把每日签到与任务上报保留在代理请求链路里
- 人工看流量，不建自动化的字段差异校验工具

## Consequences

- WorkBuddy 模型与标准 provider 并列出现在 CPA 的原生上游里，带原生 OAuth 扫码登录与自动 token 刷新
- 插件无状态，零额外状态管理：凭据存储、健康检查、冷却都由宿主负责
- 公开插件仓保持干净，不含注册脚本、成长任务与私有的逆向工具
- 开发仓的专用审计脚本把插件请求字段与真实客户端流量逐项比对，改协议字段前必须先对齐
- 能力范围固定四项：对话补全、鉴权、额度、模型清单。对话同步、云盘、MCP 网关、本地代理注册等桌面端专属面全部不做
- 插件不发任何遥测。桌面端的 `/v2/report`、`/v1/traces` 与成长类接口刻意不复刻，它们会改变账号的服务端画像，对代理没有价值
- 插件不做应用层重试与故障转移；凭据轮转、冷却、恢复归宿主，失败调用以错误透出而不是重放
- 不实现动态模型发现（`model.for_auth`），静态清单是模型清单的唯一来源
- 提示词清洗（改写系统消息里的 Claude 自指）是新写代码不是移植：上游只在静态导出脚本里放了草稿规则，重写发生在插件构建上游请求体时
- 日志只记元数据：方法、路径、状态码、耗时、模型。请求体、回复正文、鉴权头永不落日志。抓包会话带有效 bearer token，日志边界是正确性要求而非偏好
