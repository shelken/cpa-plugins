package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
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
	manifest, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("解析内嵌清单失败: %v", err)
	}
	profile, err := manifest.GetProfile(string(ProfileDesktop))
	if err != nil {
		t.Fatalf("取 desktop 档失败: %v", err)
	}
	cred := &Credential{UserID: "user-1", Credentials: TokenCredentials{Access: "token-1"}}

	headers, err := buildChatHeaders(manifest, profile, cred, "session-1")
	if err != nil {
		t.Fatalf("渲染 chat 头失败: %v", err)
	}

	if got := headers.Get("Authorization"); got != "Bearer token-1" {
		t.Errorf("authorization = %q, want Bearer token-1", got)
	}
	if got := headers.Get("X-Agent-Purpose"); got != "conversation" {
		t.Errorf("清单头部未透传, X-Agent-Purpose = %q", got)
	}
	if got := headers.Get("X-Agent-Type"); got != "main" {
		t.Errorf("X-Agent-Type = %q, want main (官方主对话必发)", got)
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

	// 请求 id 与消息 id 同值由引擎两阶段渲染保证
	if msgID, reqID := headers.Get("X-Conversation-Message-ID"), headers.Get("X-Request-ID"); msgID == "" || msgID != reqID {
		t.Errorf("消息 id 应与请求 id 相同, 实际 %q / %q", msgID, reqID)
	}

	// 会话请求/追踪 id 为 32 位无横线十六进制; 会话 id 与连接 id 为带横线 UUID
	for _, name := range []string{"X-Conversation-Message-ID", "X-Request-ID", "X-Conversation-Request-ID", "X-Trace-ID"} {
		if !reHex32.MatchString(headers.Get(name)) {
			t.Errorf("%s 应为 32 位十六进制, 实际 %q", name, headers.Get(name))
		}
	}
	for _, name := range []string{"X-Conversation-ID", "Acp-Connection-Id"} {
		if !reUUID.MatchString(headers.Get(name)) {
			t.Errorf("%s 应为带横线 UUID, 实际 %q", name, headers.Get(name))
		}
	}
	// 官方 10-21 抓包已不再发送 X-Root-Request-ID, 清单应对齐移除
	if got := headers.Get("X-Root-Request-ID"); got != "" {
		t.Errorf("X-Root-Request-ID 已从官方协议移除, 不应再发, 实际 %q", got)
	}
}

func TestChatHeadersKeepConversationIdentityStable(t *testing.T) {
	manifest, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("解析内嵌清单失败: %v", err)
	}
	profile, err := manifest.GetProfile(string(ProfileDesktop))
	if err != nil {
		t.Fatalf("取 desktop 档失败: %v", err)
	}
	cred := &Credential{UserID: "user-1", Credentials: TokenCredentials{Access: "token-1"}}

	first, err := buildChatHeaders(manifest, profile, cred, "session-1")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	second, err := buildChatHeaders(manifest, profile, cred, "session-1")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	other, err := buildChatHeaders(manifest, profile, cred, "session-2")
	if err != nil {
		t.Fatalf("render: %v", err)
	}

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
	manifest, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("解析内嵌清单失败: %v", err)
	}
	profile, err := manifest.GetProfile(string(ProfileDesktop))
	if err != nil {
		t.Fatalf("取 desktop 档失败: %v", err)
	}
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

	mustHeaders := func(seed string) http.Header {
		h, err := buildChatHeaders(manifest, profile, cred, seed)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		return h
	}
	hostFirst := mustHeaders(sessionSeed(firstTurn, firstTurn.OriginalRequest))
	hostThird := mustHeaders(sessionSeed(thirdTurn, thirdTurn.OriginalRequest))
	fallback := mustHeaders(sessionSeed(noMetadata, noMetadata.OriginalRequest))

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

