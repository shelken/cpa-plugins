package main

import (
	"encoding/json"
	"os"
	"testing"
)

// TestManifestModelsAllRegistered 钉住「静态清单映射不过滤」:
// 静态清单是生成器过滤后的产物, 映射不得丢弃任何一个 id。
// 动态路径 (/v3/config) 的黑名单过滤只发生在 parseModelsConfig,
// 两条路径在 mapManifestModels 汇合, 这里保证静态路径零丢失。
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

// parseModelsConfig 的判据来自 2026-09-24 真实抓包 (workbuddy_20260924_190947.json):
// 黑名单精确剔除非聊天模型, 其余全量暴露, 上游新增模型不在黑名单即自动出现。
func TestParseModelsConfig(t *testing.T) {
	body := []byte(`{
  "code": 0,
  "msg": "OK",
  "data": {
    "agents": [{"name": "cli", "models": ["auto", "hy4-preview-f", "hy3", "hy3-x"]}],
    "models": [
      {"id": "auto", "name": "Auto", "maxInputTokens": 256000, "maxOutputTokens": 32000,
       "supportsImages": true, "supportsReasoning": true, "onlyReasoning": false,
       "credits": null,
       "reasoning": {"supportedEfforts": ["low", "high"], "canDisableThinking": true, "defaultEffort": "low"}},
      {"id": "hy3", "name": "Hy3", "maxInputTokens": 192000, "maxOutputTokens": 64000,
       "supportsImages": true, "onlyReasoning": true, "credits": "x0.00",
       "reasoning": {"supportedEfforts": ["low", "high"], "canDisableThinking": false, "defaultEffort": "high"}},
      {"id": "hy3-x", "name": "Hy3", "maxInputTokens": 192000, "maxOutputTokens": 64000,
       "supportsImages": true, "onlyReasoning": true, "credits": "x0.05",
       "reasoning": {"supportedEfforts": ["low", "high"], "canDisableThinking": false, "defaultEffort": "high"}},
      {"id": "hy4-preview-f", "name": "Hy4 preview", "maxInputTokens": 300000, "maxOutputTokens": 48000,
       "supportsImages": true, "onlyReasoning": true, "credits": "x0.00",
       "reasoning": {"supportedEfforts": ["high"], "canDisableThinking": false, "defaultEffort": "high"}},
      {"id": "deepseek-v3-0324", "name": "DeepSeek-V3.2", "maxInputTokens": 96000, "maxOutputTokens": 32000,
       "supportsImages": false, "credits": "x0.29"},
      {"id": "visionless", "name": "Visionless", "maxInputTokens": 8000, "maxOutputTokens": 1000},
      {"id": "codewise-completions", "name": "codewise-completions", "supportsImages": true},
      {"id": "completion-gf", "name": "completion-gf", "supportsImages": true},
      {"id": "deepseek-v3-0324-taco-completion", "name": "deepseek-v3-0324", "supportsImages": true},
      {"id": "hunyuan-image-alpha", "name": "Hunyuan-Image-Alpha", "supportsImages": true},
      {"id": "fast-model", "name": "快速", "supportsImages": true},
      {"id": "default-1.1", "name": "Claude-3.7-Sonnet", "supportsImages": true}
    ]
  }
}`)

	got, err := parseModelsConfig(body)
	if err != nil {
		t.Fatalf("parseModelsConfig: %v", err)
	}

	byID := make(map[string]ManifestModel, len(got))
	for _, m := range got {
		if _, dup := byID[m.ID]; dup {
			t.Fatalf("重复模型 id: %s", m.ID)
		}
		byID[m.ID] = m
	}

	// 12 个详情 - 6 个黑名单 = 6 个暴露; 非 cli 引用但不在黑名单的保留 (deepseek-v3-0324, visionless)
	if len(got) != 6 {
		t.Fatalf("期望暴露 6 个模型, 实际 %d: %v", len(got), ids(got))
	}
	for _, excluded := range []string{"codewise-completions", "completion-gf", "deepseek-v3-0324-taco-completion", "hunyuan-image-alpha", "fast-model", "default-1.1"} {
		if _, ok := byID[excluded]; ok {
			t.Errorf("黑名单模型 %s 不应暴露", excluded)
		}
	}
	for _, keep := range []string{"auto", "hy3", "hy3-x", "hy4-preview-f", "deepseek-v3-0324", "visionless"} {
		if _, ok := byID[keep]; !ok {
			t.Errorf("模型 %s 应暴露", keep)
		}
	}

	// supportsImages 三态: 缺失(nil) 视为 true, 显式 false 保持 false
	if !byID["visionless"].SupportsImages {
		t.Error("supportsImages 缺失应视为 true, 否则图片链路被静默关闭")
	}
	if byID["deepseek-v3-0324"].SupportsImages {
		t.Error("supportsImages 显式 false 不应被翻成 true")
	}

	// 重名消歧: 同 name 的后续模型 DisplayName 加 id 差量后缀
	if byID["hy3"].DisplayName != "Hy3" {
		t.Errorf("首个同名模型保留原名, got %q", byID["hy3"].DisplayName)
	}
	if byID["hy3-x"].DisplayName != "Hy3 (X)" {
		t.Errorf("重名模型应带 id 差量后缀, got %q", byID["hy3-x"].DisplayName)
	}

	// reasoning 字段映射
	if byID["auto"].DefaultReasoningEffort != "low" {
		t.Errorf("defaultEffort 应映射到 DefaultReasoningEffort, got %q", byID["auto"].DefaultReasoningEffort)
	}
	if byID["auto"].OnlyReasoning {
		t.Error("onlyReasoning=false 不应被翻成 true")
	}

	// credits 归一: "x0.00" 免费, "x0.05" 收费, null 为空
	if byID["hy3"].Credits != "x0.00" {
		t.Errorf("hy3 credits 应为 x0.00, got %q", byID["hy3"].Credits)
	}
	if byID["auto"].Credits != "" {
		t.Errorf("credits=null 应为空串, got %q", byID["auto"].Credits)
	}
}

