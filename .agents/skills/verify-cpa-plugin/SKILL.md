---
name: verify-cpa-plugin
description: 验证 cpa-plugins 里的插件是否真能用。改完插件要证明它可用、提交或发布前设门禁、判定某个功能算不算完成、用户反馈装不上或跑不通时使用。给出按改动类型选档的判定表、真机装载与产物安装的驱动与取证方式，以及哪些验证必须交给用户。
---

# 验证 CPA 插件

单测与编译通过只是必要条件。真实证据是两个东西：**宿主真的装载了这个动态库**，以及**商店真的能装上这个产物**。两者都能离线跑，不需要凭据，也不该问用户。

下文凡是不带前缀的路径都相对本仓根目录；`{pi-codebuddy-provider}/...` 指向另一个仓库。

## 档位与人工边界

从下往上跑，改动跨到哪一档就必须跑到哪一档。低档失败不进入高档。

| 档位 | 需要什么 | 跑什么 | 通过的判据 |
| :--- | :--- | :--- | :--- |
| **离线档** | 无 | `go vet`、`go test ./...`、`check-plugins`、构建动态库并查导出符号 | 命令无输出/全过，`nm` 能看到四个导出符号 |
| **真机档** | 宿主二进制 | `dev-sandbox`，再用 `management-key` 驱动管理面 | 三条断言全过，Doctor 三项符合预期 |
| **产物档** | 已发布的产物 | `release.go pack`，`verify-registry-install` | 包结构自检通过，每个平台的 SHA256 与动态库格式校验通过 |
| **凭据档** | **可用账号** | 扫码登录、对话、额度拉取 | 见对应特性文件 |

改动类型 → 最低档位：

- 只改文档、注释 → 离线档的 `check-plugins`。
- 改 Go 代码（请求构造、解析、流程）→ 离线档 + 真机档。
- 改 `plugin.json`、`data/*.json`、版本号 → 离线档 + 产物档。
- 改协议字段（请求头、请求体、登录流程）→ 离线档 + 真机档 + 用抓包会话复跑字段审计。

离线档里的导出符号检查（`nm` 在 macOS 用 `-gU`，Linux 用 `-D`）：

```bash
cd plugins/workbuddy && CGO_ENABLED=1 go build -buildmode=c-shared -o /tmp/verify/workbuddy.dylib .
nm -gU /tmp/verify/workbuddy.dylib | grep -c cliproxy   # 期望 4: init / Call / Free / Shutdown
```

**宿主版本要求。** 能力面由宿主版本决定，插件声明不算数。宿主不校验插件的 SDK 版本，所以版本不匹配不会报错，只会静默少功能：

|能力|最低宿主版本|依据|
|:---|:---|:---|
|装载、模型、扫码登录|早于 `v7.2.158` 即支持|按 SDK `v7.2.159` 构建的产物在 `v7.2.158` 宿主上装载成功，返回 21 个模型与真实登录 URL|
|额度|`v7.2.159`|`quota.*` 方法在该版引入。同一产物在 `v7.2.158` 上 `supports_quota` 为 `null`，`quota/providers` 返回 404|

拿不准宿主版本时，先看装载日志与 Doctor 的能力字段，别从版本号推断。

**插件必须声明在配置里。** `plugins.configs.<id>` 缺失时插件根本不加载：动态库在磁盘上，管理面也只报 `registered: false`，`/v1/models` 为空。从商店安装时宿主会自己补这条；手写或由外部注入配置时要自己写上。

**人工边界。** 离线档、真机档、产物档全部无需用户，自己跑完再说话。凭据档里只有一步不可替代：

1. **手机扫码。** agent 自己就能拿到登录 URL、自己轮询状态、自己确认凭据落盘（见 `features/auth-login.md`），但把那个 URL 变成凭据的动作只能由人完成。做法是把 URL 原样交给用户，等他确认后再轮询，不要自己猜。
2. **账号可用性的判断。** 账号被风控或限额时，agent 无法从错误码区分"账号问题"还是"插件缺陷"。凭据档出现的错误一律先标注账号状态，不要在账号未验证时定性（教训见 `postmortems/003-unusable-account-verification.md`）。
3. **真实环境的主观判定。** 用户在自己环境引入后的体验、上游策略变化要不要跟进，属于用户判断，不要代替他下结论。

