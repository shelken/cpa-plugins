# 扫码登录

用户视角：在管理面板点一次登录，手机扫码确认，之后这个渠道就能用了。

## Sub-features

- `auth-url` 插件返回官方登录地址与 `state`
- `auth-parse` 凭据文件被宿主归属为 `provider=<id>`、`quota_provider=<id>`
- `auth-poll` 扫码后宿主的登录会话转为完成
- `auth-cancel` 未完成的登录会话可以取消
- `auth-refresh` 凭据过期时宿主按凭据调度自动续期

## How to get to it (user POV)

- 管理面板的登录入口（等价于下面的 `-auth-url` 请求）
- 手机扫码确认
- 凭据落在宿主 `auth-dir` 下

## Driving it with the management API

Preconditions:

- 沙箱以 `-keep` 启动，宿主在 `127.0.0.1:18317`
- 管理面命令一律写成 `go run scripts/management-api.go -sandbox workbuddy -path <路径>`，密钥由脚本从 `<沙箱>/management-key` 读
- 凭据档需要可用账号。本仓没有可测账号，这一步必须由用户现场扫码登录或提供凭据，不要拿旧凭据反复试

- **拿到登录地址。** 跑 `go run scripts/management-api.go -sandbox workbuddy -path /v0/management/workbuddy-auth-url`。返回 `{"status":"ok","url":"https://www.workbuddy.cn/login?...","state":"<36 字符>"}`。这一步 agent 可以独立完成，`url` 就是交给用户的东西
- **交给人。** 把 `url` 原样发给用户扫码。不要打印 `state` 之外的会话细节，也不要把 URL 当成凭据存进仓库
- **轮询。** 用户确认前后都可用 `go run scripts/management-api.go -sandbox workbuddy -path '/v0/management/get-auth-status?state=<state>'`。未扫码时返回 `{"status":"wait"}`；异常时返回 `{"status":"error","error":"..."}`；完成后返回 `{"status":"ok"}`
- **确认凭据落盘。** `go run scripts/management-api.go -sandbox workbuddy -path /v0/management/auth-files | jq -c '.files[] | {name, provider, quota_provider, account_type}'`。出现新凭据且三个归属字段符合预期才算登录链路通过。具体账号字段不要写进证据
- **取消悬挂会话。** `go run scripts/management-api.go -sandbox workbuddy -method DELETE -path '/v0/management/oauth-session?state=<state>'`，看 stderr 的 `[res] HTTP 200`，随后同一 state 轮询应变成 `unknown or expired state`
- **离线归属判定。** 不联网也能验 `auth.parse`：自造一个同结构的凭据文件放进 `auth-dir`（字段形状见实现简报第六节），重启宿主后看 `auth-files` 的归属字段。不要拿真实账号文件做这件事

## Gotchas

- 宿主装载时会把 `config.yaml` 里的明文密钥哈希后写回，明文只在 `<沙箱>/management-key`。读错地方会得到 `invalid management key`
- 端口上的宿主如果不是你启动的，登录会话可能属于别人，别去取消
- 登录档与身份档是两个独立配置（`login-profile` / `identity-profile`），混改会同时影响请求头形状与登录流程，验证时一次只动一个
- 凭据失效会触发宿主侧冷却，后续请求直接 `503 auth_unavailable`。遇到 503 先重启宿主再判断
- 别用不可用账号反复打登录接口；失败错误会污染判断，把账号问题读成插件缺陷
