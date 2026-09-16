package main

import (
	"testing"
)

func TestParseManifestOK(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if m.SchemaVersion != 2 {
		t.Fatalf("schemaVersion = %d, want 2", m.SchemaVersion)
	}
	if m.Endpoints.BaseURL != "https://gateway.qwenwork.cn" {
		t.Fatalf("baseUrl = %q", m.Endpoints.BaseURL)
	}
	if len(m.Models) != 3 {
		t.Fatalf("models = %d, want 3", len(m.Models))
	}
	// 版本跟随数据刷新 (app-values --update → export-cpa-static), 断言数据驱动:
	// profile.cosyVersion 与 chat 组渲染结果一致即可, 不钉具体版本号
	if m.Profile.CosyVersion == "" {
		t.Fatal("cosyVersion 为空")
	}

	// 已知值渲染: cosyVersion 已替换, 运行期变量保留
	chat, err := m.HeaderGroup("chat")
	if err != nil {
		t.Fatalf("chat group: %v", err)
	}
	if chat["Cosy-Version"] != m.Profile.CosyVersion {
		t.Fatalf("chat Cosy-Version = %q, want rendered %s", chat["Cosy-Version"], m.Profile.CosyVersion)
	}
	if chat["X-Model-Key"] != "{{modelKey}}" {
		t.Fatalf("chat X-Model-Key = %q, want runtime var kept", chat["X-Model-Key"])
	}
}

func TestParseManifestMissingField(t *testing.T) {
	cases := []struct {
		name    string
		mutator func(map[string]any)
		wantErr string
	}{
		{"schemaVersion", func(m map[string]any) { m["schemaVersion"] = 3 }, "schemaVersion"},
		{"baseUrl", func(m map[string]any) {
			m["endpoints"].(map[string]any)["baseUrl"] = ""
		}, "baseUrl"},
		{"chatCompletions", func(m map[string]any) {
			m["endpoints"].(map[string]any)["chatCompletions"] = ""
		}, "chatCompletions"},
		{"clientId", func(m map[string]any) {
			m["profile"].(map[string]any)["clientId"] = ""
		}, "clientId"},
		{"cosyVersion", func(m map[string]any) {
			m["profile"].(map[string]any)["cosyVersion"] = ""
		}, "cosyVersion"},
		{"headers group", func(m map[string]any) {
			delete(m["profile"].(map[string]any)["headers"].(map[string]any), "chat")
		}, "missing group"},
		{"models empty", func(m map[string]any) { m["models"] = []any{} }, "models"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw map[string]any
			if err := unmarshalJSON(defaultStaticConfigBytes, &raw); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			tc.mutator(raw)
			data := mustMarshalJSON(t, raw)
			if _, err := parseManifest(data); err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			} else if !contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestParseManifestUnknownTemplateVar(t *testing.T) {
	var raw map[string]any
	if err := unmarshalJSON(defaultStaticConfigBytes, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	headers := raw["profile"].(map[string]any)["headers"].(map[string]any)
	headers["base"].(map[string]any)["User-Agent"] = "{{unknownVar}}"

	if _, err := parseManifest(mustMarshalJSON(t, raw)); err == nil {
		t.Fatal("expected unknown template var error, got nil")
	}
}

func TestChatURL(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	want := "https://gateway.qwenwork.cn/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"
	if got := m.ChatURL(); got != want {
		t.Fatalf("ChatURL = %q, want %q", got, want)
	}
}

func TestWebURL(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if got, want := m.WebURL("/user/wallets"), "https://qwenwork.cn/user/wallets"; got != want {
		t.Fatalf("WebURL = %q, want %q", got, want)
	}
}

func TestRenderHeaderGroup(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}

	// 1. 成功渲染 openApi 头
	openApi, err := m.RenderHeaderGroup("openApi", map[string]string{
		"deviceToken": "mock-token-123",
	})
	if err != nil {
		t.Fatalf("RenderHeaderGroup openApi: %v", err)
	}
	if got := openApi["Authorization"]; got != "Bearer mock-token-123" {
		t.Fatalf("Authorization = %q, want Bearer mock-token-123", got)
	}

	// 2. 成功渲染 chat 动态头
	chat, err := m.RenderHeaderGroup("chat", map[string]string{
		"modelKey":    "pro",
		"modelSource": "system",
	})
	if err != nil {
		t.Fatalf("RenderHeaderGroup chat: %v", err)
	}
	if chat["X-Model-Key"] != "pro" {
		t.Fatalf("X-Model-Key = %q, want pro", chat["X-Model-Key"])
	}
	if chat["X-Model-Source"] != "system" {
		t.Fatalf("X-Model-Source = %q, want system", chat["X-Model-Source"])
	}

	// 3. 遗漏变量 fail-fast
	if _, err := m.RenderHeaderGroup("openApi", map[string]string{}); err == nil {
		t.Fatal("expected missing variable error, got nil")
	}

	// 4. 不存在的头组 fail-fast
	if _, err := m.RenderHeaderGroup("nonexistent", nil); err == nil {
		t.Fatal("expected nonexistent group error, got nil")
	}
}