// TestReasoningEffortPassthrough 逐档断言: 客户端给的档位原样写进上游请求。
//
// 档位透传是确定性契约, 钉在这里; 真机 `-scenarios effort` 比的是上游行为 (深度中位数),
// 受上游噪声影响, 不承担这条判据 (见 .agents/skills/verify-cpa-plugin/features/chat.md)。
func TestReasoningEffortPassthrough(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}
	cfg := mustParseConfig(t, nil)

	tested := 0
	for _, model := range m.Models {
		if !model.SupportsReasoning {
			continue
		}
		efforts := append([]string(nil), model.SupportedEfforts...)
		if len(efforts) == 0 && model.DefaultReasoningEffort != "" {
			efforts = []string{model.DefaultReasoningEffort}
		}
		if len(efforts) == 0 {
			t.Fatalf("模型 %s 声明了 reasoning 却没有任何档位, 契约无从验证", model.ID)
		}
		for _, want := range append([]string{""}, efforts...) {
			label := want
			if label == "" {
				label = "(缺省)"
			}
			t.Run(model.ID+"/"+label, func(t *testing.T) {
				raw := fmt.Sprintf(
					`{"model":"workbuddy/%s","messages":[{"role":"user","content":"hi"}],"reasoning_effort":%q}`,
					model.ID, want)
				body, err := prepareChatRequestBody(cfg, []byte(raw), m, &ProfileConfig{}, true)
				if err != nil {
					t.Fatalf("prepareChatRequestBody: %v", err)
				}
				var out map[string]any
				if err := json.Unmarshal(body, &out); err != nil {
					t.Fatalf("unmarshal body: %v", err)
				}
				got, _ := out["reasoning_effort"].(string)
				expect := want
				if expect == "" {
					expect = model.DefaultReasoningEffort
				}
				if got != expect {
					t.Fatalf("上游 reasoning_effort = %q, 期望 %q", got, expect)
				}
			})
			tested++
		}
	}
	if tested == 0 {
		t.Fatal("没有任何模型被覆盖, 用例失去意义")
	}
}

func TestUserMessageFieldsInjected(t *testing.T) {
	manifest, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("解析内嵌清单失败: %v", err)
	}
	profile, err := manifest.GetProfile(string(ProfileDesktop))
	if err != nil {
		t.Fatalf("取 desktop 档失败: %v", err)
	}
	if len(profile.Body.UserMessageFields) == 0 {
		t.Fatal("desktop 档清单缺少 body.userMessageFields, 注入路径失去意义")
	}

	cfg := mustParseConfig(t, nil)
	raw := `{"model":"workbuddy/hy3","messages":[` +
		`{"role":"system","content":"sys"},` +
		`{"role":"user","content":"第一轮"},` +
		`{"role":"assistant","content":"答"},` +
		`{"role":"user","content":"第二轮"}]}`
	body, err := prepareChatRequestBody(cfg, []byte(raw), manifest, profile, true)
	if err != nil {
		t.Fatalf("prepareChatRequestBody: %v", err)
	}

	var out struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}

	// 官方行为: 每条 user 消息都带簿记; assistant/system 不带
	userCount := 0
	for _, msg := range out.Messages {
		role, _ := msg["role"].(string)
		if role != "user" {
			if _, has := msg["agent"]; has {
				t.Errorf("%s 消息不应注入 agent 字段", role)
			}
			continue
		}
		userCount++
		if msg["agent"] != "cli" {
			t.Errorf("user 消息 agent = %v, want cli", msg["agent"])
		}
		if msg["startsNewUserRequest"] != true {
			t.Errorf("user 消息 startsNewUserRequest = %v, want true", msg["startsNewUserRequest"])
		}
		convReqID, _ := msg["conversationRequestId"].(string)
		if !reHex32.MatchString(convReqID) {
			t.Errorf("user 消息 conversationRequestId 应为 32 位十六进制, 实际 %q", convReqID)
		}
	}
	if userCount != 2 {
		t.Fatalf("期望 2 条 user 消息, 实际 %d", userCount)
	}

	// 会话请求 id 在同一次请求的两条 user 消息上同值(与头组共享同一 {{conversationRequestId}})
	first, _ := out.Messages[1]["conversationRequestId"].(string)
	second, _ := out.Messages[3]["conversationRequestId"].(string)
	if first == "" || first != second {
		t.Errorf("同请求内 conversationRequestId 应同值, 实际 %q / %q", first, second)
	}
}

