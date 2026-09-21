package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestBuildChatRequestBody(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}

	trueVal := true
	cfg := &PluginConfig{
		Enabled:           true,
		EnableModelPrefix: &trueVal,
		ModelPrefix:       "qwenworkcn",
	}

	rawReq := `{
		"model": "qwenworkcn/pro",
		"messages": [
			{"role": "user", "content": "hello world"},
			{
				"role": "assistant",
				"content": "",
				"tool_calls": [
					{
						"id": "call_1",
						"type": "function",
						"function": {"name": "get_time", "arguments": "{\"zone\":\"UTC\"}"}
					}
				]
			}
		],
		"tools": [
			{
				"type": "function",
				"function": {"name": "get_time", "description": "Get current time"}
			}
		],
		"reasoning_effort": "medium"
	}`

	sessionID := "11111111-2222-3333-4444-555555555555"
	modelID, bodyBytes, err := buildChatRequestBody(cfg, []byte(rawReq), m, sessionID)
	if err != nil {
		t.Fatalf("buildChatRequestBody: %v", err)
	}

	// 1. 验证剥除前缀后的裸 modelID
	if modelID != "pro" {
		t.Fatalf("modelID = %q, want pro", modelID)
	}

	var env map[string]any
	if err := json.Unmarshal(bodyBytes, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	// 2. 验证 Cosy 信封固定字段
	if env["chat_task"] != "FREE_INPUT" {
		t.Fatalf("chat_task = %v, want FREE_INPUT", env["chat_task"])
	}
	if env["session_id"] != sessionID || env["request_set_id"] != sessionID {
		t.Fatalf("session_id = %v, want %s", env["session_id"], sessionID)
	}
	if env["session_type"] != "qoder_work" {
		t.Fatalf("session_type = %v, want qoder_work", env["session_type"])
	}

	// 3. 验证 parameters (决策 5: context_length 恒写 1000000)
	params, ok := env["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters missing or not map: %v", env["parameters"])
	}
	if params["context_length"] != float64(1000000) {
		t.Fatalf("parameters.context_length = %v, want 1000000", params["context_length"])
	}
	if params["reasoning_effort"] != "medium" {
		t.Fatalf("parameters.reasoning_effort = %v, want medium", params["reasoning_effort"])
	}
	if params["tool_choice"] != "auto" {
		t.Fatalf("parameters.tool_choice = %v, want auto", params["tool_choice"])
	}

	// 4. 验证 user content 双写 (withUserContentsDualWrite)
	msgs, ok := env["messages"].([]any)
	if !ok || len(msgs) < 2 {
		t.Fatalf("messages invalid: %v", env["messages"])
	}
	firstMsg := msgs[0].(map[string]any)
	if firstMsg["role"] != "user" || firstMsg["content"] != "hello world" {
		t.Fatalf("first msg: %v", firstMsg)
	}
	contents, ok := firstMsg["contents"].([]any)
	if !ok || len(contents) != 1 {
		t.Fatalf("first msg contents missing or not length 1: %v", firstMsg["contents"])
	}
	c0 := contents[0].(map[string]any)
	if c0["type"] != "text" || c0["text"] != "hello world" {
		t.Fatalf("contents[0] = %v, want type:text, text:hello world", c0)
	}

	// 5. 验证 assistant 缺少 tool result 时补齐合成 tool 响应
	if len(msgs) != 3 {
		t.Fatalf("messages count = %d, want 3 (user + assistant + synthetic tool)", len(msgs))
	}
	synthMsg := msgs[2].(map[string]any)
	if synthMsg["role"] != "tool" || synthMsg["tool_call_id"] != "call_1" {
		t.Fatalf("synthetic msg invalid: %v", synthMsg)
	}

	// 6. 验证 model_config
	mc, ok := env["model_config"].(map[string]any)
	if !ok {
		t.Fatalf("model_config missing: %v", env["model_config"])
	}
	if mc["key"] != "pro" || mc["source"] != "system" {
		t.Fatalf("model_config = %v", mc)
	}

	// 7. business 必须带 product/type: 上游按它解析模型目录, 缺失即 503 Model catalog unavailable
	biz, ok := env["business"].(map[string]any)
	if !ok {
		t.Fatalf("business missing: %v", env["business"])
	}
	if biz["product"] != "qoder_work" || biz["type"] != "agent" {
		t.Fatalf("business = %v, want product=qoder_work type=agent", biz)
	}
}

