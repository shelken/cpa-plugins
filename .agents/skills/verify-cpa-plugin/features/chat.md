# 对话链路

客户端把请求打到宿主, 流式回复逐帧返回, 非流式回复一次返回完整结果

## Sub-features

- `chat-stream` 流式请求按帧返回增量
- `chat-nonstream` 非流式请求由插件聚合上游流式后返回单条完整响应
- `chat-cot` 思维链以 `reasoning_content` 增量下发, 正文以 `content` 下发
- `chat-cache` 多轮与重复请求命中上游前缀缓存
- `chat-memory` 多轮里模型复现前文事实
- `chat-effort` 客户端 `reasoning_effort` 真实改变上游思考深度
- `chat-tools` 工具定义透传, 模型返回 `tool_calls` 并消费工具结果
- `chat-tool-choice` 客户端 `tool_choice` 与 `parallel_tool_calls` 的约束到达上游 (各插件现状见下方 Gotchas: workbuddy 桌面档按协议不发, qwenworkcn 点名函数被网关拒)
- `chat-model` 只接受宿主报送过的模型 id
- `chat-cancel` 客户端断开后, 插件经宿主取消链中止上游请求, 不留僵尸连接 (无脚本判据, 见 Driving it)

## 场景与请求成本

场景 id 由脚本的注册表 (`scenarioRegistry`) 声明, `verify-chat.go -list` 打印权威清单与每项请求成本, 不在此处复制。下表只补充脚本派不出来的语义: 每个场景覆盖哪些判据、何时该跑。

| 场景 id | 覆盖的判据 | 何时该跑 |
|---|---|---|
| `session` | 流式帧合规、正文输出、上下文记忆、多轮缓存(轮次2/3)、用量上报 | 改了消息组装、会话头、请求体构造 |
| `probe` | 重复请求缓存(二次)、用量上报 | 改了缓存相关字段、请求幂等性 |
| `nonstream` | 非流式链路 | 改了流式聚合、`stream:false` 路径 |
| `tools` | 工具调用 流式/非流式、工具结果消费 | 改了工具字段透传、分片拼接 |
| `toolchoice` | 点名函数、`tool_choice=none`、`parallel_tool_calls=false` | 改了 `tool_choice` 或 `parallel_tool_calls` 的上行构造。已知现状 (2026-10-22, 修复前后一致): workbuddy 桌面档 `none` 必 FAIL; qwenworkcn 点名函数被网关 400 并触发凭据冷却, 同次运行的后续请求全部 503 |
| `effort` | 思维链输出、思考深度传递 | 改了推理档位映射、`reasoning_effort` 传递 |
| `guard` | 未知模型拒绝 | 改了模型注册、路由或清单装载 |

默认 (不传 `-scenarios`) 只跑 `session,nonstream`: 二者成本最低且覆盖请求构造主路径。场景按注册顺序执行, 与书写顺序无关。

**加场景**: 写一个 `func(s *session)` 跑请求并 `s.record(...)` 判据, 在 `scenarioRegistry` 注册一行。`-list`、成本合计、参数解析、调度自动跟随, 无需改 main, 也不要在此表外另抄一份清单。

## 判据

判据只在本场景被点名时执行 (见上一节的场景表); 已执行的判据里任一 `FAIL` 即对话链路不可用, 退出码 1。未点名的场景不执行也不报绿。

每个请求轮次都会先记一条健康判据 (`轮次1/2/3 流式`、`探针 首次/二次`、`低档/高档 <effort>`), 覆盖该次请求本身: 状态码、帧数、正文与推理字数; 请求失败或流式缺终止帧直接 `FAIL`。下面这组是链路级判据:

