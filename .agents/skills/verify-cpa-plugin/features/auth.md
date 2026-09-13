# 扫码登录与凭据保存

管理面板发起官方 OAuth 登录流程，扫码后将凭据持久化存储并在宿主内绑定渠道

## Sub-features

- `auth-url` 插件生成并返回官方扫码登录地址及会话标识
- `auth-poll` 轮询会话状态直至用户完成手机授权
- `auth-files-parse` 宿主解析凭据文件并标记渠道归属

## How to get to it (user POV)

- 在管理界面凭据页选择对应渠道发起登录
- 手机端打开链接或扫码确认授权
- 凭据自动落盘至宿主凭据目录，账号列表中出现新条目

## Driving it with management-api.go

Preconditions:

- 插件已在宿主中处于启用状态
- 能够通过管理面访问鉴权接口

- **查询已有凭据列表。** 检查当前系统已加载的所有凭据及渠道归属：
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/auth-files | jq '.files[] | {id, provider, status, account_type}'
  ```
  输出中目标账号的 `provider` 必须精确匹配插件标识（如 `workbuddy`），`status` 须为 `active`

- **发起登录请求。** 生成新的授权会话：
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/workbuddy-auth-url
  ```
  响应返回包含授权链接 `url` 与会话 `state`

- **检查登录结果。** 待外部扫码确认后，轮询状态直至完成：
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path '/v0/management/get-auth-status?state=<state>'
  ```

## Gotchas

- 已登录账号但客户端调用提示 unknown provider -> 检查 `auth-files` 中对应条目的 `provider` 字段是否与插件标识一致
- 账号鉴权失败后后续请求直接报 503 -> 宿主触发了凭据失败冷却熔断，需等待冷却结束或重启宿主进程
- 登录中途终止残留悬挂会话 -> 发送 `DELETE /v0/management/oauth-session?state=<state>` 释放服务端会话
