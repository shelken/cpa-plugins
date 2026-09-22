package main

import (
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 模型可见性由生成器决定, 插件侧不重复过滤: scripts/staticctl.ts 导出清单时已按
// PROFILE.models.excludedIds/excludedIdPrefixes 与 src/models-filter.ts 的 isPiModel
// (balanced-model/deep-model/fast-model 三个精确 id + deepseek/glm/kimi/minimax 前缀)
// 过滤过, 写进 data/static-config.json 的就已经是可见集合。插件没有运行时模型发现,
// 清单是唯一来源, 本地再抄一份规则只会与生成器漂移 (实测对当前 21 条清单零命中)。
func mapManifestModels(cfg *PluginConfig, manifestModels []ManifestModel) []pluginapi.ModelInfo {
	models := make([]pluginapi.ModelInfo, 0, len(manifestModels))
	for _, m := range manifestModels {
		modalities := []string{"text"}
		if m.SupportsImages {
			modalities = append(modalities, "image")
		}

		var thinking *pluginapi.ThinkingSupport
		if m.SupportsReasoning {
			canOff := !m.OnlyReasoning
			if m.CanDisableThinking != nil {
				canOff = *m.CanDisableThinking
			}
			efforts := m.SupportedEfforts
			if len(efforts) == 0 && m.DefaultReasoningEffort != "" {
				efforts = []string{m.DefaultReasoningEffort}
			}
			thinking = &pluginapi.ThinkingSupport{
				ZeroAllowed:    canOff,
				DynamicAllowed: false,
				Levels:         efforts,
			}
		}

		displayName := m.DisplayName
		if displayName == "" {
			displayName = m.Name
		}
		if displayName == "" {
			displayName = m.ID
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		ownedBy := m.OwnedBy
		if ownedBy == "" {
			ownedBy = "workbuddy"
		}

		models = append(models, pluginapi.ModelInfo{
			ID:                         registeredModelID(cfg, m.ID),
			Object:                     "model",
			Name:                       name,
			DisplayName:                displayName,
			OwnedBy:                    ownedBy,
			ContextLength:              m.ContextLength,
			MaxCompletionTokens:        m.MaxCompletionTokens,
			SupportedInputModalities:   modalities,
			SupportedOutputModalities:  []string{"text"},
			SupportedGenerationMethods: []string{"chat"},
			Thinking:                   thinking,
		})
	}
	return models
}