func TestUnknownPlaceholderFailsFast(t *testing.T) {
	manifest, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("解析内嵌清单失败: %v", err)
	}
	profile := manifest.Profiles[string(ProfileDesktop)]
	profile.Headers = map[string]map[string]string{
		"chat": {"X-Bogus": "{{noSuchValue}}"},
	}
	cred := &Credential{UserID: "u", Credentials: TokenCredentials{Access: "t"}}

	if _, err := buildChatHeaders(manifest, &profile, cred, "seed"); err == nil {
		t.Error("未知占位符应报错, 实际静默通过")
	}
}

func TestHeaderAndBodyConversationRequestIDMatch(t *testing.T) {
	manifest, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("解析内嵌清单失败: %v", err)
	}
	profile, err := manifest.GetProfile(string(ProfileDesktop))
	if err != nil {
		t.Fatalf("取 desktop 档失败: %v", err)
	}

	cfg := mustParseConfig(t, nil)
	raw := `{"model":"workbuddy/hy3","messages":[{"role":"user","content":"hello"}]}`
	seed := "test-session-seed-12345"
	cred := &Credential{UserID: "user-1", Credentials: TokenCredentials{Access: "token-1"}}
	vars := manifestTemplateVars(manifest)

	values, err := resolveValues(manifest.Values, seed, cred, vars)
	if err != nil {
		t.Fatalf("resolveValues: %v", err)
	}

	body, err := prepareChatRequestBody(cfg, []byte(raw), manifest, profile, true, values)
	if err != nil {
		t.Fatalf("prepareChatRequestBody: %v", err)
	}
	headers, err := renderHeaders(profile.Headers["chat"], values)
	if err != nil {
		t.Fatalf("renderHeaders: %v", err)
	}

	var out struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}

	bodyConvID, _ := out.Messages[0]["conversationRequestId"].(string)
	headerConvID := headers.Get("X-Conversation-Request-ID")

	if bodyConvID == "" || headerConvID == "" {
		t.Fatalf("ID 不应为空: body=%q header=%q", bodyConvID, headerConvID)
	}
	if bodyConvID != headerConvID {
		t.Errorf("body 的 conversationRequestId (%s) 应与请求头 X-Conversation-Request-ID (%s) 完全一致",
			bodyConvID, headerConvID)
	}
}

func TestUnclosedPlaceholderFailsFast(t *testing.T) {
	manifest, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("解析内嵌清单失败: %v", err)
	}
	profile := manifest.Profiles[string(ProfileDesktop)]
	profile.Headers = map[string]map[string]string{
		"chat": {"Authorization": "Bearer {{accessToken"},
	}
	cred := &Credential{UserID: "u", Credentials: TokenCredentials{Access: "t"}}

	if _, err := buildChatHeaders(manifest, &profile, cred, "seed"); err == nil {
		t.Error("未闭合占位符应报错, 实际静默通过")
	}
}

func TestUnknownDeriveTypeFailsFast(t *testing.T) {
	manifest, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("解析内嵌清单失败: %v", err)
	}
	manifest.Values["invalid"] = ValueDecl{
		Derive: "unknown-kind",
		Salt:   "salt",
		Scope:  "session",
	}
	cred := &Credential{UserID: "u", Credentials: TokenCredentials{Access: "t"}}
	vars := manifestTemplateVars(manifest)

	if _, err := resolveValues(manifest.Values, "seed", cred, vars); err == nil {
		t.Error("未知的 derive 类型应报错, 实际静默通过")
	}
}

func TestConcurrentResolveValuesSafe(t *testing.T) {
	manifest, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("解析内嵌清单失败: %v", err)
	}
	cred := &Credential{UserID: "u", Credentials: TokenCredentials{Access: "t"}}
	vars := manifestTemplateVars(manifest)
	const workers = 50
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			vals, err := resolveValues(manifest.Values, "same-seed", cred, vars)
			if err != nil {
				t.Errorf("resolveValues in worker: %v", err)
				return
			}
			if vals["conversationRequestId"] == "" {
				t.Errorf("conversationRequestId is empty")
			}
		}()
	}
	wg.Wait()
}

