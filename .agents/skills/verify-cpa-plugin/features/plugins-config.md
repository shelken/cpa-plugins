# 插件装载与可视化配置

用户在管理面板查看已安装插件状态, 并修改插件声明的可视化配置字段

## Sub-features

- `plugin-load` 宿主按配置扫描并装载插件动态库
- `config-fields-render` 管理界面按插件上报的元数据渲染表单字段
- `config-update` 通过管理接口热更新插件运行时配置

## How to get to it (user POV)

- 浏览器打开 `http://<host>:8317/management.html` 进入插件管理页
- 管理面接口 `GET /v0/management/plugins` 与 `GET /v0/management/plugins/<id>/config`

## Driving it

Preconditions:

- 宿主服务正常运行, 管理面接口可达
- 生产实例经 `sec-run` 取密钥; 沙箱经 `-token-file` 取密钥

- **查询插件状态。** 确认装载与启用状态:
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins | jq '.plugins[] | select(.id=="<id>")'
  ```
  `registered` 为 `true` 且 `enabled` 为 `true`; 有配置字段时 `config_fields` 非空

- **查询当前配置。** 读取已生效的键值项:
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins/<id>/config
  ```

- **启用插件。** 未激活时提交写入, 响应 `HTTP 200`, 宿主热重载后装载:
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins/<id>/config -method PUT -body '{"enabled":true}'
  ```

- **复原。** 验证结束把改动过的配置 PUT 回原值

## Gotchas

- 页面提示「该插件没有声明可视化配置字段」 -> 先读 `metadata.version` 与 `config_fields`: `registered` 为 `false` 说明宿主没装载该二进制, `enabled` 为 `false` 说明配置里没启用; 两者都为 `true` 而 `config_fields` 仍为空, 说明宿主跑的产物版本早于声明 `ConfigFields` 的那次提交, 须升版本重新出包再更新宿主
- 动态库已在目录但 `registered` 恒为 `false` -> 宿主配置 `plugins.configs.<id>` 缺失或 `enabled` 为 `false`
- 配置文件里的明文密钥被替换成 bcrypt 哈希 -> 宿主预期防护行为, 真实明文经 `sec-run printenv CPA_TOKEN` 提取
