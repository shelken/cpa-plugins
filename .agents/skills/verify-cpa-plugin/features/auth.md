# 扫码登录与凭据保存

用户在管理面板发起官方 OAuth 登录, 授权后凭据持久化并在宿主内绑定渠道

## Sub-features

- `auth-url` 插件生成官方扫码登录地址与会话标识
- `auth-poll` 轮询会话状态直至用户完成手机授权
- `auth-files-parse` 宿主解析凭据文件并标记渠道归属

## 断言子集

本特性无沙箱离线断言, 全部走下方 Driving it 的管理面接口。相关场景: 注入凭据后的真实对话 (`verify-chat.go -scenarios session`) 依赖本特性产出的 `status: active` 凭据; 改了凭据结构 (`ToStorageJSON` 字段) 后必须重跑该场景。

## How to get to it (user POV)

- 管理界面凭据页选择渠道发起登录
- 手机端打开链接或扫码确认授权
- 凭据落盘至宿主凭据目录, 账号列表出现新条目

## Driving it

Preconditions:

- 插件已启用 (`effective_enabled` 为 `true`)
- 管理面鉴权接口可达

- **查询已有凭据。** 确认当前凭据与渠道归属 (`account_type` 由宿主按凭据里的 OAuth 元数据推导, 插件渠道实测为 `oauth`; 不作为「是否已登录」的判据, 只看 `provider` 与 `status`):
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/auth-files | jq '.files[] | {id, provider, status}'
  ```
  目标账号 `provider` 与插件 id 一致 (如 `workbuddy`), `status` 为 `active`

- **发起登录。** 生成新授权会话, 响应含授权链接 `url` 与会话 `state`:
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/<provider>-auth-url
  ```

- **检查登录结果。** 外部扫码确认后轮询直至 `status` 为 `ok`; 未扫码中间态是 `wait` (宿主把插件内部的 `pending` 转换成 `wait`), 断言 `pending` 会误报。`state` 未知或过期时接口仍返回 `HTTP 200`, 但 body 是 `{"status":"error","error":"unknown or expired state"}`——所以判据读 body 里的 `status` 字段, 不能只看状态码:
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path '/v0/management/get-auth-status?state=<state>'
  ```

- **二次确认凭据归属。** 登录成功后重跑第一步的查询, 新条目 `provider` 与 `status` 满足判据。列表读的是宿主内存里的登记记录, 证明宿主已登记该账号, 不证明文件已落盘; 要落盘证据用 `GET /v0/management/auth-files/download?name=<file>` 确认文件存在

- **把生产凭据搬进沙箱 (真机对话验收的前置)。** 沙箱 `auth/` 目录每次启动都会重建, 所以搬运必须在宿主就绪之后做; 又因为凭据值经任何回显通道都会被打码 (取到打码值直接上线会 401), 下载 (生产 `GET /v0/management/auth-files/download`) 与上传 (沙箱 `POST /v0/management/auth-files`) 必须在同一进程里完成, 由脚本负责, 中间值不落盘、不回显, 只打印文件名与字节数:
  ```bash
  go run scripts/copy-auth.go -from https://<prod-host> \
    -token-file ~/.cache/cpa-plugins/sandbox/<ids>/management-key <file.json> [<file2.json> ...]
  ```
  生产密钥经 `sec-run` 隐式读取, 生产侧只做 GET。搬完按上一条再查一次沙箱 `auth-files`, 判据同样是 `provider` 与 `status` 为 `active`; 文件名取自生产 `auth-files` 的 `name` 字段

## Gotchas

- 已登录但调用报 unknown provider -> 查 `auth-files` 对应条目的 `provider` 是否与插件 id 一致
- 鉴权失败后请求直接 503 -> 宿主触发凭据失败冷却熔断, 等冷却结束或重启宿主; 冷却期失败不能当功能失败
- 登录中途终止残留悬挂会话 -> `go run scripts/management-api.go -base http://<host>:8317 -path '/v0/management/oauth-session?state=<state>' -method DELETE` 释放服务端会话; 脚本默认 GET, 缺 `-method DELETE` 会 404; 缺 `state` 返回 `400 missing state`, 成功返回 `{"status":"ok","cancelled":<bool>}`, 会话已不存在时 `cancelled` 为 `false`; 取消后再查 `get-auth-status` 应为 `unknown or expired state`
- 沙箱注入后 `auth-files` 里同一文件名出现两条 -> v8.0.4 宿主经 POST 上传时按文件路径登记一条, 文件监听器经插件解析又按 `userId` 登记一条, 两条共用同一文件; 用 `name`/`id`/`auth_index` 区分, 不能用条数判断文件数
- 每次对话后宿主日志出现 `auth file changed (REMOVE)` 与 `(WRITE)` 交替 -> 宿主在记录请求结果后持久化凭据, 插件凭据走临时文件原子替换, 监听器把它看成先删后写; 这不是插件在刷新 token, 只要记录仍为 `active` 就不影响验收
- 登录链路驱动沙箱时管理密钥用 `-token-file`, 生产密钥对沙箱必然 401
