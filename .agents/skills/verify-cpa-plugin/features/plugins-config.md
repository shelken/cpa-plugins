# 插件装载与可视化配置

用户在管理面板查看已安装插件状态, 并修改插件声明的可视化配置字段

## Sub-features

- `plugin-load` 宿主按配置扫描并装载插件动态库
- `config-fields-render` 管理界面按插件上报的元数据渲染表单字段
- `config-update` 通过管理接口热更新插件运行时配置

## 断言子集

断言 id 由 `dev-sandbox.go` 的注册表 (`checkRegistry`) 声明, `--checks list` 打印权威清单, 不在此处复制。下表只补充脚本派不出来的语义: 每个断言对应哪些判据、何时该跑。

| 断言 id | 对应判据 | 何时该跑 |
|---|---|---|
| `load` | 宿主日志出现该插件 id 的 `plugin loaded plugin_id=<id>` 与 `plugin registered plugin_id=<id>`; 日志另有 `model registrar` + `context deadline exceeded` 时告警 | 改了 `plugin.json`、`main.go` 注册路径、SDK 版本 |
| `config` | 插件源码声明的配置字段名 (注册元数据 `ConfigFields`, 按源码文本扫描) 管理面全部回报; 源码未声明而管理面回报了字段也判失败; 未声明且未回报才跳过。不比较字段类型、枚举值与描述, 也不证明 PUT 后生效 | 改了 `ConfigFields` 声明 |
| `menus` | 管理面 `plugins` 列表暴露菜单 (面板侧边栏入口) | 改了菜单/入口声明 |
| `resource` | 管理面 `menus[].path` 回报的每个资源路径 (插件声明相对路径如 `/quota`, 宿主补全为 `/v0/resource/plugins/<id>/quota`) 都能返回 `200` 且内容含插件 id | 改了 resource 页面 |
| `models` | `/v1/models` 覆盖静态清单声明的全部模型 | 改了静态清单或模型注册 |
| `quota` | 插件声明的额度能力与宿主注册的额度提供方一致 (声明了必须在列, 没声明不得混入) | 改了 QuotaProvider |

`config` 与 `quota` 都先读插件源码/宿主回报的声明, 再比对管理面结果, 因此不会因为响应为空而静默通过。

装载类改动至少点名 `load,models`; 只改配置字段声明时 `-checks load,config` 足够。

沙箱的 `-profile <值>` 会把该值写进插件的 `identity-profile` 与 `login-profile` 配置项, 用于验证「档位改变后上行请求随之改变」, 默认不写。

**加断言**: 写一个 `func(*sandbox, string) error`, 在 `checkRegistry` 注册一行; 每个断言对每个插件各跑一次, `--checks list`、解析与调度自动跟随。

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

- **查询当前配置。** 读取宿主**保存的**配置对象; 它不证明插件已应用 (PUT 返回 200 后宿主才异步重载插件), 运行时是否生效要看行为, 如模型前缀开关改变 `/v1/models` 的 id 形态 (见 models.md):
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins/<id>/config
  ```

- **修改配置。** `PUT` 用请求体**整体替换** `plugins.configs.<id>`, 未写的键全部丢失; 只改个别键用 `PATCH` (浅合并)。例如只启用插件、保留身份档与前缀等其余配置:
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins/<id>/config -method PATCH -body '{"enabled":true}'
  ```
  响应 `HTTP 200` 后宿主异步热重载; 用行为确认生效, 首次查询仍是旧值时稍后重查

- **复原。** 改前先 GET 保存原对象, 验证结束用 `PUT` 原对象整体写回, 再 GET 确认与原对象一致

## Gotchas

- 页面提示「该插件没有声明可视化配置字段」或 `registered` 为 `false` -> 按顺序排查: 宿主全局插件开关与 `effective_enabled`; 宿主配置 `plugins.configs.<id>` 是否存在且 `enabled` 为 `true`; 宿主日志里该插件的装载/注册错误 (动态库装载失败、注册失败、元数据无效都会让它不注册); 前面都正常而 `config_fields` 仍为空, 才怀疑宿主跑的产物版本早于声明 `ConfigFields` 的提交, 须升版本重新出包
- 用 `PUT {"enabled":true}` 启用插件后身份档、前缀等配置全部恢复默认 -> PUT 是整体替换, 改用 PATCH 或带完整原对象 PUT
- 配置文件里的明文密钥被替换成 bcrypt 哈希 -> 宿主预期防护行为, 真实明文经 `sec-run printenv CPA_TOKEN` 提取
