# 协议基准：对齐 WorkBuddy 桌面端，而非 codebuddy CLI

上游 `pi-codebuddy-provider` 按五种客户端身份分层构造请求头（独立 codebuddy CLI、桌面端主进程 axios、桌面端 CLI 层、桌面端渲染进程、Web 成长中心），取值来自 2026-08-06 的流量观测；其中对话补全与 OAuth 握手走的是独立 CLI 身份（`codebuddyCli.headers.chat`、`/v2/plugin/auth/state?platform=cli`），这正是它与官方桌面端出现系统性字段差异的来源。本插件不再沿用多身份混用与过期观测：全部接口统一以本机 WorkBuddy 桌面端 5.3.14 的实测流量为唯一基准，`data/static-config.json` 中每个字段都必须能回溯到一次版本匹配的抓包会话。

## Status

accepted（由 ADR-0004 修订：桌面端由唯一基准降为默认基准）

## Considered Options

- 沿用上游的五身份分层：改动最小，但基准分散、取自 5.3.11 时期，无法回答"某个头到底该不该发"。

## Consequences

- 对话补全不再使用独立 CLI 身份：实测桌面端发 `User-Agent: WorkBuddy/5.3.14 WorkBuddy/5.3.14 CLI/2.115.0`、`X-IDE-Type/Name: WorkBuddy`，且 `x-session-id`、`tool_choice` 实测根本不存在。
- 上游已成文的桌面端常量同样过期：`X-Domain` 实测为 `www.workbuddy.cn`，而非上游写死的 `www.codebuddy.cn`。
- OAuth 握手需按桌面端重录：`/v2/plugin/auth/state`、`/v2/plugin/auth/token` 只在登出扫码时触发，本次抓包（已登录态）零样本，`platform=cli` 等 CLI 取值不能直接沿用。
- 与上游 `pi-codebuddy-provider` 的协议代码持续分叉，移植时不能照搬其头部常量。
- 基准证据：`revest-app/output/WorkBuddy_20260913_171327.json`（抓包 UA `WorkBuddy/5.3.14 CLI/2.115.0`，与本机安装版本一致）。