// 宿主 HTTP 流回调替身: 按 method 分发, 记录插件发出的回调供断言。
type fakeHostHTTP struct {
	mu           sync.Mutex
	doStreamReq  hostHTTPRequest
	statusCode   int // 0 视为 200
	sseChunks    []string
	streamOpen   bool
	streamClosed bool
	readErr      error
	reads        int
	emitErr      error
	emitted      []string
	closeErr     string
	// streamDoneCh 在下游 stream_close 回调被处理后关闭。插件保证它是流 goroutine
	// 的最后一个宿主回调, 测试据此等待 goroutine 退出, 避免与 cleanup 恢复 callHost 竞争。
	streamDoneCh chan struct{}
}

func (f *fakeHostHTTP) install(t *testing.T) {
	t.Helper()
	previous := callHost
	t.Cleanup(func() { callHost = previous })
	f.streamDoneCh = make(chan struct{})
	streamID := "test-stream-1"
	callHost = func(method string, payload any) (json.RawMessage, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch method {
		case pluginabi.MethodHostHTTPDoStream:
			f.doStreamReq = payload.(hostHTTPRequest)
			f.streamOpen = true
			status := f.statusCode
			if status == 0 {
				status = 200
			}
			return json.Marshal(hostHTTPStreamResponse{StatusCode: status, StreamID: streamID})
		case pluginabi.MethodHostHTTPStreamRead:
			if !f.streamOpen {
				return nil, fmt.Errorf("host http stream %q is not open", streamID)
			}
			f.reads++
			if len(f.sseChunks) > 0 {
				head := f.sseChunks[0]
				f.sseChunks = f.sseChunks[1:]
				return json.Marshal(hostHTTPStreamReadResponse{Payload: []byte(head)})
			}
			if f.readErr != nil {
				return json.Marshal(hostHTTPStreamReadResponse{Error: f.readErr.Error(), Done: true})
			}
			return json.Marshal(hostHTTPStreamReadResponse{Done: true})
		case pluginabi.MethodHostHTTPStreamClose:
			f.streamClosed = true
			f.streamOpen = false
			return json.Marshal(map[string]any{})
		case pluginabi.MethodHostStreamClose:
			f.closeErr = payload.(rpcStreamCloseRequest).Error
			close(f.streamDoneCh)
			return json.Marshal(map[string]any{})
		case pluginabi.MethodHostStreamEmit:
			f.emitted = append(f.emitted, string(payload.(rpcStreamEmitRequest).Payload))
			if f.emitErr != nil {
				return nil, f.emitErr
			}
			return json.Marshal(map[string]any{})
		default:
			return nil, fmt.Errorf("fake host: unexpected callback %s", method)
		}
	}
}

func (f *fakeHostHTTP) upstreamCallbackID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.doStreamReq.HostCallbackID
}

// waitStreamDone 等待流 goroutine 处理完下游关闭回调, 之后读替身字段不再有并发写。
func (f *fakeHostHTTP) waitStreamDone(t *testing.T) {
	t.Helper()
	select {
	case <-f.streamDoneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("流 goroutine 2 秒内未处理下游关闭回调")
	}
}

// runStream 用替身驱动一次流式执行, 等 goroutine 退出后返回。
func runStream(t *testing.T, fake *fakeHostHTTP) {
	t.Helper()
	manifest, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("解析内嵌清单失败: %v", err)
	}
	fake.install(t)
	raw := `{"model":"workbuddy/hy3","messages":[{"role":"user","content":"hi"}]}`
	if _, err := handleExecuteStream(manifest, mustParseConfig(t, nil), pluginapi.ExecutorRequest{
		Payload:         []byte(raw),
		OriginalRequest: []byte(raw),
		StorageJSON:     []byte(`{"userId":"u","credentials":{"access":"t"}}`),
	}, "downstream-1", "callback-1"); err != nil {
		t.Fatalf("handleExecuteStream: %v", err)
	}
	fake.waitStreamDone(t)
}

