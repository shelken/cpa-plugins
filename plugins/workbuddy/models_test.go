package main

import (
	"encoding/json"
	"os"
	"testing"
)

// TestManifestModelsAllRegistered 钉住「插件侧不再有自己的模型黑名单」:
// 清单是唯一来源 (生成时已按官方隐藏规则过滤), 映射不得丢弃任何一个 id。
// 谁再往插件里加本地过滤规则, 这条会红。
func TestManifestModelsAllRegistered(t *testing.T) {
	raw, err := os.ReadFile("data/static-config.json")
	if err != nil {
		t.Fatalf("read static-config.json: %v", err)
	}
	var doc struct {
		Models []ManifestModel `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal static-config.json: %v", err)
	}
	if len(doc.Models) == 0 {
		t.Fatal("清单里没有模型, 判据失去意义")
	}

	models := mapManifestModels(mustParseConfig(t, nil), doc.Models)
	if len(models) != len(doc.Models) {
		t.Fatalf("清单声明 %d 个模型, 只注册了 %d 个: 插件侧不应再过滤", len(doc.Models), len(models))
	}
	for i, m := range models {
		if m.ID != registeredModelID(mustParseConfig(t, nil), doc.Models[i].ID) {
			t.Fatalf("第 %d 个模型 id 不匹配: %s", i, m.ID)
		}
	}
}

func TestMapManifestModels(t *testing.T) {
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
			ID: "glm-5.0-turbo",
		},
	}

	models := mapManifestModels(mustParseConfig(t, nil), manifestModels)
	if len(models) != len(manifestModels) {
		t.Fatalf("expected %d models, got %d", len(manifestModels), len(models))
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

func TestMapManifestModelsPrefixDisabled(t *testing.T) {
	yamlData := []byte("enable-model-prefix: false\n")
	models := mapManifestModels(mustParseConfig(t, yamlData), []ManifestModel{{ID: "deepseek-v4-pro"}})
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].ID != "deepseek-v4-pro" {
		t.Errorf("expected bare id deepseek-v4-pro, got %s", models[0].ID)
	}
}
