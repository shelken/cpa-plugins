package main

import (
	"encoding/json"
	"testing"
)

// 历史 assistant 消息的 reasoning_content 上游不读 (沙箱 codeword 实验),
// 换成官方客户端的 reasoning 字段后模型能读到 (magpie #291 实测)。
func TestHistoryReasoningField(t *testing.T) {
	manifest := &ManifestV2{}

	cleaned := cleanMessages([]chatMessage{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "ok", ReasoningContent: "SECRET-CODEWORD"},
		{Role: "user", Content: "repeat it"},
	}, true, manifest)

	// 找 assistant 消息
	var asst *chatMessage
	for i := range cleaned {
		if cleaned[i].Role == "assistant" {
			asst = &cleaned[i]
			break
		}
	}
	if asst == nil {
		t.Fatal("assistant 消息丢失")
	}

	out := messagesToJSON([]chatMessage{*asst})
	m := out[0].(map[string]any)
	if _, has := m["reasoning_content"]; has {
		t.Errorf("reasoning_content 应被移除")
	}
	if m["reasoning"] != "SECRET-CODEWORD" {
		t.Errorf("reasoning 应承接原值, got %v", m["reasoning"])
	}
}

// 已有 reasoning 字段 (宿主/客户端显式给的) 优先, 不覆盖。
func TestHistoryReasoningFieldExistingWins(t *testing.T) {
	manifest := &ManifestV2{}

	cleaned := cleanMessages([]chatMessage{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "ok", ReasoningContent: "from-history",
			Extra: map[string]any{"reasoning": "explicit"}},
	}, true, manifest)

	var asst *chatMessage
	for i := range cleaned {
		if cleaned[i].Role == "assistant" {
			asst = &cleaned[i]
			break
		}
	}
	if asst == nil {
		t.Fatal("assistant 消息丢失")
	}
	if asst.Extra["reasoning"] != "explicit" {
		t.Errorf("显式 reasoning 应保留, got %v", asst.Extra["reasoning"])
	}
	if asst.ReasoningContent != "" {
		t.Errorf("reasoning_content 仍应被移除 (上游不读)")
	}
}

// user/tool 等非 assistant 消息不动。
func TestHistoryReasoningFieldOnlyAssistant(t *testing.T) {
	manifest := &ManifestV2{}

	cleaned := cleanMessages([]chatMessage{
		{Role: "user", Content: "hi", ReasoningContent: "should-stay"},
	}, true, manifest)

	// normalizeSystemFirst 给无 system 开头的会话注入了占位 system, user 后移一位
	if cleaned[0].Role != "system" {
		t.Fatalf("expected injected system first, got %s", cleaned[0].Role)
	}
	if cleaned[1].ReasoningContent != "should-stay" {
		t.Errorf("非 assistant 消息的 reasoning_content 不应被动, got %q", cleaned[1].ReasoningContent)
	}
}

func TestStripHostReasoningPlaceholder(t *testing.T) {
	manifest, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}
	profile := manifest.Profiles["desktop"]
	trueVal := true
	cfg := &PluginConfig{Enabled: true, EnableModelPrefix: &trueVal, ModelPrefix: "workbuddy"}

	rawReq := `{
		"model": "workbuddy/glm-5.3",
		"messages": [
			{"role": "system", "content": "sys"},
			{"role": "user", "content": "step 1"},
			{
				"role": "assistant",
				"content": "",
				"reasoning_content": "  [reasoning unavailable]  ",
				"tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "ls", "arguments": "{}"}}]
			},
			{"role": "tool", "tool_call_id": "call_1", "content": "ok"},
			{"role": "assistant", "content": "", "reasoning_content": "[reasoning unavailable]"},
			{
				"role": "assistant",
				"content": "",
				"reasoning_content": "real-plan",
				"tool_calls": [{"id": "call_2", "type": "function", "function": {"name": "pwd", "arguments": "{}"}}]
			},
			{"role": "tool", "tool_call_id": "call_2", "content": "/tmp"},
			{"role": "user", "content": "next"}
		]
	}`

	bodyBytes, _, err := prepareChatRequestBody(cfg, []byte(rawReq), manifest, &profile, true)
	if err != nil {
		t.Fatalf("prepareChatRequestBody: %v", err)
	}

	var out struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(bodyBytes, &out); err != nil {
		t.Fatalf("unmarshal out: %v", err)
	}

	// 预期消息序列: system, user, assistant(call_1), tool(call_1), assistant(call_2), tool(call_2), user
	// 单独的合成占位空 assistant 消息应随占位剥离一并丢弃, 不留空壳。
	if len(out.Messages) != 7 {
		t.Fatalf("messages len = %d, want 7; got %v", len(out.Messages), out.Messages)
	}

	call1Asst := out.Messages[2]
	if _, has := call1Asst["reasoning"]; has {
		t.Errorf("call_1 assistant 的占位符不应转为 reasoning 发往上游, got %v", call1Asst["reasoning"])
	}
	if _, has := call1Asst["reasoning_content"]; has {
		t.Errorf("call_1 assistant 不应保留 reasoning_content, got %v", call1Asst["reasoning_content"])
	}

	call2Asst := out.Messages[4]
	if call2Asst["reasoning"] != "real-plan" {
		t.Errorf("真实历史思考应保留在 reasoning, got %v", call2Asst["reasoning"])
	}
}
