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
- `chat-model` 只接受宿主报送过的模型 id

## 判据

`verify-chat.go` 一次跑完全部判据, 任一 FAIL 即对话链路不可用:

| 判据 | 通过条件 |
|---|---|
| 流式帧合规 | 每帧剥掉宿主补的 `data:` 后都是合法 JSON, 末帧为 `[DONE]` |
| 思维链输出 | 上限档推演轮的增量里 `reasoning_content` 非空; 上游自述 `reasoning_tokens>0` 而流里没有思考帧即 FAIL (插件丢帧) |
| 正文输出 | 复述轮 `content` 非空 |
| 上下文记忆 | 第二轮复现第一轮植入的暗号 |
| 用量上报 | 每个流式轮次 `usage` 非空, 缺失直接 FAIL (缓存判据的前置) |
| 多轮缓存(轮次2/轮次3) | 第二轮起缓存命中数大于 0 |
| 重复请求缓存(二次) | 同一请求连发两次, 第二次命中数大于 0 |
| 思考深度传递 | 高档 `reasoning_tokens` 大于低档 (档位无数值差异时 WARN) |
| 非流式链路 | 单条完整响应, `object` 为 `chat.completion`, 带 `finish_reason`; `usage` 缺失不判 FAIL |
| 工具调用 流式 | 带 `tools` 与 `tool_choice:auto` 的请求返回 `finish_reason:"tool_calls"`, 函数名非空, `arguments` 是合法 JSON |
| 工具调用 非流式 | 同一请求走聚合路径后仍带 `tool_calls` 与合法 `arguments` |
| 工具结果消费 | 把首轮 `tool_calls` 与工具结果带回下一轮, 模型正文引用了工具返回的事实 |

缓存判据要有可命中的公共前缀才有意义, 脚本默认用重复段落撑出足够长的 system 消息, 段落数由 `-prefix` 控制

## How to get to it (user POV)

- 任意 OpenAI 兼容客户端指向宿主, 选该渠道模型发消息
- 直接 `POST /v1/chat/completions`

## Driving it

Preconditions:

- 实例经 Doctor 确认可用, 插件 `registered` 且 `enabled`
- 真实对话需要可用凭据 (`auth-files` 中目标渠道 `status` 为 `active`); 凭据可用性先于一切链路判断 (postmortems/003)
- 密钥: 生产经 `sec-run`, 沙箱经 `-token-file ~/.cache/cpa-plugins/sandbox/<id>/management-key`

- **跑全量判据。** 脚本按 `-manifest` 清单取该模型的档位声明, 逐项打印判据与结论, 有失败项退出码为 1:
  ```bash
  go run scripts/verify-chat.go -base http://<host>:8317 -model <id>/<model> -manifest plugins/<id>/data/static-config.json
  ```

- **工具调用已并入全量判据。** 无需手工发请求, 首轮带 `get_weather` 定义断言 `finish_reason:"tool_calls"` 与合法 `arguments`, 次轮带回调用与结果断言模型引用了结果

- **跑构造层单测。** 不依赖上游凭据, 改动请求构造后先跑:
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

- 判据只写在文档手工步骤里等于没有判据 -> 手工路径要自己拼密钥、拼凭据、拼两轮消息, 任一环断裂就没人再跑, 验收格长期空着也无人发现; 工具调用判据此前就是这样空的, 见 `postmortems/009`
- 别拿复述型提示词判思考 -> 上游是否输出思考由 effort 档与问题难度共同决定, 实测同一账号「只回复两个字」在 low/medium/high 档分别空 4/6、4/6、2/6, max 档 0/6, 参考实现同账号空 5/8; 思考判据要挂推演型提示词加该模型最高档, 挂复述轮必然抖动 (postmortems/009)
- 清单查找要剥任意渠道前缀 -> 宿主注册 id 形如 `<plugin>/<model>`, 清单存裸 id; 按固定插件前缀剥离会让换插件后整个清单查不到, 思考与深度判据被跳过, 表现成一堆 PASS 但实际上关键判据没跑 (postmortems/009)
- 上游只接受流式请求 -> 发 `stream:false` 得到 `400 code 11101`, 宿主据此把凭据标成不可用, 冷却期内连流式一起 `503 auth_unavailable`, 一次非流式调用就能打瘫整条渠道; 非流式必须由插件驱动上游流式端点再聚合
- 宿主给每段载荷补 `data:` 前缀 -> 插件交出去的必须是裸 JSON, 交整行会得到 `data: data: {...}`, 官方 SDK 直接 `JSONDecodeError`
- 冷却不是永久故障 -> 凭据不可用后有 `next_retry_after`, 约一分钟自愈, 重启宿主立即清除; 冷却期失败不能当功能失败
- 缓存看命中数不看总 token -> 命中数为 0 说明公共前缀没被复用, 先检查 `-prefix` 段落数
- 会话级 id 必须整轮不变 -> 会话头每请求新生成会让上游把每轮当新会话, 前缀缓存命中率随请求序列抖动; 只有消息 id 与 trace id 该每次新
- 流式缺 `[DONE]` 或缺 `usage` -> 先怀疑响应体被中途掐断: 插件侧挂整体超时会让长思考流读一半断连, 尾部帧连 `usage` 一起消失; `verify-chat.go` 会区分「读取中断」与「缺终止帧」
- `403 code 11140 request illegal` -> 该形态曾因账号不可用产生 (postmortems/003), 未用可用账号复现前不要据此改字段
- 桌面档与 CLI 档头部不同 -> 两者不可混用, 默认取桌面档
- 日志边界 -> 见 `docs/adr/plugins/workbuddy/0002`, 只记方法、路径、状态码、耗时与模型名, 不记请求体与回复正文