| 判据 | 通过条件 |
|---|---|
| 流式帧合规 | 每帧剥掉宿主补的 `data:` 后都是合法 JSON, 末帧为 `[DONE]` |
| 思维链输出 | 上限档推演轮的增量里 `reasoning_content` 非空; 上游自述 `reasoning_tokens>0` 而流里没有思考帧即 FAIL (插件丢帧) |
| 正文输出 | 复述轮 `content` 非空 |
| 上下文记忆 | 第二轮复现第一轮植入的暗号 |
| 用量上报 | 流式轮次的 `usage` 非空; 只在缺失时记 FAIL, 有则归入该轮健康判据的明细 (缓存判据的前置), 覆盖轮次2/3 与探针二次 |
| 多轮缓存(轮次2/轮次3) | 第二轮起缓存命中数大于 0 |
| 重复请求缓存(二次) | 同一请求连发两次, 第二次命中数大于 0 |
| 思考深度传递 | 最低档与最高档各采样 3 次比中位数: 高档中位大于低档为 PASS; 相等或反序记 WARN (上游未按档位加深, 透传契约由 plugins/<id> 单测钉住); 仅在两档所有采样都没有任何思考输出时 FAIL (思考链路失效)。全采样有 reasoning_tokens 时按 tokens 比, 否则退到推理正文字数 |
| 非流式链路 | 单条完整响应, `object` 为 `chat.completion`, 带 `finish_reason`; `usage` 缺失不判 FAIL |
| 工具调用 流式 | 带 `tools` 与 `tool_choice:auto` 的请求返回 `finish_reason:"tool_calls"`, 每个调用带非空 `id` 与函数名, `arguments` 是合法 JSON |
| 工具调用 非流式 | 同一请求走聚合路径后仍带 `tool_calls` 与合法 `arguments` |
| 工具结果消费 | 把首轮 `tool_calls` 与工具结果带回下一轮, 模型正文引用了工具返回的事实 |
| 点名函数 | `tool_choice` 用对象形态点名 `get_weather` 时, 返回的 `tool_calls` 恰为 1 个且为该函数 (脚本只打印 `finish_reason`, 不判它); 返回别的函数或直接答正文即 FAIL (客户端约束被丢弃) |
| `tool_choice=none` | 同请求带 `tool_choice:"none"` 时不得返回 `tool_calls`, 且要有正文或推理输出; 返回调用即 FAIL |
| `parallel_tool_calls=false` | 显式给出该字段的请求被上游接受 (HTTP 2xx), 且返回的调用数不超过 1 |
| 未知模型拒绝 | 未报送的模型 id 在路由阶段被拒 (400 `model_not_found`); 同沙箱内合法模型会走到凭据阶段 (无凭据时 503 `auth_not_found`), 说明二者处理阶段不同 |

缓存判据要有可命中的公共前缀才有意义, 脚本默认用重复段落撑出足够长的 system 消息, 段落数由 `-prefix` 控制

参数: `-base` 目标实例、`-model <plugin>/<model>`、`-manifest` 静态清单 (默认按模型 id 的插件段推导)、`-scenarios` 场景子集、`-prefix` 前缀段落数、`-timeout` 单请求超时 (默认 180s)、`-token-file` 沙箱密钥、`-list` 打印场景清单

## How to get to it (user POV)

- 任意 OpenAI 兼容客户端指向宿主, 选该渠道模型发消息
- 直接 `POST /v1/chat/completions`

## Driving it

Preconditions:

- 实例经 Doctor 确认可用, 插件 `registered` 且 `enabled`
- 真实对话需要可用凭据 (`auth-files` 中目标渠道 `status` 为 `active`); 凭据可用性先于一切链路判断 (postmortems/003)
- 密钥: 生产经 `sec-run`, 沙箱经 `-token-file ~/.cache/cpa-plugins/sandbox/<id>/management-key`

