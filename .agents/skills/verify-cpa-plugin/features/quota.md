# 额度查询与版本门禁

管理面板提供各渠道实时配额查询，该能力由宿主版本门禁控制

## Sub-features

- `quota-providers` 宿主识别具备额度查询能力的已注册插件
- `quota-fetch` 通过管理接口拉取实时余额并返回格式化比例

## How to get to it (user POV)

- 打开管理界面额度页查看各渠道余额与额度条
- 概览列表中显示对应渠道的剩余比例

## Driving it with management-api.go

Preconditions:

- 宿主运行版本须满足 `>= v7.2.159`
- 插件在宿主中处于启用状态且 `supports_quota` 为 `true`

- **查询额度提供方。** 检查宿主是否成功识别插件的额度扩展点：
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/quota/providers
  ```
  预期返回状态码 `HTTP 200`，且列表中包含对应插件条目。若返回 `HTTP 404` 则表明宿主版本过低

- **拉取账号实时额度。** 根据账号凭据索引获取实时余额：
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/quota/fetch -method POST -body '{"auth_index":"<auth-index>"}'
  ```
  输出按分组给出剩余比例，JSON 字段为 `remainingFraction`

## Gotchas

- 管理面板完全不显示额度且接口直接返回 404 -> 宿主运行版本低于 `v7.2.159`，额度路由在此版本尚未实现，需升级宿主镜像
- 额度查询返回 502 错误 -> 上游返回的部分精确度数值带有字符串格式，插件须使用兼容反序列化类型处理