现有账号一律不可用。凭据档必须等用户现场扫码登录或提供凭据，在此之前只跑前三档。

## Launch

```bash
# 沙箱: 构建插件 -> 按平台放置 -> 生成配置 -> 启动宿主 -> 三条断言
go run scripts/dev-sandbox.go -plugin workbuddy -host-src <已 checkout 到该 tag 的 CLIProxyAPI 目录> -timeout 120s
```

宿主的解析顺序是 `-host <二进制>` > `$CPA_HOST_BIN` > `~/.cache/cpa-plugins/host/v<SDK版本>/cliproxyapi` > `-host-src <源码目录>`（现场构建并落缓存）。源码目录必须先 `git checkout` 到插件 `go.mod` 指定的那个 tag，脚本会核对，不一致直接失败。改动频繁时用 `/tmp/ref/CLIProxyAPI` 这类固定克隆，别反复下载。

沙箱不需要凭据。它会：

- 在 `~/.cache/cpa-plugins/sandbox/<id>/` 起一个宿主，端口默认 `18317`。
- 每次运行开始时清空该目录，所以上一轮的东西不会串味。
- 断言三件事：日志出现 `plugin loaded` 与 `plugin registered`；`/v1/models` 覆盖静态清单声明的每一个模型；实现额度能力的插件出现在 `quota/providers`。
- 打印日志路径与 `pid`。

需要留下宿主动管理面时加 `-keep`，它会保留进程并在结尾打印密钥文件路径。收尾见 Cleanup。

## 生产实例

沙箱之外的宿主（用户自己部署的那台）是唯一能证明"真能用"的地方。它同时是别人的生产环境，按只读对待。

**先确认可达性与目标，再动手。** 401 表示网络通、只差密钥；超时才是网络不通。两者要分开说，别混成一句"连不上"。

**版本从响应头读，不从部署仓库推断。** 每次管理面响应都带 `X-CPA-VERSION`、`X-CPA-COMMIT`、`X-CPA-BUILD-DATE`、`X-CPA-SUPPORT-PLUGIN`，它们在鉴权之前写入，所以密钥不对时也拿得到。仓库里的镜像 tag 与 Pod 实际运行的版本可能不一致。

**鉴权统一走 `scripts/management-api.go`，不要手搓 curl。** 密钥来源、请求头形式、宿主版本头、被拒时的处置建议都由它封装；响应体走 stdout，诊断行走 stderr，所以可以直接接 `jq`：

```bash
# 沙箱: 地址与密钥都从沙箱目录取
go run scripts/management-api.go -sandbox workbuddy -path /v0/management/plugins | jq -c '.plugins[]'

# 生产实例: 密钥来源三选一
go run scripts/management-api.go -base http://<host>:8317 -key-cmd 'sec-run printenv <变量名>' -path /v0/management/auth-files
go run scripts/management-api.go -base http://<host>:8317 -key-name home-ops -path /v0/management/auth-files
go run scripts/management-api.go -base http://<host>:8317 -key-file ~/.cache/cpa-plugins/keys/home-ops -path /v0/management/auth-files

# 没有任何密钥来源时它也发一次请求, 专门用来读宿主自报的版本头
go run scripts/management-api.go -base http://<host>:8317
```

密钥来源优先级 `-key-file` > `-key-name`（读 `~/.cache/cpa-plugins/keys/<名字>`）> `-key-cmd` > `-key-env` > 沙箱目录。密钥只留在进程内，不进对话、不进证据、不进 shell 历史。取不到、或值不被接受，就停下来问用户，不要改试别的凭据。用户已在浏览器登录管理面时还有最后一个来源：用 CDP 把页面里的值**重定向进密钥文件**，不打印。

**放法用原值，不加 `Bearer`。** 宿主两种都收，`Authorization: <key>` 原值或 `X-Management-Key: <key>`，脚本默认第一种，要换用 `-auth x-management-key`。前缀不是决定项：宿主会先剥 `Bearer ` 再比，所以同一个值两种写法等价；但去掉前缀能少一层"是不是头写错了"的自我怀疑，排查时只留一个变量。