func TestUnwrapSSEFrame(t *testing.T) {
	// 1. [DONE]
	r1 := unwrapSSEFrame("[DONE]")
	if !r1.IsDone {
		t.Fatalf("[DONE] should be IsDone: %+v", r1)
	}

	// 2. [NOTIFICATIONS]
	r2 := unwrapSSEFrame("[NOTIFICATIONS] ping")
	if !r2.Skip {
		t.Fatalf("[NOTIFICATIONS] should be Skip: %+v", r2)
	}

	// 3. 形态 A 正常 chunk
	frameA := `{"body":"{\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}","statusCodeValue":200}`
	r3 := unwrapSSEFrame(frameA)
	if r3.RawChunk != `{"choices":[{"delta":{"content":"hello"}}]}` {
		t.Fatalf("Form A chunk = %q", r3.RawChunk)
	}

	// 4. 形态 A [DONE]
	frameADone := `{"body":"[DONE]","statusCodeValue":200}`
	r4 := unwrapSSEFrame(frameADone)
	if !r4.IsDone {
		t.Fatalf("Form A [DONE] should be IsDone: %+v", r4)
	}

	// 5. 形态 A 网关错误 (HTTP 400)
	frameAErr := `{"body":"INVALID_REQUEST","statusCodeValue":400}`
	r5 := unwrapSSEFrame(frameAErr)
	if !strings.Contains(r5.ErrorMsg, "400") || !strings.Contains(r5.ErrorMsg, "INVALID_REQUEST") {
		t.Fatalf("Form A error = %q", r5.ErrorMsg)
	}

	// 6. 形态 B 直出 chunk
	frameB := `{"id":"c1","choices":[{"delta":{"content":"direct"}}]}`
	r6 := unwrapSSEFrame(frameB)
	if r6.RawChunk != frameB {
		t.Fatalf("Form B raw = %q", r6.RawChunk)
	}

	// 7. 业务错误 code != 0
	frameBizErr := `{"code":11002,"message":"rate limited"}`
	r7 := unwrapSSEFrame(frameBizErr)
	if !strings.Contains(r7.ErrorMsg, "11002") {
		t.Fatalf("business error = %q", r7.ErrorMsg)
	}

	// 8. 尾帧 (只有 duration 等，无 choices 和 usage)
	frameTail := `{"duration":123}`
	r8 := unwrapSSEFrame(frameTail)
	if !r8.Skip {
		t.Fatalf("tail frame without choices should be Skip: %+v", r8)
	}
}

