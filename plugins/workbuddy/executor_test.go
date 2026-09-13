package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var (
	reHex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)
	reUUID  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

func TestCleanMessages(t *testing.T) {
	manifest := &ManifestV2{
		Sanitizations: []SanitizationRule{
			{Pattern: "Claude Code", Replacement: "CodeBuddy"},
		},
	}

	messages := []chatMessage{
		{
			Role:    "user",
			Content: "Hello Claude Code!",
		},
		{
			Role:       "assistant",
			StopReason: "error", // should be dropped
			Content:    "Partial response",
		},
		{
			Role: "assistant",
			ToolCalls: []toolCall{
				{
					ID:   "call_invalid",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "test_fn",
						Arguments: "{invalid-json}",
					},
				},
				{
					ID:   "call_valid",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "test_fn",
						Arguments: `{"key":"value"}`,
					},
				},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "call_invalid", // should be dropped because parent call was dropped
			Content:    "Result",
		},
	}

	cleaned := cleanMessages(messages, false, manifest)

	// Message 0 should be sanitized user message
	if cleaned[0].Content != "Hello CodeBuddy!" {
		t.Errorf("expected sanitized user content, got %v", cleaned[0].Content)
	}

	// Message 1 (error assistant) should be dropped
	// Message 2 assistant should have only 1 valid tool call
	var asstMsg *chatMessage
	for i := range cleaned {
		if cleaned[i].Role == "assistant" {
			asstMsg = &cleaned[i]
			break
		}
	}
	if asstMsg == nil {
		t.Fatal("expected assistant message in cleaned messages")
	}
	if len(asstMsg.ToolCalls) != 1 {
		t.Fatalf("expected 1 valid tool call, got %d", len(asstMsg.ToolCalls))
	}
	if asstMsg.ToolCalls[0].ID != "call_valid" {
		t.Errorf("expected call_valid, got %s", asstMsg.ToolCalls[0].ID)
	}

	// Orphan tool call for call_valid should be synthesized at the end
	lastMsg := cleaned[len(cleaned)-1]
	if lastMsg.Role != "tool" || lastMsg.ToolCallID != "call_valid" {
		t.Errorf("expected synthesized orphan tool message, got %+v", lastMsg)
	}
}

func TestBuildChatHeadersMatchDesktopClient(t *testing.T) {
	profile := ProfileConfig{
		Headers: map[string]map[string]string{
			"chat": {"X-Agent-Purpose": "conversation", "X-Product": "SaaS"},
		},
	}
	cred := &Credential{UserID: "user-1", Credentials: TokenCredentials{Access: "token-1"}}

	headers := buildChatHeaders(&profile, cred, "session-1")

	if got := headers.Get("Authorization"); got != "Bearer token-1" {
		t.Errorf("authorization = %q, want Bearer token-1", got)
	}
	if got := headers.Get("X-Agent-Purpose"); got != "conversation" {
		t.Errorf("清单头部未透传, X-Agent-Purpose = %q", got)
	}
	if got := headers.Get("X-Session-ID"); got != "" {
		t.Errorf("桌面客户端在对话接口上不发送 X-Session-ID, 实际 %q", got)
	}
	if got := headers.Get("X-Codebuddy-Request"); got != "1" {
		t.Errorf("X-Codebuddy-Request = %q, want 1", got)
	}
	if got := headers.Get("X-Requested-With"); got != "XMLHttpRequest" {
		t.Errorf("X-Requested-With = %q, want XMLHttpRequest", got)
	}

	// 请求 id 与消息 id 相同, 会话请求 id 与根请求 id 相同
	if msgID, reqID := headers.Get("X-Conversation-Message-ID"), headers.Get("X-Request-ID"); msgID == "" || msgID != reqID {
		t.Errorf("消息 id 应与请求 id 相同, 实际 %q / %q", msgID, reqID)
	}
	if convReqID, rootID := headers.Get("X-Conversation-Request-ID"), headers.Get("X-Root-Request-ID"); convReqID == "" || convReqID != rootID {
		t.Errorf("会话请求 id 应与根请求 id 相同, 实际 %q / %q", convReqID, rootID)
	}

	// 三类追踪 id 为 32 位无横线十六进制; 会话 id 与连接 id 为带横线 UUID
	for _, name := range []string{"X-Conversation-Message-ID", "X-Request-ID", "X-Conversation-Request-ID", "X-Root-Request-ID", "X-Trace-Id"} {
		if !reHex32.MatchString(headers.Get(name)) {
			t.Errorf("%s 应为 32 位十六进制, 实际 %q", name, headers.Get(name))
		}
	}
	for _, name := range []string{"X-Conversation-ID", "Acp-Connection-Id"} {
		if !reUUID.MatchString(headers.Get(name)) {
			t.Errorf("%s 应为带横线 UUID, 实际 %q", name, headers.Get(name))
		}
	}
}

