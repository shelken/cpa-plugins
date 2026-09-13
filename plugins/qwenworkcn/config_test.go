package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseConfigDefaultPrefix(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("default enabled = false, want true")
	}
	if got := cfg.modelPrefix(); got != "qwenworkcn/" {
		t.Fatalf("modelPrefix = %q, want qwenworkcn/", got)
	}
}

func TestParseConfigPrefixToggle(t *testing.T) {
	off := false
	cfg, err := parseConfig([]byte("enable-model-prefix: false\nmodel-prefix: custom\n"))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	cfg.EnableModelPrefix = &off
	if got := cfg.modelPrefix(); got != "" {
		t.Fatalf("modelPrefix = %q, want empty when disabled", got)
	}

	on := true
	cfg.EnableModelPrefix = &on
	if got := cfg.modelPrefix(); got != "custom/" {
		t.Fatalf("modelPrefix = %q, want custom/", got)
	}
}

func TestRegisteredAndManifestModelID(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if got := registeredModelID(cfg, "pro"); got != "qwenworkcn/pro" {
		t.Fatalf("registeredModelID = %q", got)
	}
	if got := manifestModelID(cfg, "qwenworkcn/pro"); got != "pro" {
		t.Fatalf("manifestModelID = %q", got)
	}
	if got := manifestModelID(cfg, "other/pro"); got != "other/pro" {
		t.Fatalf("manifestModelID passthrough = %q", got)
	}
}

func TestParseConfigInvalidYAML(t *testing.T) {
	if _, err := parseConfig([]byte("enabled: [unclosed")); err == nil {
		t.Fatal("expected yaml error, got nil")
	}
}

func unmarshalJSON(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
