# 每日签到由管理页显式触发

ADR-0002 曾把每日签到列为刻意丢弃的桌面端流程（连同成长事件、短信注册），理由是它们改变账号服务端画像、对纯代理运行时没有价值。用户实际需要这个能力：签到直接兑换积分，而额度页已经具备管理密钥门禁与凭据列表。决策：把签到以「管理页手动触发」的形态加回插件——端点 `POST /v0/management/plugins/workbuddy/checkin` 只接受 `auth_index`，插件经 `host.auth.get` 回调取凭据后代发上游，浏览器全程不接触 access token；`enable-checkin` 配置缺省开启、可显式关闭；不做 session start 钩子、定时器或任何后台自动签到。

## Status

accepted（对 ADR-0002 中「每日签到丢弃」的边界做局部取代，其余决策不变）

## Considered Options

- 维持 ADR-0002 的全量丢弃：能力缺失，用户只能开官方客户端手动点
- 完整移植 pi 侧的自动签到（session start 触发 + 账号本地签到记录）：签到成为代理运行的副作用，用户失去对「何时向上游发起写操作」的控制
- 管理页手动触发 + 配置开关（选定）：写操作由用户显式发起，默认可用满足日常需求，关闭时不产生任何上游请求

## Consequences

- 签到协议进入静态清单（`endpoints.dailyCheckin`、`profiles.desktop.headers.checkin`、`identityHeaders.dailyCheckin`、`requestBodies.dailyCheckin`），由 pi 仓 `staticctl --protocol` 生成，与模型/鉴权协议同一证据链
- 插件是无状态的：不保存签到记录、不做本地「今日已签」去重，幂等性完全依赖上游 `already_claimed` 响应；重复点击安全但会重复发请求
- 「全部签到」由前端按账号顺序逐个调用单账号端点实现，不新增批量后端 API；批量节奏与官方客户端一致，且每个账号都有独立的成功/失败呈现
- 签到固定走桌面主进程协议档，与 `identity-profile` 无关：该端点在 CLI 侧不存在，没有可切换的备选协议