func TestChatHeadersKeepConversationIdentityStable(t *testing.T) {
	profile := ProfileConfig{}
	cred := &Credential{UserID: "user-1", Credentials: TokenCredentials{Access: "token-1"}}

	first := buildChatHeaders(&profile, cred, "session-1")
	second := buildChatHeaders(&profile, cred, "session-1")
	other := buildChatHeaders(&profile, cred, "session-2")

	// 整轮对话里会话 id 与会话请求 id 必须不变, 否则上游每一轮都当成新会话, 前缀缓存无法复用。
	if first.Get("X-Conversation-ID") != second.Get("X-Conversation-ID") {
		t.Errorf("同一会话的会话 id 发生变化: %q -> %q", first.Get("X-Conversation-ID"), second.Get("X-Conversation-ID"))
	}
	if first.Get("X-Conversation-Request-ID") != second.Get("X-Conversation-Request-ID") {
		t.Errorf("同一会话的会话请求 id 发生变化: %q -> %q",
			first.Get("X-Conversation-Request-ID"), second.Get("X-Conversation-Request-ID"))
	}
	// 不同会话不能共用标识, 否则上游会把两次对话的前缀混在一起。
	if first.Get("X-Conversation-ID") == other.Get("X-Conversation-ID") {
		t.Errorf("不同会话共用会话 id: %q", other.Get("X-Conversation-ID"))
	}
	if first.Get("X-Conversation-Request-ID") == other.Get("X-Conversation-Request-ID") {
		t.Errorf("不同会话共用会话请求 id: %q", other.Get("X-Conversation-Request-ID"))
	}
	// 会话 id 与会话请求 id 是两个不同的取值。
	if first.Get("X-Conversation-ID") == first.Get("X-Conversation-Request-ID") {
		t.Error("会话 id 与会话请求 id 撞成同一个值")
	}
	// 消息 id 每次请求都要新, 否则上游会把它当成同一条消息的重复提交。
	if first.Get("X-Conversation-Message-ID") == second.Get("X-Conversation-Message-ID") {
		t.Error("消息 id 应每次请求都变")
	}
}

func TestSessionSeedUsesHostCanonicalSessionID(t *testing.T) {
	profile := ProfileConfig{}
	cred := &Credential{UserID: "user-1", Credentials: TokenCredentials{Access: "token-1"}}

	firstTurn := pluginapi.ExecutorRequest{
		Metadata:        map[string]any{canonicalSessionIDKey: "host-session-7"},
		OriginalRequest: []byte(`{"model":"hy3","messages":[{"role":"user","content":"第一轮"}]}`),
	}
	thirdTurn := pluginapi.ExecutorRequest{
		Metadata: map[string]any{canonicalSessionIDKey: "host-session-7"},
		OriginalRequest: []byte(`{"model":"hy3","messages":[{"role":"user","content":"第一轮"},` +
			`{"role":"assistant","content":"好"},{"role":"user","content":"第三轮"}]}`),
	}
	noMetadata := pluginapi.ExecutorRequest{
		OriginalRequest: firstTurn.OriginalRequest,
	}

	hostFirst := buildChatHeaders(&profile, cred, sessionSeed(firstTurn, firstTurn.OriginalRequest))
	hostThird := buildChatHeaders(&profile, cred, sessionSeed(thirdTurn, thirdTurn.OriginalRequest))
	fallback := buildChatHeaders(&profile, cred, sessionSeed(noMetadata, noMetadata.OriginalRequest))

	if hostFirst.Get("X-Conversation-ID") != hostThird.Get("X-Conversation-ID") {
		t.Errorf("宿主给出的规范会话 id 未被跨轮沿用: %q -> %q",
			hostFirst.Get("X-Conversation-ID"), hostThird.Get("X-Conversation-ID"))
	}
	if hostFirst.Get("X-Conversation-ID") == fallback.Get("X-Conversation-ID") {
		t.Error("宿主给出的规范会话 id 被忽略, 结果与兜底取值相同")
	}
}

