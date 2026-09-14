package main

import (
	"testing"
)

func TestFilterAndMapModels(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	cfg := &PluginConfig{Enabled: true}

	models := filterAndMapModels(cfg, m.Models)
	if len(models) != 3 {
		t.Fatalf("models = %d, want 3", len(models))
	}

	first := models[0]
	if first.ID != "qwenworkcn/pro" {
		t.Fatalf("first ID = %q, want qwenworkcn/pro", first.ID)
	}
	if first.OwnedBy != "qwenworkcn" {
		t.Fatalf("ownedBy = %q", first.OwnedBy)
	}
	if first.ContextLength != 1_000_000 {
		t.Fatalf("contextLength = %d", first.ContextLength)
	}
	if first.MaxCompletionTokens != 128_000 {
		t.Fatalf("maxCompletionTokens = %d", first.MaxCompletionTokens)
	}

	// 视觉: 清单 supportsImages=true → modalities 含 image
	hasImage := false
	for _, mod := range first.SupportedInputModalities {
		if mod == "image" {
			hasImage = true
		}
	}
	if !hasImage {
		t.Fatalf("modalities = %v, want image", first.SupportedInputModalities)
	}

	// onlyReasoning → ZeroAllowed=false, levels=low..max
	if first.Thinking == nil {
		t.Fatal("thinking = nil, want set for reasoning model")
	}
	if first.Thinking.ZeroAllowed {
		t.Fatal("ZeroAllowed = true, want false for onlyReasoning model")
	}
	wantLevels := []string{"low", "medium", "high", "xhigh", "max"}
	if len(first.Thinking.Levels) != len(wantLevels) {
		t.Fatalf("levels = %v, want %v", first.Thinking.Levels, wantLevels)
	}
	for i, l := range wantLevels {
		if first.Thinking.Levels[i] != l {
			t.Fatalf("levels[%d] = %q, want %q", i, first.Thinking.Levels[i], l)
		}
	}
}

func TestFilterAndMapModelsAllThree(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	models := filterAndMapModels(&PluginConfig{}, m.Models)
	wantIDs := []string{"qwenworkcn/pro", "qwenworkcn/flash", "qwenworkcn/qwen3.8-max-preview"}
	for i, want := range wantIDs {
		if models[i].ID != want {
			t.Fatalf("models[%d].ID = %q, want %q", i, models[i].ID, want)
		}
	}
}