// 上游最后一条帧没带换行就 EOF 时, 必须照常下发, 不能静默丢弃。
func TestExecuteStreamFlushesFinalLineWithoutNewline(t *testing.T) {
	fake := &fakeHostHTTP{sseChunks: []string{"data: {\"a\":1}\n\ndata: {\"b\":2}"}}
	runStream(t, fake)
	if want := []string{`{"a":1}`, `{"b":2}`}; !reflect.DeepEqual(fake.emitted, want) {
		t.Errorf("emitted = %q, want %q", fake.emitted, want)
	}
	if fake.closeErr != "" {
		t.Errorf("正常结束不应带错误关闭, got %q", fake.closeErr)
	}
}

// 非 200 时上游错误体里有限流/鉴权的具体原因, 必须带进错误信息。
func TestExecuteStreamUpstreamErrorKeepsBody(t *testing.T) {
	fake := &fakeHostHTTP{statusCode: 429, sseChunks: []string{`{"error":"rate `, `limited"}`}}
	runStream(t, fake)
	if !strings.Contains(fake.closeErr, "429") || !strings.Contains(fake.closeErr, `{"error":"rate limited"}`) {
		t.Errorf("closeErr = %q, 应含状态码与完整错误体", fake.closeErr)
	}
	if !fake.streamClosed {
		t.Error("读完错误体后应关闭上游流")
	}
}

// 下游 emit 失败说明客户端已不可达, 插件应立即停读上游, 不能继续消耗上游流。
func TestExecuteStreamStopsReadingAfterEmitFailure(t *testing.T) {
	fake := &fakeHostHTTP{
		emitErr:   fmt.Errorf("downstream gone"),
		sseChunks: []string{"data: {\"a\":1}\n\ndata: {\"b\":2}\n\n", "data: {\"c\":3}\n\n"},
	}
	runStream(t, fake)
	if fake.reads != 1 || len(fake.emitted) != 1 {
		t.Errorf("emit 失败后应立即退出: reads=%d emitted=%q", fake.reads, fake.emitted)
	}
	if !fake.streamClosed {
		t.Error("emit 失败退出时应关闭上游流")
	}
}

func TestExecuteStreamForwardsHostCallbackID(t *testing.T) {
	manifest, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("解析内嵌清单失败: %v", err)
	}
	cfg := mustParseConfig(t, nil)
	fake := &fakeHostHTTP{sseChunks: []string{"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n", "data: [DONE]\n\n"}}
	fake.install(t)

	raw := `{"model":"workbuddy/hy3","messages":[{"role":"user","content":"hi"}]}`
	testCred := []byte(`{"userId":"u","credentials":{"access":"t"}}`)
	resp, err := handleExecuteStream(manifest, cfg, pluginapi.ExecutorRequest{
		Payload:         []byte(raw),
		OriginalRequest: []byte(raw),
		StorageJSON:     testCred,
	}, "downstream-1", "callback-42")
	if err != nil {
		t.Fatalf("handleExecuteStream: %v", err)
	}
	if _, hasHeaders := resp["headers"]; !hasHeaders {
		t.Errorf("response should carry SSE headers, got %v", resp)
	}

	fake.waitStreamDone(t)
	if got := fake.upstreamCallbackID(); got != "callback-42" {
		t.Errorf("host.http.do_stream 应携带下游 callback id, 实际 %q", got)
	}
	fake.mu.Lock()
	closed := fake.streamClosed
	fake.mu.Unlock()
	if !closed {
		t.Error("上游流结束后应调用 host.http.stream_close")
	}
}