func TestSessionSeedStableAcrossTurnsWithoutMetadata(t *testing.T) {
	prefix := `{"model":"hy3","messages":[{"role":"system","content":"背景资料"},{"role":"user","content":"第一轮"}`
	firstTurn := []byte(prefix + `]}`)
	secondTurn := []byte(prefix + `,{"role":"assistant","content":"好"},{"role":"user","content":"第二轮"}]}`)
	otherConversation := []byte(`{"model":"hy3","messages":[{"role":"system","content":"背景资料"},` +
		`{"role":"user","content":"另一件事"}]}`)

	account := pluginapi.ExecutorRequest{AuthID: "auth-1"}

	if a, b := sessionSeed(account, firstTurn), sessionSeed(account, secondTurn); a != b {
		t.Errorf("缺元数据时同一会话的不同轮次得到不同种子: %q / %q", a, b)
	}
	if a, c := sessionSeed(account, firstTurn), sessionSeed(account, otherConversation); a == c {
		t.Errorf("缺元数据时不同对话得到相同种子: %q", a)
	}
	if a, d := sessionSeed(account, firstTurn), sessionSeed(pluginapi.ExecutorRequest{AuthID: "auth-2"}, firstTurn); a == d {
		t.Error("缺元数据时不同账号得到相同种子")
	}
}

func TestQuotaRequestPayloadMatchesDesktopClient(t *testing.T) {
	raw, err := json.Marshal(quotaRequestPayload{
		PageNumber:  1,
		PageSize:    100,
		ProductCode: "p_tcaca",
		Status:      []int{0, 3},
	})
	if err != nil {
		t.Fatalf("marshal quota payload: %v", err)
	}

	// 形状照抄 data/static-config.json requestBodies.userResource 抓包 (客户端 5.3.14)。
	// 字段是数字/数组, 不含 OnlyValidPeriod; 上游会把字符串字段反序列化成 bool/int 失败。
	want := `{"PageNumber":1,"PageSize":100,"ProductCode":"p_tcaca","Status":[0,3]}`
	if string(raw) != want {
		t.Errorf("额度请求体形状不符\n实际: %s\n期望: %s", raw, want)
	}
}

