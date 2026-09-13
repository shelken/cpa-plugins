# 对话

用户视角：把某个客户端指到这个渠道，发一句话能拿到正常回复，长回复是边生成边出来的。

## Sub-features

- `chat-stream` 流式请求按帧返回增量。
- `chat-nonstream` 非流式请求返回单条完整响应。
- `chat-headers` 请求头集合与取值关系与真实客户端一致。
- `chat-prompt` 提示词按渠道要求转换后再上行。
- `chat-model` 只接受宿主报送过的模型 id。

## How to get to it (user POV)

- 任意 OpenAI 兼容客户端指向宿主，选该渠道下的模型发消息。
- 直接 `POST /v1/chat/completions`。

## Driving it with the management API

Preconditions:

- 沙箱以 `-keep` 启动，`A="Authorization: Bearer $(cat ~/.cache/cpa-plugins/sandbox/workbuddy/management-key)"`。
- `auth-files` 里有可用凭据。**没有可用凭据时本特性停在未跑到，不要用假凭据试。**

- **流式。** `curl -N -s -H "$A" -H 'Content-Type: application/json' -d '{"model":"hy3","messages":[{"role":"user","content":"你好"}],"stream":true}' http://127.0.0.1:18317/v1/chat/completions`。输出是逐帧到达的 SSE，能看到多个 `data:` 增量而不是一次性返回，最后一帧是 `[DONE]`。
- **非流式。** 同上去掉 `"stream":true`。返回单条完整 JSON。上游本身不支持非流式（`11101`），因此这条要么由插件内部聚合 SSE 后返回，要么明确失败；两种情况都要在证据里写清是哪一种。
- **请求保真。** 改过请求头或请求体后，在 `{pi-codebuddy-provider}` 里跑 `bun run scripts/audit-traffic-diff.ts --session WorkBuddy_20260913_171327.json`，聊天接口不应有 `Missing` 项。抓包版本必须与本机客户端版本一致。
- **构造层兜底。** `cd plugins/workbuddy && go test ./...`，`TestBuildChatHeadersMatchDesktopClient` 钉住头部集合、id 同值关系与格式。单测不算生产端证据，但它能在真机跑之前挡住构造回归。
- **凭据冷却。** 若返回 `503 auth_unavailable`，重启宿主后再判断，不要把冷却期的失败当成对话功能失败。

## Gotchas

- 上游只支持流式。非流式客户端能不能用，取决于插件的聚合实现，不取决于上游。
- `403 code 11140 request illegal` 曾被怀疑是字段构造问题，但当时用的账号已确认不可用，因此尚未定性。**在可用账号上复现之前，不要据此改字段。**
- 桌面档与 CLI 档的头部不同，两者不可混用；默认都取桌面档。
- 桌面档在 chat 上不发 `x-session-id`，而额度接口的请求体字段必须全用字符串形态。改动前先看抓包，不要凭常识推断。
- 日志边界见 `docs/adr/0002`：只记方法、路径、状态码、耗时与模型名，不记请求体与回复正文。