func TestAggregateChatStream(t *testing.T) {
	// 模拟流式输出：既有文本片段、又有思维链片段、还有工具调用分片合并
	streamData := `data: {"body":"{\"id\":\"chat-1\",\"choices\":[{\"delta\":{\"content\":\"Hello \",\"reasoning_content\":\"thinking...\"}}]}","statusCodeValue":200}
data: {"body":"{\"choices\":[{\"delta\":{\"content\":\"world!\",\"tool_calls\":[{\"index\":0,\"id\":\"call_abc\",\"type\":\"function\",\"function\":{\"name\":\"tool_1\",\"arguments\":\"{\\\"k\\\":\"}}]}}]}","statusCodeValue":200}
data: {"body":"{\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"v\\\"}\"}}]}}]}","statusCodeValue":200}
data: {"body":"{\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":20}}","statusCodeValue":200}
data: [DONE]
`

	aggregated, err := aggregateChatStream([]byte(streamData))
	if err != nil {
		t.Fatalf("aggregateChatStream: %v", err)
	}

	var resp chatCompletionResponse
	if err := json.Unmarshal(aggregated, &resp); err != nil {
		t.Fatalf("unmarshal aggregated response: %v", err)
	}

	if resp.ID != "chat-1" {
		t.Fatalf("ID = %q, want chat-1", resp.ID)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("Choices length = %d, want 1", len(resp.Choices))
	}
	choice := resp.Choices[0]
	if choice.Message.Content != "Hello world!" {
		t.Fatalf("Content = %q, want Hello world!", choice.Message.Content)
	}
	if choice.Message.ReasoningContent != "thinking..." {
		t.Fatalf("ReasoningContent = %q, want thinking...", choice.Message.ReasoningContent)
	}
	if choice.FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q, want tool_calls", choice.FinishReason)
	}
	if len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("ToolCalls length = %d, want 1", len(choice.Message.ToolCalls))
	}
	tc := choice.Message.ToolCalls[0]
	if tc.ID != "call_abc" || tc.Function.Name != "tool_1" || tc.Function.Arguments != `{"k":"v"}` {
		t.Fatalf("ToolCall = %+v, want merged arguments {\"k\":\"v\"}", tc)
	}
}

func TestHandleCountTokens(t *testing.T) {
	resp, err := handleCountTokens(pluginapi.ExecutorRequest{
		Payload: []byte("12345678"),
	})
	if err != nil {
		t.Fatalf("handleCountTokens: %v", err)
	}
	if string(resp.Payload) != `{"total_tokens":2,"input_tokens":2}` {
		t.Fatalf("countTokens payload = %s", string(resp.Payload))
	}
}

// 网关包装帧里的业务错误必须在内层认出来, 否则错误 JSON 会被当成 chunk 下发给客户端。
func TestUnwrapSSEFrameFormABusinessError(t *testing.T) {
	frame := `{"body":"{\"code\":11002,\"message\":\"rate limited\"}","statusCodeValue":200}`
	got := unwrapSSEFrame(frame)
	if got.ErrorMsg == "" {
		t.Fatalf("形态 A 内层业务错误未被识别, 结果: %+v", got)
	}
	if !strings.Contains(got.ErrorMsg, "11002") {
		t.Fatalf("ErrorMsg = %q, want code 11002", got.ErrorMsg)
	}
}

// 无参函数的 arguments 是空串, 丢掉它会让同一轮的工具结果一起消失。
func TestCleanMessagesKeepsEmptyArgumentToolCall(t *testing.T) {
	call := toolCall{ID: "call_1", Type: "function"}
	call.Function.Name = "get_time"
	call.Function.Arguments = ""

	cleaned := cleanMessages([]chatMessage{
		{Role: "assistant", ToolCalls: []toolCall{call}},
		{Role: "tool", ToolCallID: "call_1", Content: "12:00"},
	}, false)

	if len(cleaned) != 2 {
		t.Fatalf("messages = %d (%+v), want 2: 空 arguments 的工具调用与结果都被丢了", len(cleaned), cleaned)
	}
	if len(cleaned[0].ToolCalls) != 1 || cleaned[0].ToolCalls[0].Function.Name != "get_time" {
		t.Fatalf("assistant tool_calls = %+v, want get_time", cleaned[0].ToolCalls)
	}
	if content, _ := cleaned[1].Content.(string); content != "12:00" {
		t.Fatalf("tool 结果 = %v, want 12:00", cleaned[1].Content)
	}
}