func TestPrepareChatRequestBodyProfileDifferences(t *testing.T) {
	manifest := &ManifestV2{
		Endpoints: EndpointsConfig{
			BaseURL: "https://copilot.tencent.com",
		},
		Profiles: map[string]ProfileConfig{
			"desktop": {
				Body: ProfileBodyConfig{
					Omit:      []string{"tool_choice", "store", "parallel_tool_calls"},
					Set:       map[string]any{"verbosity": "high", "reasoning_summary": "auto"},
					ExtraVars: true,
				},
			},
			"cli": {
				Body: ProfileBodyConfig{
					Omit:      []string{},
					Set:       map[string]any{"store": false, "reasoning_summary": "auto"},
					ExtraVars: false,
				},
			},
		},
	}

	reqPayload := []byte(`{
		"model": "deepseek-v4-pro",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{"type": "function", "function": {"name": "test"}}],
		"tool_choice": "auto",
		"parallel_tool_calls": true
	}`)

	desktopProf := manifest.Profiles["desktop"]
	desktopBody, err := prepareChatRequestBody(mustParseConfig(t, nil), reqPayload, manifest, &desktopProf, true)
	if err != nil {
		t.Fatalf("desktop prepare failed: %v", err)
	}
	var desktopMap map[string]any
	if err := json.Unmarshal(desktopBody, &desktopMap); err != nil {
		t.Fatalf("unmarshal desktop body failed: %v", err)
	}

	if _, ok := desktopMap["tool_choice"]; ok {
		t.Error("expected tool_choice omitted in desktop profile")
	}
	if _, ok := desktopMap["parallel_tool_calls"]; ok {
		t.Error("expected parallel_tool_calls omitted in desktop profile")
	}
	if desktopMap["verbosity"] != "high" {
		t.Errorf("expected verbosity high in desktop profile, got %v", desktopMap["verbosity"])
	}
	if _, ok := desktopMap["extra_vars"]; !ok {
		t.Error("expected extra_vars in desktop profile")
	}

	cliProf := manifest.Profiles["cli"]
	cliBody, err := prepareChatRequestBody(mustParseConfig(t, nil), reqPayload, manifest, &cliProf, true)
	if err != nil {
		t.Fatalf("cli prepare failed: %v", err)
	}
	var cliMap map[string]any
	if err := json.Unmarshal(cliBody, &cliMap); err != nil {
		t.Fatalf("unmarshal cli body failed: %v", err)
	}

	if cliMap["tool_choice"] != "auto" {
		t.Errorf("expected tool_choice auto in cli profile, got %v", cliMap["tool_choice"])
	}
	if cliMap["store"] != false {
		t.Errorf("expected store false in cli profile, got %v", cliMap["store"])
	}
}

func TestSSEPayloadStripsFramingPrefix(t *testing.T) {
	// 宿主会补自己的 data: 前缀, 插件只能交出裸载荷, 交整行就会变成 "data: data: {...}"。
	if payload, ok := ssePayload(`data: {"id":"abc"}`); !ok || payload != `{"id":"abc"}` {
		t.Errorf("应剥掉一层 data: 前缀, 实际 ok=%v payload=%q", ok, payload)
	}
	if _, ok := ssePayload("data:   "); ok {
		t.Error("空载荷的心跳帧不应下发")
	}
	if _, ok := ssePayload(": keep-alive"); ok {
		t.Error("非 data 行不应下发")
	}
}