**别在同一把密钥上连试。** 宿主对连续失败尝试按 IP 封禁，实测 5 次失败锁 30 分钟。`invalid management key` 只说明这把值不属于该实例，重试改变不了结论，只会把后面的判断窗口一起封掉。被拒就换来源，或者停下来问人。

**默认直连。** 脚本不读环境里的 `HTTP_PROXY` / `HTTPS_PROXY`，内网地址被代理吃掉会表现为超时或错误页面；确需代理时用 `-proxy <url>` 显式打开。

手搓命令时仍要注意：工具调用之间不共享 shell 变量，上一轮定义的 `$K` 在下一轮就是空的，空令牌换来的 401 长得像"密钥错"。下判断前先确认取值非空，不要用 401 反推密钥对不对。

**证据要脱敏。** 落盘前裁掉 `Authorization`、token、邮箱、手机号、完整账号标识、余额绝对值；只留方法、路径、状态码、能力字段、数量与比例。要留整份响应就先过字段裁剪，别整包 `tee`。别开 `set -x`。

**只读边界。** 生产实例默认只做 GET。安装、卸载、登出、改配置属于写操作，要用户明确同意，并说明会影响到谁。

**界面那侧。** 管理面 UI 只是 API 的前端，能读 API 就不点界面。必须用界面时（二维码、图形化状态）按 `browser-best-practice` 连 CDP，读页面文本即可；UI 的 network 记录带鉴权头，别把它的内容贴进证据。

## Doctor

任何异常先跑 Doctor，别急着改代码。三项只读检查：

```bash
go run scripts/management-api.go -sandbox workbuddy -path /v0/management/plugins | jq -c '.plugins'
go run scripts/management-api.go -sandbox workbuddy -path /v0/management/auth-files | jq -c '{count: ((.files // [])|length)}'
go run scripts/management-api.go -sandbox workbuddy -path /v0/management/quota/providers | jq -c '.'
```

预期：`registered`/`enabled` 为 `true`，`supports_oauth`/`supports_quota` 与插件实际能力一致，`metadata.version` 等于 `plugin.json` 的版本；`auth-files` 在沙箱里是空的（沙箱不写凭据）；额度提供方列表包含本插件。

密钥由 `-sandbox` 自动从 `<沙箱>/management-key` 读，不要从 `config.yaml` 读：宿主装载时会把明文密钥 bcrypt 哈希后写回配置，配置里只有哈希，拿它请求一律返回 `invalid management key`。

## Drive

管理面就是驱动面，统一用 `go run scripts/management-api.go` 驱动：`-sandbox <id>` 连沙箱，`-base <url>` 配 `-key-*` 连别处；写请求用 `-method POST -body '<json>'`。按特性文件里逐条列出的命令执行，command 与预期结果都在那里，别即兴发挥。

三个入口各自的用途：

- `GET /v0/management/plugins` 看装载与能力声明，最省事的 Doctor。
- `GET /v0/management/<provider>-auth-url` 发起登录，返回 `{status, url, state}`；`GET /v0/management/get-auth-status?state=<state>` 轮询，返回 `wait` / `ok` / `error`。
- `POST /v1/chat/completions`、`POST /v0/management/quota/fetch` 是需要凭据的真实链路。

管理面端点（在 `v7.2.159` 上实测过，比照着猜省事）：

|端点|看什么|
|:---|:---|
|`GET /v0/management/plugins`|装载与能力声明，含 `config_fields`（插件声明的可视化配置项，空数组就是没声明）|
|`GET /v0/management/plugins/<id>/config`|该插件在 `plugins.configs.<id>` 下的实际配置，如 `{"enabled":true}`|
|`GET /v0/management/config`|宿主运行配置，看代理、日志开关等|
|`GET /v0/management/auth-files`|已落盘的凭据（沙箱里恒为空）|
|`GET /v0/management/plugin-store`|商店视角的插件与来源|
|`GET /v0/management/logs`|宿主的日志文件，**仅 `logging-to-file: true` 时可用**，否则 400 且报 `logging to file disabled`|
|`quota/providers`、`quota/fetch`、`quota/reset`|额度，需 `v7.2.159` 及以上宿主|

