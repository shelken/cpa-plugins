package main

import (
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