func TestPrepareChatRequestBodyStripsModelPrefix(t *testing.T) {
	manifest := &ManifestV2{
		Endpoints: EndpointsConfig{BaseURL: "https://copilot.tencent.com"},
		Profiles: map[string]ProfileConfig{
			"desktop": {},
		},
		Models: []ManifestModel{
			{ID: "hy3", SupportsReasoning: true, DefaultReasoningEffort: "medium"},
		},
	}
	reqPayload := []byte(`{"model": "workbuddy/hy3", "messages": [{"role": "user", "content": "hi"}]}`)

	body, err := prepareChatRequestBody(mustParseConfig(t, nil), reqPayload, manifest, &ProfileConfig{}, true)
	if err != nil {
		t.Fatalf("prepare failed: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if out["model"] != "hy3" {
		t.Errorf("expected upstream model hy3, got %v", out["model"])
	}
	if out["reasoning_effort"] != "medium" {
		t.Errorf("expected reasoning_effort medium from manifest default, got %v", out["reasoning_effort"])
	}
}

func TestAggregateChatStreamRebuildsCompletion(t *testing.T) {
	raw := []byte(strings.Join([]string{
		`data: {"id":"abc","model":"hy3","object":"chat.completion.chunk","created":1789305164,"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":""}],"usage":null}`,
		`data: {"id":"abc","model":"hy3","object":"chat.completion.chunk","created":1789305164,"choices":[{"index":0,"delta":{"content":"","reasoning_content":"先算"},"finish_reason":""}],"usage":null}`,
		`data: {"id":"abc","model":"hy3","object":"chat.completion.chunk","created":1789305164,"choices":[{"index":0,"delta":{"content":"","reasoning_content":"再算"},"finish_reason":""}],"usage":null}`,
		`data: {"id":"abc","model":"hy3","object":"chat.completion.chunk","created":1789305164,"choices":[{"index":0,"delta":{"content":"链路正常"},"finish_reason":""}],"usage":null}`,
		`data: {"id":"abc","model":"hy3","object":"chat.completion.chunk","created":1789305164,"choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":31,"total_tokens":51,"completion_tokens_details":{"reasoning_tokens":27}}}`,
		`data: [DONE]`,
		``,
	}, "\n"))

	out, err := aggregateChatStream(raw)
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}

	var resp struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Model   string `json:"model"`
		Created int64  `json:"created"`
		Choices []struct {
			Index        int    `json:"index"`
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role             string `json:"role"`
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			Details          struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("聚合结果不是合法 JSON: %v, 原文 %s", err, out)
	}

	if resp.Object != "chat.completion" {
		t.Errorf("object 应为 chat.completion, 实际 %q", resp.Object)
	}
	if resp.ID != "abc" || resp.Model != "hy3" || resp.Created != 1789305164 {
		t.Errorf("标识字段未透传: id=%q model=%q created=%d", resp.ID, resp.Model, resp.Created)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("应聚合成单条 choice, 实际 %d 条", len(resp.Choices))
	}
	choice := resp.Choices[0]
	if choice.Message.Role != "assistant" {
		t.Errorf("role 应为 assistant, 实际 %q", choice.Message.Role)
	}
	if choice.Message.Content != "链路正常" {
		t.Errorf("正文增量未续接, 实际 %q", choice.Message.Content)
	}
	if choice.Message.ReasoningContent != "先算再算" {
		t.Errorf("推理增量未续接, 实际 %q", choice.Message.ReasoningContent)
	}
	if choice.FinishReason != "stop" {
		t.Errorf("finish_reason 应为 stop, 实际 %q", choice.FinishReason)
	}
	if resp.Usage.PromptTokens != 20 || resp.Usage.CompletionTokens != 31 || resp.Usage.Details.ReasoningTokens != 27 {
		t.Errorf("用量未透传: %+v", resp.Usage)
	}
}

func TestAggregateChatStreamMergesToolCallFragments(t *testing.T) {
	raw := []byte(strings.Join([]string{
		`data: {"id":"t1","model":"hy3","created":1,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"ci"}}]},"finish_reason":""}],"usage":null}`,
		`data: {"id":"t1","model":"hy3","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ty\":\"hz\"}"}}]},"finish_reason":"tool_calls"}],"usage":null}`,
		`data: [DONE]`,
		``,
	}, "\n"))

	out, err := aggregateChatStream(raw)
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}

	var resp struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []map[string]any `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("聚合结果不是合法 JSON: %v", err)
	}
	if len(resp.Choices) != 1 || len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("应合并成 1 个工具调用, 实际 %+v", resp.Choices)
	}
	call := resp.Choices[0].Message.ToolCalls[0]
	if call["id"] != "call_1" {
		t.Errorf("工具调用 id 丢失, 实际 %v", call["id"])
	}
	// 非流式响应的 message.tool_calls 不带 index, 带上会让部分客户端解析失败。
	if _, ok := call["index"]; ok {
		t.Error("聚合后的工具调用不应保留流式的 index 字段")
	}
	fn, _ := call["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("函数名丢失, 实际 %v", fn["name"])
	}
	if fn["arguments"] != `{"city":"hz"}` {
		t.Errorf("参数分片未续接, 实际 %q", fn["arguments"])
	}
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason 应为 tool_calls, 实际 %q", resp.Choices[0].FinishReason)
	}
}
