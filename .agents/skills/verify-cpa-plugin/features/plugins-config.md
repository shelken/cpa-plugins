# 插件装载与可视化配置

管理面板可查看已安装插件状态，并允许对插件声明的可视化字段进行修改

## Sub-features

- `plugin-load` 宿主根据配置扫描并装载插件动态库
- `config-fields-render` 管理界面依据插件上报的元数据渲染表单字段
- `config-update` 通过管理接口热更新插件运行时配置

## How to get to it (user POV)

- 访问管理面 `http://<host>:8317/management.html` 进入插件管理页
- 查看目标插件的装载开关、优先级与自定义字段

## Driving it with management-api.go

Preconditions:

- 宿主服务正常运行且管理面接口可达
- 执行环境通过 `sec-run` 能够正常提取 `CPA_TOKEN`

- **查询插件状态。** 运行管理面命令提取插件元数据：
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins | jq '.plugins[] | select(.id=="workbuddy")'
  ```
  输出确认 `registered` 为 `true`，`enabled` 为 `true`。若存在配置字段，`config_fields` 数组不为空

- **查询当前配置。** 读取插件在宿主配置文件中已生效的键值项：
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins/workbuddy/config
  ```

- **启用插件配置。** 当插件未激活时，提交 PUT 请求写入配置：
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins/workbuddy/config -method PUT -body '{"enabled":true}'
  ```
  响应返回 `HTTP 200`，宿主随后在热重载中装载该插件

## Gotchas

- 页面提示「该插件没有声明可视化配置字段」 -> 插件在宿主配置中未设 `enabled: true`，或插件在当前发布的二进制中未声明 `ConfigFields`
- 动态库位于目录但 `registered` 恒为 `false` -> 宿主配置 `plugins.configs.<id>` 缺失或 `enabled` 为 `false`
- 配置文件中的明文密钥被自动替换为 bcrypt 哈希 -> 属于宿主预期防护行为，通过 `sec-run printenv CPA_TOKEN` 提取真实明文