- **按场景跑判据。** 脚本只执行点名的场景, 未点名的判据不执行也不报绿, 退出码 1 表示有失败项:
  ```bash
  # 改了请求构造: 只跑主路径两个场景
  go run scripts/verify-chat.go -base http://<host>:8317 -model <id>/<model> \
    -manifest plugins/<id>/data/static-config.json -scenarios session,nonstream

  # 改了工具透传或推理档位: 追加对应场景
  go run scripts/verify-chat.go -base http://<host>:8317 -model <id>/<model> \
    -manifest plugins/<id>/data/static-config.json -scenarios tools,effort

  # 发布门禁 / 协议层改动: 全量
  go run scripts/verify-chat.go -base http://<host>:8317 -model <id>/<model> \
    -manifest plugins/<id>/data/static-config.json -scenarios all
  ```
  qwenworkcn 跑 `all` 时 `toolchoice` 排在 `effort` 前 (按注册顺序执行, 改书写顺序无效), 其网关 400 会让凭据冷却约一分钟, `effort` 随之全部 503。先单独跑除 `toolchoice` 外的场景, 冷却结束后再单独跑 `toolchoice`; 冷却期的 503 不算 `effort` 失败
  `-manifest` 可省略, 默认按 `-model` 的插件段推导为 `plugins/<plugin>/data/static-config.json`

- **工具调用是独立场景。** 无需手工发请求, 首轮带 `get_weather` 定义断言 `finish_reason:"tool_calls"` 与合法 `arguments`, 次轮带回调用与结果断言模型引用了结果; 只在点名 `-scenarios tools` 时执行

- **下游取消链 (`chat-cancel`)。** 没有脚本判据。插件侧由单测钉住: `cd plugins/<id> && go test -race .` 中的 `TestExecuteStreamDownstreamCancelAbortsUpstreamRead` (宿主 `stream_read` 报错后插件退出并关闭上游流) 与 `TestExecuteStreamForwardsHostCallbackID` (上游请求携带下游 `host_callback_id`, 宿主据此级联取消)。真机只能做对照观察: 修复前后两个沙箱各发一次流式长输出请求, 300ms 内 (首字节前) 断开, 用 `nettop -P -p <宿主 pid> -s 1 -J bytes_in -x` 看断开后宿主是否还在收这次请求的上游响应, 并对照宿主日志排除后台模型刷新; 结论只作人工判断, 不作通过判据

- **跑构造层单测。** 不依赖上游凭据, 改动请求构造后先跑 (这是零成本, 不需要选场景):
  ```bash
  cd plugins/<id> && go test ./...
  ```
  以 workbuddy 为例, `TestSSEPayloadStripsFramingPrefix` 钉住下发载荷只带一层帧头, `TestAggregateChatStreamRebuildsCompletion` 与 `TestAggregateChatStreamMergesToolCallFragments` 钉住非流式聚合

- **抓包差异审计。** 改过请求头或请求体后, 在对应研究仓跑一次, 聊天接口不应有 `Missing` 项; 抓包版本须与本机客户端版本一致:
  ```bash
  # 在 pi-codebuddy-provider 仓
  bun run scripts/audit-traffic-diff.ts --session WorkBuddy_20260913_171327.json
  ```

## Gotchas