没有"测试模型"这类端点。真实对话只能走 `/v1/chat/completions`，用**客户端 API key**，管理密钥不能替代。

同一端口只能有一个实例。要并行验两个插件就显式换 `-port`，不要双驱同一个宿主。

## Evidence

证据是命令原文加真实输出，不是"应该可以"。三条底线：

- 单测、编译通过、"日志里没报错"都不是生产端证据。生产端证据来自真实宿主进程或真实产物。
- 用户能看见的行为，就用用户看见的方式证明；不要用只存在于测试里的入口去替代真实入口。
- 跳过就是跳过。某个入口因为缺前置条件没跑到，直说它没跑到，不要用另一条路的结果冒充。
- 生产实例上的日志走 `GET /v0/management/logs`（需 `logging-to-file: true`）。关掉文件日志时日志只在 stdout，归运维侧的容器日志或日志聚合管，agent 读不到就直接说读不到，不要拿别的证据顶上。

证据落盘到 `~/.cache/cpa-plugins/evidence/<plugin>/<日期>-<特性>/`，至少包含该轮的 `host.log` 与把管理面响应存下来的文本：

```bash
D=~/.cache/cpa-plugins/evidence/workbuddy/$(date +%F)-models
mkdir -p "$D"
cp ~/.cache/cpa-plugins/sandbox/workbuddy/host.log "$D/host.log"
```

沙箱目录会在下次运行时被清空，所以要留作证据的东西必须在收尾前拷出来。仓库内不留证据与构建产物。

## Cleanup

- `-keep` 留下的宿主：用运行输出里打印的 `pid` 结束它（`kill <pid>`）。按进程名杀会误伤同名的其他实例。
- 起过登录会话就用 `DELETE /v0/management/oauth-session?state=<state>` 取消，否则它会一直挂在宿主的会话表里。
- 清理删实例与临时状态，不删证据。清理完确认证据还在。
- 失败的那一轮也要收尾，否则端口和进程会留到下一轮，把下一个断言变成假阳性。

## Helpers

仓库里的这五个脚本就是本 skill 的手。不要绕过它们手搓同样的动作。

| 命令 | 作用 |
| :--- | :--- |
| `go run scripts/management-api.go -path <管理面路径> [...]` | 管理面唯一入口：封装沙箱 / 文件 / 名字 / 命令四种密钥来源与请求头形式，默认直连，响应体走 stdout 可直接接 `jq`，版本头与拒绝建议走 stderr |
| `go run scripts/check-plugins.go [-release-ready] [-strict]` | 仓库不变量：清单同步、id 与目录名一致、声明平台在 CI 矩阵内、哈希已回填、URL 末段等于宿主期望的资产名。`-release-ready` 作发布门禁 |
| `go run scripts/dev-sandbox.go -plugin <id> [...]` | 构建、装载、注册、模型清单、额度声明的真机断言 |
| `go run scripts/release.go pack --plugin <id> [--version <v>] [--out dist]` | 本地打包并按宿主规则自检包结构，输出 sha256。`record --plugin <id>` 用真实产物哈希回填清单 |
| `go run scripts/verify-registry-install.go [-local] -plugin <a,b>` | 拉清单、下载发布产物、校验 SHA256 与动态库格式（linux 认 ELF，darwin 认 Mach-O） |

协议字段的证据链在另一个仓库：`{pi-codebuddy-provider}/scripts/audit-traffic-diff.ts --session <文件名片段>`，比对插件实际发送的字段与抓包。抓包版本必须与本机客户端版本一致，否则结论无效。

## 特性地图

先读 `features/README.md` 的基线与驱动约定，再进对应特性文件。每条特性都列了用户视角的入口、逐条命令与可观测结果。

- `features/models.md` 模型清单与过滤
- `features/auth-login.md` 扫码登录与凭据归属
- `features/chat.md` 对话（流式与非流式）
- `features/quota.md` 额度声明与拉取
- `features/release-install.md` 打包、清单与安装态

`echo-probe` 是冒烟用的探针插件，走同一套档位，没有独立的特性文件；它的价值是证明"这条流水线本身通"。

地图会随客户端与宿主漂移。发现入口过时就跑 `/maintain-verification-skill` 更新，不要让它烂在原地。
