package main

import (
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 静态清单即唯一模型来源, 无需按 id 过滤 (workbuddy 的 tier 排除表针对动态发现,
// 这里 models 恒为生成器导出的 3 条)。
func filterAndMapModels(cfg *PluginConfig, manifestModels []ManifestModel) []pluginapi.ModelInfo {
	models := make([]pluginapi.ModelInfo, 0, len(manifestModels))
	for _, m := range manifestModels {
		modalities := []string{"text"}
		if m.SupportsImages {
			modalities = append(modalities, "image")
		}

		var thinking *pluginapi.ThinkingSupport
		if m.SupportsReasoning {
			canOff := !m.OnlyReasoning
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
			ownedBy = "qwenworkcn"
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