func TestExecuteStreamDownstreamCancelAbortsUpstreamRead(t *testing.T) {
	manifest, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("解析内嵌清单失败: %v", err)
	}
	cfg := mustParseConfig(t, nil)
	// 首块正常下发, 之后宿主侧因下游 499 使流失效: read 永远报错。
	fake := &fakeHostHTTP{sseChunks: []string{"data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"}}
	fake.readErr = fmt.Errorf("context canceled")
	fake.install(t)

	raw := `{"model":"workbuddy/hy3","messages":[{"role":"user","content":"hi"}]}`
	testCred := []byte(`{"userId":"u","credentials":{"access":"t"}}`)
	if _, err := handleExecuteStream(manifest, cfg, pluginapi.ExecutorRequest{
		Payload:         []byte(raw),
		OriginalRequest: []byte(raw),
		StorageJSON:     testCred,
	}, "downstream-1", "callback-7"); err != nil {
		t.Fatalf("handleExecuteStream: %v", err)
	}

	fake.waitStreamDone(t)
	fake.mu.Lock()
	closed := fake.streamClosed
	fake.mu.Unlock()
	if !closed {
		t.Error("下游取消后插件应通过 read 错误退出并关闭上游流, 而不是无限阻塞")
	}
}

func TestExecuteNonStreamAggregatesViaHostStream(t *testing.T) {
	manifest, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("解析内嵌清单失败: %v", err)
	}
	cfg := mustParseConfig(t, nil)
	upstream := "data: {\"choices\":[{\"delta\":{\"content\":\"ANSWER\"}}]}\n\n" +
		"data: [DONE]\n\n"
	fake := &fakeHostHTTP{sseChunks: []string{upstream}}
	fake.install(t)

	raw := `{"model":"workbuddy/hy3","messages":[{"role":"user","content":"hi"}]}`
	testCred := []byte(`{"userId":"u","credentials":{"access":"t"}}`)
	resp, err := handleExecute(manifest, cfg, pluginapi.ExecutorRequest{
		Payload:         []byte(raw),
		OriginalRequest: []byte(raw),
		StorageJSON:     testCred,
	}, "callback-9")
	if err != nil {
		t.Fatalf("handleExecute: %v", err)
	}
	if !strings.Contains(string(resp.Payload), "ANSWER") {
		t.Errorf("聚合结果应包含上游内容, 实际 %q", string(resp.Payload))
	}
	if got := fake.upstreamCallbackID(); got != "callback-9" {
		t.Errorf("非流式同样应携带 callback id, 实际 %q", got)
	}
}

func TestExecuteNonStreamUpstreamErrorKeepsBody(t *testing.T) {
	manifest, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("解析内嵌清单失败: %v", err)
	}
	fake := &fakeHostHTTP{statusCode: 401, sseChunks: []string{`{"error":"token expired"}`}}
	fake.install(t)
	raw := `{"model":"workbuddy/hy3","messages":[{"role":"user","content":"hi"}]}`
	_, err = handleExecute(manifest, mustParseConfig(t, nil), pluginapi.ExecutorRequest{
		Payload:     []byte(raw),
		StorageJSON: []byte(`{"userId":"u","credentials":{"access":"t"}}`),
	}, "callback-9")
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "token expired") {
		t.Errorf("err = %v, 应含状态码与错误体", err)
	}
}

// 宿主按固定大小分块, 不保证按行。切点落在 data: 前缀后、JSON 中部、[DONE] 中部时,
// 必须拼回完整行再下发, 且 [DONE] 之后的内容不再下发。
func TestExecuteStreamReassemblesSplitSSELine(t *testing.T) {
	frame := `data: {"choices":[{"delta":{"content":"hello"}}]}` + "\n\ndata: [DONE]\n\ndata: {\"late\":1}\n\n"
	split1 := len(`data:`)
	split2 := len(`data: {"choices":[{"del`)
	split3 := strings.Index(frame, "[DO") + 3
	fake := &fakeHostHTTP{sseChunks: []string{frame[:split1], frame[split1:split2], frame[split2:split3], frame[split3:]}}
	runStream(t, fake)
	if want := []string{`{"choices":[{"delta":{"content":"hello"}}]}`}; !reflect.DeepEqual(fake.emitted, want) {
		t.Errorf("emitted = %q, want %q", fake.emitted, want)
	}
}