// credits 最终进 ModelInfo.Description, 宿主无专字段 (sdk/pluginapi/types.go ModelInfo)。
func TestMapManifestModelsCreditsDescription(t *testing.T) {
	manifestModels := []ManifestModel{
		{ID: "hy3", Name: "Hy3", DisplayName: "Hy3", Credits: "x0.00"},
		{ID: "kimi-k3-1", Name: "Kimi-K3-1", DisplayName: "Kimi-K3-1", Credits: "x1.62"},
		{ID: "auto", Name: "Auto", DisplayName: "Auto"},
	}

	models := mapManifestModels(mustParseConfig(t, nil), manifestModels)
	if got := models[0].Description; got != "积分 x0.00（免费）" {
		t.Errorf("免费模型 Description = %q", got)
	}
	if got := models[1].Description; got != "积分 x1.62" {
		t.Errorf("收费模型 Description = %q", got)
	}
	if got := models[2].Description; got != "" {
		t.Errorf("无 credits 的模型 Description 应为空, got %q", got)
	}
}

// 同 name 且 id 无公共前缀时, 后缀退化为完整 id, 保证不撞名。
func TestDisambiguateNamesFallback(t *testing.T) {
	models := []ManifestModel{
		{ID: "foo", Name: "Same", DisplayName: "Same"},
		{ID: "bar", Name: "Same", DisplayName: "Same"},
	}
	disambiguateNames(models)
	if models[1].DisplayName != "Same (bar)" {
		t.Errorf("无前缀差量时后缀应为完整 id, got %q", models[1].DisplayName)
	}
}

// currentModels: 动态清单优先, 未拉取时静态兜底; Reconfigure 丢弃静态缓存
// 不得影响已拉取的动态清单 (宿主凭据落盘会触发 Reconfigure, 实测踩过)。
func TestCurrentModelsPrefersLive(t *testing.T) {
	staticModels := []ManifestModel{{ID: "static-a"}}
	manifest := &ManifestV2{Models: staticModels}

	if got := currentModels(manifest); len(got) != 1 || got[0].ID != "static-a" {
		t.Fatalf("未拉取时应返回静态清单, got %v", ids(got))
	}

	liveModels = []ManifestModel{{ID: "live-1"}, {ID: "live-2"}}
	t.Cleanup(func() { liveModels = nil })

	got := currentModels(manifest)
	if len(got) != 2 || got[0].ID != "live-1" {
		t.Fatalf("已拉取时应返回动态清单, got %v", ids(got))
	}
}

func ids(ms []ManifestModel) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}