- 连跑判据时不要先 grep 再看结论 -> 过滤会把失败项一起滤掉, 只剩一行「N 项失败」, 事后无法归因; 全量输出落盘再分析, 本轮有一次单点失败因此无法定位
- 判据只写在文档手工步骤里等于没有判据 -> 手工路径要自己拼密钥、拼凭据、拼两轮消息, 任一环断裂就没人再跑, 验收格长期空着也无人发现; 工具调用判据此前就是这样空的, 见 `postmortems/009`
- 别拿复述型提示词判思考 -> 上游是否输出思考由 effort 档与问题难度共同决定, 实测同一账号「只回复两个字」在 low/medium/high 档分别空 4/6、4/6、2/6, max 档 0/6, 参考实现同账号空 5/8; 思考判据要挂推演型提示词加该模型最高档, 挂复述轮必然抖动 (postmortems/009)
- 清单查找要剥任意渠道前缀 -> 宿主注册 id 形如 `<plugin>/<model>`, 清单存裸 id; 按固定插件前缀剥离会让换插件后整个清单查不到, 思考与深度判据被跳过, 表现成一堆 PASS 但实际上关键判据没跑 (postmortems/009)
- 上游只接受流式请求 -> 发 `stream:false` 得到 `400 code 11101`, 宿主据此把凭据标成不可用, 冷却期内连流式一起 `503 auth_unavailable`, 一次非流式调用就能打瘫整条渠道; 非流式必须由插件驱动上游流式端点再聚合
- 宿主给每段载荷补 `data:` 前缀 -> 插件交出去的必须是裸 JSON, 交整行会得到 `data: data: {...}`, 官方 SDK 直接 `JSONDecodeError`
- 冷却不是永久故障 -> 凭据不可用后有 `next_retry_after`, 约一分钟自愈, 重启宿主立即清除; 冷却期失败不能当功能失败
- 缓存看命中数不看总 token -> 命中数为 0 说明公共前缀没被复用, 先检查 `-prefix` 段落数
- 会话级 id 必须整轮不变 -> 会话头每请求新生成会让上游把每轮当新会话, 前缀缓存命中率随请求序列抖动; 只有消息 id 与 trace id 该每次新
- 流式缺 `[DONE]` 或缺 `usage` -> 插件已不挂本地对话超时 (上游连接由宿主 HTTP 流回调管理, 宿主客户端无整体超时), 先排查验收客户端自身的 `-timeout` (默认 180s)、客户端提前断开、上游中断; `verify-chat.go` 会区分「读取中断」与「缺终止帧」
- workbuddy `tool_choice=none` 仍返回 `tool_calls` -> 默认桌面档按官方桌面协议 (docs/adr/plugins/workbuddy/0003) 在上行前删掉 `tool_choice` 与 `parallel_tool_calls` (`plugins/workbuddy/data/static-config.json` 桌面档的删除字段), CLI 档则固定为 `auto`/`true`; 两档都不传客户端约束, 这是插件配置决定的已知缺口, 不是上游忽略了约束
- qwenworkcn 点名函数返回 `upstream gateway error HTTP 400: {"code":"400","message":"Error in upstream response"}` -> 插件把客户端对象原样放进 Cosy `parameters.tool_choice`, 网关在 200 流里回了错误帧; 普通工具调用可用不代表点名约束可用, 是否需要另一种编码尚无协议证据。外层 HTTP 200 不能证明对话成功, 错误藏在流内帧里
- 验证下游取消链别在上游出数据时断开 -> 上游持续出数据时, 下一次 emit 失败就会让插件停读, 取消链断了也看不出来; 只有在上游首字节前断开, 插件才完全依赖宿主取消解除阻塞 (生产 `bytes_sent: 0` 卡几十秒到几分钟的形态)
- 判取消链别数 TCP 连接 -> 上游走 HTTP/2 时取消只发 RST_STREAM, 连接留在池里; 也别把真实上游的进程入站字节写成阈值判据: 同一版本两次运行的断开后入站量相差两个数量级, workbuddy 还会随凭据刷新重拉 live 模型列表 (沙箱实测约每分钟一次 `live model list applied`, 几十 KB~MB 入站)。2026-10-22 的对照实测见 `~/.cache/cpa-plugins/evidence/workbuddy/verify-20261022-cancel-summary.md`
- `403 code 11140 request illegal` -> 该形态曾因账号不可用产生 (postmortems/003), 未用可用账号复现前不要据此改字段
- 桌面档与 CLI 档头部不同 -> 两者不可混用, 默认取桌面档
- 日志边界 -> 见 `docs/adr/plugins/workbuddy/0002`, 只记方法、路径、状态码、耗时与模型名, 不记请求体与回复正文
- 别拿单次采样判档位深度 -> 同一档位内思考量方差极大 (如同一模型同一 prompt 在 low 档一次 100+ tokens 一次 1000+ tokens, 甚至大于档位间差异), 单样本比较是掷硬币; 必须多采样取中位比。插件透传契约由 plugins/<id>/executor_test.go 逐档断言钉住, 真机测试仅反映上游行为