// TestReasoningEffortPassthrough 逐档断言: 客户端给的档位原样写进上游 parameters。
//
// 档位透传是确定性契约, 钉在这里; 真机 `-scenarios effort` 比的是上游行为 (深度中位数),
// 受上游噪声影响, 不承担这条判据 (见 .agents/skills/verify-cpa-plugin/features/chat.md)。
func TestReasoningEffortPassthrough(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}
	trueVal := true
	cfg := &PluginConfig{Enabled: true, EnableModelPrefix: &trueVal, ModelPrefix: "qwenworkcn"}
	const sessionID = "11111111-2222-3333-4444-555555555555"

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
					`{"model":"qwenworkcn/%s","messages":[{"role":"user","content":"hi"}],"reasoning_effort":%q}`,
					model.ID, want)
				_, body, err := buildChatRequestBody(cfg, []byte(raw), m, sessionID)
				if err != nil {
					t.Fatalf("buildChatRequestBody: %v", err)
				}
				var envBody map[string]any
				if err := json.Unmarshal(body, &envBody); err != nil {
					t.Fatalf("unmarshal envelope: %v", err)
				}
				params, _ := envBody["parameters"].(map[string]any)
				got, _ := params["reasoning_effort"].(string)
				expect := want
				if expect == "" {
					expect = model.DefaultReasoningEffort
				}
				if got != expect {
					t.Fatalf("上游 parameters.reasoning_effort = %q, 期望 %q", got, expect)
				}
			})
			tested++
		}
	}
	if tested == 0 {
		t.Fatal("没有任何模型被覆盖, 用例失去意义")
	}
}

// 客户端点名函数或断言 none 时必须原样上行, 未指定时保持官方客户端基准 auto。
func TestToolChoicePassthrough(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}
	trueVal := true
	cfg := &PluginConfig{Enabled: true, EnableModelPrefix: &trueVal, ModelPrefix: "qwenworkcn"}
	const sessionID = "11111111-2222-3333-4444-555555555555"
	const tools = `[{"type":"function","function":{"name":"get_time"}},{"type":"function","function":{"name":"get_weather"}}]`

	cases := []struct {
		name        string
		raw         string
		wantChoice  any
		wantPresent bool
	}{
		{
			name:        "点名函数",
			raw:         `{"model":"qwenworkcn/pro","messages":[{"role":"user","content":"hi"}],"tools":` + tools + `,"tool_choice":{"type":"function","function":{"name":"get_weather"}}}`,
			wantChoice:  map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}},
			wantPresent: true,
		},
		{
			name:        "禁止调用",
			raw:         `{"model":"qwenworkcn/pro","messages":[{"role":"user","content":"hi"}],"tools":` + tools + `,"tool_choice":"none"}`,
			wantChoice:  "none",
			wantPresent: true,
		},
		{
			name:        "未指定回落 auto",
			raw:         `{"model":"qwenworkcn/pro","messages":[{"role":"user","content":"hi"}],"tools":` + tools + `}`,
			wantChoice:  "auto",
			wantPresent: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, body, err := buildChatRequestBody(cfg, []byte(tc.raw), m, sessionID)
			if err != nil {
				t.Fatalf("buildChatRequestBody: %v", err)
			}
			var envBody map[string]any
			if err := json.Unmarshal(body, &envBody); err != nil {
				t.Fatalf("unmarshal envelope: %v", err)
			}
			params, _ := envBody["parameters"].(map[string]any)
			got := params["tool_choice"]
			if tc.wantPresent && got == nil {
				t.Fatal("上游 parameters 缺少 tool_choice")
			}
			if !reflect.DeepEqual(got, tc.wantChoice) {
				t.Fatalf("上游 parameters.tool_choice = %#v, 期望 %#v", got, tc.wantChoice)
			}
		})
	}

	t.Run("并行开关透传", func(t *testing.T) {
		raw := `{"model":"qwenworkcn/pro","messages":[{"role":"user","content":"hi"}],"tools":` + tools + `,"parallel_tool_calls":false}`
		_, body, err := buildChatRequestBody(cfg, []byte(raw), m, sessionID)
		if err != nil {
			t.Fatalf("buildChatRequestBody: %v", err)
		}
		var envBody map[string]any
		if err := json.Unmarshal(body, &envBody); err != nil {
			t.Fatalf("unmarshal envelope: %v", err)
		}
		params, _ := envBody["parameters"].(map[string]any)
		if got, ok := params["parallel_tool_calls"]; !ok || got != false {
			t.Fatalf("上游 parameters.parallel_tool_calls = %#v, 期望 false", got)
		}
	})
}
