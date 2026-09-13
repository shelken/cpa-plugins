package main

import (
	"testing"
)

func TestModelFiltering(t *testing.T) {
	cases := []struct {
		id      string
		allowed bool
	}{
		{"deepseek-v4-pro", true},
		{"glm-5.0-turbo", true},
		{"kimi-k2.5", true},
		{"minimax-m3", true},

		// Tier 1 blacklist
		{"hunyuan-3b", false},
		{"hunyuan-7b-dense", false},
		{"auto", false},
		{"default", false},
		{"default-1.1", false},
		{"default-1.2", false},
		{"hunyuan-image-1", false},
		{"codewise-completion", false},
		{"completion-gf-1", false},

		// Tier 2 blacklist
		{"balanced-model", false},
		{"deep-model", false},
		{"fast-model", false},
		{"deepseek-v3", false},
		{"deepseek-v3-0324", false},
		{"deepseek-r1", false},
		{"deepseek-r1-0528", false},
		{"glm-4.6", false},
		{"glm-4.6v", false},
		{"kimi-k2-instruct-taiji", false},
		{"minimax-m2.5", false},
	}

	for _, tc := range cases {
		got := isModelAllowed(tc.id)
		if got != tc.allowed {
			t.Errorf("isModelAllowed(%q) = %v, want %v", tc.id, got, tc.allowed)
		}
	}
}

func TestFilterAndMapModels(t *testing.T) {
	manifestModels := []ManifestModel{
		{
			ID:                     "deepseek-v4-pro",
			Name:                   "Deepseek V4 Pro",
			DisplayName:            "Deepseek-V4-Pro",
			OwnedBy:                "workbuddy",
			ContextLength:          168000,
			MaxCompletionTokens:    32000,
			SupportsImages:         true,
			SupportsReasoning:      true,
			SupportedEfforts:       []string{"low", "high", "max"},
			DefaultReasoningEffort: "high",
		},
		{
			ID: "balanced-model", // blacklisted
		},
	}

	models := filterAndMapModels(mustParseConfig(t, nil), manifestModels)
	if len(models) != 1 {
		t.Fatalf("expected 1 model after filtering, got %d", len(models))
	}

	m := models[0]
	if m.ID != "workbuddy/deepseek-v4-pro" {
		t.Errorf("expected prefixed model id workbuddy/deepseek-v4-pro, got %s", m.ID)
	}
	if m.Thinking == nil {
		t.Fatal("expected thinking support, got nil")
	}
	if len(m.Thinking.Levels) != 3 {
		t.Errorf("expected 3 thinking levels, got %d", len(m.Thinking.Levels))
	}
	if len(m.SupportedInputModalities) != 2 {
		t.Errorf("expected text and image modalities, got %v", m.SupportedInputModalities)
	}
}

func TestFilterAndMapModelsPrefixDisabled(t *testing.T) {
	yamlData := []byte("enable-model-prefix: false\n")
	models := filterAndMapModels(mustParseConfig(t, yamlData), []ManifestModel{{ID: "deepseek-v4-pro"}})
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].ID != "deepseek-v4-pro" {
		t.Errorf("expected bare id deepseek-v4-pro, got %s", models[0].ID)
	}
}
