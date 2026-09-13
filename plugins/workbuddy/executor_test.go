package main

import (
	"encoding/json"
	"regexp"
	"testing"
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

	headers := buildChatHeaders(&profile, cred)

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

func TestQuotaRequestPayloadMatchesDesktopClient(t *testing.T) {
	raw, err := json.Marshal(quotaRequestPayload{
		OnlyValidPeriod: "True",
		PageNumber:      "1",
		PageSize:        "100",
		ProductCode:     "p_tcaca",
		Status:          "[0, 3]",
	})
	if err != nil {
		t.Fatalf("marshal quota payload: %v", err)
	}

	// 客户端全部用字符串发送, 且不发送任何时间范围过滤; 加回数字或时间过滤会改变服务端筛选结果。
	want := `{"OnlyValidPeriod":"True","PageNumber":"1","PageSize":"100","ProductCode":"p_tcaca","Status":"[0, 3]"}`
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
	desktopBody, err := prepareChatRequestBody(reqPayload, manifest, &desktopProf, true)
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
	cliBody, err := prepareChatRequestBody(reqPayload, manifest, &cliProf, true)
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
