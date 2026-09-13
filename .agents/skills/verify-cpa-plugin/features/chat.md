# 对话链路

客户端把请求打到宿主，流式回复逐帧返回，非流式回复一次返回完整结果

## Sub-features

- `chat-stream` 流式请求按帧返回增量
- `chat-nonstream` 非流式请求由插件聚合上游流式后返回单条完整响应
- `chat-cot` 思维链以 `reasoning_content` 增量下发，正文以 `content` 下发
- `chat-cache` 多轮与重复请求能命中上游前缀缓存
- `chat-memory` 多轮里模型记得住前文给出的事实
- `chat-effort` 客户端的 `reasoning_effort` 真的改变了上游的思考深度
- `chat-headers` 请求头集合与取值关系与真实客户端一致
- `chat-model` 只接受宿主报送过的模型 id

## 判据

一次跑完六项，任一项失败即视为对话链路不可用

| 指标 | 通过条件 |
|---|---|
| 流式帧合规 | 每帧剥掉宿主补的一层 `data:` 后都是合法 JSON，末帧为 `[DONE]` |
| 思维链输出 | 增量里 `reasoning_content` 非空 |
| 上下文记忆 | 第二轮复现第一轮植入的暗号 |
| 多轮缓存 | 第二轮 `prompt_cache_hit_tokens` 大于 0 |
| 重复请求缓存 | 同一请求连发两次，第二次命中数大于 0 |
| 思考深度传递 | 高档 `reasoning_tokens` 大于低档 |
| 非流式链路 | 返回 `object` 为 `chat.completion` 的单条完整响应，带 `finish_reason` 与 `usage` |

缓存这一项要有可命中的公共前缀才有意义，脚本默认用重复段落撑出足够长的 system 消息，段落数由 `-prefix` 控制

## How to get to it (user POV)

- 任意 OpenAI 兼容客户端指向宿主，选该渠道下的模型发消息
- 直接 `POST /v1/chat/completions`

## Driving it

管理密钥只经 `sec-run printenv CPA_TOKEN` 读取，客户端密钥由脚本就地取自管理面，两处都不打印

```bash
go run scripts/verify-chat.go -base http://<host>:8317 -model hy3
```

脚本按 `plugins/workbuddy/data/static-config.json` 取该模型的档位声明，逐项打印判据与结论，有失败项时退出码为 1

以下三项不依赖上游凭据，改动构造层后先跑它们

```bash
cd plugins/workbuddy && go test ./...
```

`TestSSEPayloadStripsFramingPrefix` 钉住下发载荷只带一层帧头，`TestAggregateChatStreamRebuildsCompletion` 与 `TestAggregateChatStreamMergesToolCallFragments` 钉住非流式聚合的正文、推理、工具调用与用量

改过请求头或请求体后，在 `{pi-codebuddy-provider}` 跑一次差异审计，聊天接口不应有 `Missing` 项

```bash
bun run scripts/audit-traffic-diff.ts --session WorkBuddy_20260913_171327.json
```

抓包版本必须与本机客户端版本一致

## Gotchas

- 上游只接受流式请求 -> 发 `stream:false` 会得到 `400` 与 `code 11101`，宿主据此把凭据标成不可用，冷却期内连流式一起 `503 auth_unavailable`，一次非流式调用就能打瘫整条渠道，所以非流式必须由插件驱动上游流式端点再聚合
- 宿主自己给每段载荷补 `data:` 前缀 -> 插件交出去的必须是裸 JSON，交整行会得到 `data: data: {...}`，官方 SDK 直接 `JSONDecodeError`
- 冷却不是永久故障 -> 凭据被打成不可用后有 `next_retry_after`，约一分钟自愈，重启宿主也能立即清掉，冷却期的失败不能当成功能失败
- 缓存要看命中数而不是总 token -> `prompt_cache_hit_tokens` 与 `prompt_cache_miss_tokens` 分别在 `usage` 里，命中率为 0 说明公共前缀没被复用
- 会话级 id 必须整轮不变 -> `X-Conversation-ID` 与 `X-Conversation-Request-ID` 若每次请求新生成，上游会把每一轮都当成新会话，前缀缓存命中率随请求序列抖动（同一会话里有的轮次命中、有的为 0），只有消息 id 与 trace id 该每次新
- 流式缺 `[DONE]` 或没有 `usage` -> 先怀疑响应体被中途掐断，而不是上游没上报：插件侧对话请求若挂了整体超时（如 `http.Client.Timeout`），长思考流会在读到一半时断连，尾部帧连 `usage` 一起消失，表现为缓存命中与 `reasoning_tokens` 时有时无；`verify-chat.go` 会区分「读取中断」与「缺终止帧」两类失败
- `403 code 11140 request illegal` -> 曾被怀疑是字段构造问题，当时所用账号已确认不可用，因此尚未定性，在可用账号复现之前不要据此改字段
- 桌面档与 CLI 档头部不同 -> 两者不可混用，默认都取桌面档
- 日志边界 -> 见 `docs/adr/plugins/workbuddy/0002`，只记方法、路径、状态码、耗时与模型名，不记请求体与回复正文
