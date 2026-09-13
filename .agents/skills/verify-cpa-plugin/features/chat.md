# 对话链路

客户端把请求打到宿主，流式回复逐帧返回，非流式回复一次返回完整结果

## Sub-features

- `chat-stream` 流式请求按帧返回增量
- `chat-nonstream` 非流式请求返回单条完整响应
- `chat-headers` 请求头集合与取值关系与真实客户端一致
- `chat-prompt` 提示词按渠道要求转换后再上行
- `chat-model` 只接受宿主报送过的模型 id

## How to get to it (user POV)

- 任意 OpenAI 兼容客户端指向宿主，选该渠道下的模型发消息
- 直接 `POST /v1/chat/completions`

## Driving it with curl

Preconditions:

- 沙箱用 `-keep` 启动后监听 `127.0.0.1:18317`，未配 `api-keys`，调用 `/v1` 不带鉴权头；真实实例的 `/v1` 需要客户端 API key，管理密钥不能替代
- `auth-files` 里有可用凭据。没有可用凭据时本特性停在未跑到，不要用假凭据试

- **流式。** 观察增量是否逐帧到达：
  ```bash
  curl -N -s -H 'Content-Type: application/json' \
    -d '{"model":"hy3","messages":[{"role":"user","content":"你好"}],"stream":true}' \
    http://127.0.0.1:18317/v1/chat/completions
  ```
  输出为 SSE，可见多个 `data:` 增量而不是一次到齐，末帧为 `[DONE]`

- **非流式。** 同上去掉 `"stream":true`，返回单条完整 JSON。上游不支持非流式（错误码 `11101`），因此这条要么由插件聚合 SSE 后返回，要么明确失败，证据里须写明属于哪一种

- **请求保真。** 改过请求头或请求体后，在 `{pi-codebuddy-provider}` 跑一次差异审计，聊天接口不应有 `Missing` 项：
  ```bash
  bun run scripts/audit-traffic-diff.ts --session WorkBuddy_20260913_171327.json
  ```
  抓包版本必须与本机客户端版本一致

- **构造层兜底。** 真机跑之前先挡住构造回归：
  ```bash
  cd plugins/workbuddy && go test ./...
  ```
  `TestBuildChatHeadersMatchDesktopClient` 钉住头部集合、id 同值关系与格式。单测不算生产端证据

- **凭据冷却。** 返回 `503 auth_unavailable` 时先重启宿主再判断，冷却期的失败不能当成对话功能失败

## Gotchas

- 上游只支持流式 -> 非流式客户端能否使用取决于插件的聚合实现，与上游无关
- `403 code 11140 request illegal` -> 曾被怀疑是字段构造问题，当时所用账号已确认不可用，因此尚未定性，在可用账号复现之前不要据此改字段
- 桌面档与 CLI 档头部不同 -> 两者不可混用，默认都取桌面档
- 日志边界 -> 见 `docs/adr/0002`，只记方法、路径、状态码、耗时与模型名，不记请求体与回复正文
