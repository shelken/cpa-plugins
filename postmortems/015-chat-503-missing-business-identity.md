# 对话固定 503 与 body.business 身份缺失

**日期**: 2026-09-22
**影响**: CN 渠道（qwenwork）对话链路自 2026-09-17 起全部不可用；管理面配额、模型列表正常，插件与 pi provider 两个实现同时中招；排查耗时约一天，绕了四条错误假设
**发现人**: shelken / agent

## 问题

qwenwork 渠道登录、积分、模型列表全部正常，但任何模型发起的普通对话都固定返回
`503 {"code":"503","message":"Model catalog unavailable"}`；同一账号用官方桌面端
（QwenWorkCN 1.0.6）却能正常对话。两个自己维护的实现（Go 插件、TypeScript provider）
都复现，社区三个第三方实现（`rockswang/wild-work#31`、`wicm84266964/Buddy2api#82`）
同日同症且至今无补丁。

## 现象

最小复现（沙箱宿主 + 官方凭据）：

```bash
SDKROOT=/Library/Developer/CommandLineTools/SDKs/MacOSX26.5.sdk \
  go run scripts/dev-sandbox.go --plugin qwenworkcn --checks load --keep
go run scripts/verify-chat.go -base http://127.0.0.1:18317 \
  -model qwenworkcn/flash -token-file ~/.cache/cpa-plugins/sandbox/qwenworkcn/management-key
```

修复前：`9 项失败`，每条都是 HTTP 503 `{"code":"503","message":"Model catalog unavailable"}`。
直接打上游同样命中（生产宿主、本机、沙箱三处一致，排除网络与去重因素）：

```bash
POST https://gateway.qwenwork.cn/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1
```

对照面：官方客户端同账号同端点 → HTTP 200 SSE，`X-Model-Name: qwork-openai-chat-mode-pool`；
把它那条请求原样重放 → `403 Duplicate request`（服务端按 requestId 去重，证明签名校验已通过）。

## 根因

**实际约束**：CN 网关用**请求体** `business.product` 与 `business.type` 解析该会话的模型目录；
头部 `Cosy-Business-Product` / `Cosy-Business-Type` 只做业务标记，不能替代 body 里的这两个字段。
两个实现都只写了 `business: {version, feature_switches}`，上游解析不到业务身份，于是回
"Model catalog unavailable"。补齐后同一签名链路立即 200。官方客户端 body 里两字段齐全
（`product=qoder_work`、`type=agent`）。

**错误假设（四条弯路，全部证伪）**：

1. `Cosy-Version` 过旧（我们 1.1.32 / 客户端 1.1.59）→ 改 1.1.59 仍 503
2. runtime 签名载荷缺 `security_oauth_token`（官方 `info` 856 字符 / 我们 172 字符）→
   写入真令牌仍 503，写入 `GARBAGE<真令牌>` 也仍 503 → 该字段不影响判定
3. 机器身份形式不对（官方 `Cosy-MachineToken` = 机器 UUID，我们发整串 JWT）→ 替换成官方
   UUID 仍 503
4. `model_config` / `parameters.context_length` / `system` 类型 / `chat_context` / 去掉
   `custom_model` → 单独改任一项仍 503

**缺失检查点**：

- 逐字段单变量试错在"部分对齐"状态下进行，每次失败都可能只是**另一个未对齐字段**导致，
  无法据此排除任何假设；正确顺序是先做一次"整体克隆"确认可修复，再二分回退。
- 中途选了错误对照样本：拿官方 `prompt_suggestion` 那条请求的 `business` 值去测普通对话，
  测出 503 后差点把正确方向排除。对照样本必须同场景（同 `sub_task`/`task_id`/`agent_id`）。
- 官方真实对话 body 有 21 个顶层字段、43 个 tools，早期只看了一条 `prompt_suggestion`
  样本，得出"字段集基本一致"的错误结论。

## 修复

两个实现都改为：body `business` 的 `product`/`type` 与自身业务头**同源**，缺失即报错：

- Go 插件 `plugins/qwenworkcn/executor.go`：从 `manifest.RenderHeaderGroup("chat", ...)`
  取 `Cosy-Business-Product`/`Type` 写入 `business`，取不到直接返回错误
- TS provider `src/product/product-profile.ts`：新增 `chatBusinessFromJson()`，CN 两个
  flavor 的 `chat` 身份由 `headers.json` 的 chat 头组派生
- 回归测试各一条（红绿验证：移除字段即失败）
- 文档：`pi-qwenwork-provider/docs/interface/apis/chat.md` 记录该契约

验证：沙箱 `verify-chat` **9 项通过 / 0 失败**；provider `just dev chat` 200，
`routedModel=qwork-openai-chat-mode-pool`（同一账号、同一条签名链路）。

## 预防

- 对接上游时，若同一份业务标识同时存在 header 与 body 两处，两处都要按官方实发值成对写入；
  只对齐 header 不算完成（`Cosy-Business-*` ↔ `body.business.*`）。
- 差分定位官方客户端行为时，先做**整体克隆基线**（仅替换签名/工具变量，其余字段逐字照抄），
  基线跑通后再逐个回退二分；禁止在部分对齐状态下用单变量试错去"排除"假设。
- 选对照样本必须限定同一业务场景：比对前先确认 `sub_task` / `task_id` / `agent_id` 与目标请求一致。
- 每次改动后必须同时验证"修复前失败、修复后成功"的红绿两侧，避免把环境噪声当成结论
  （本轮多次出现"改了没用"其实是被另一处未对齐项掩盖）。
- 抓包留档前先脱敏：样本含 `Authorization`、机器令牌与真实对话内容，不得入库；只留字段骨架与
  结构长度，凭据与正文一律不落仓库。
