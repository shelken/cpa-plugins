package main

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var tier1ExcludedExact = map[string]struct{}{
	"hunyuan-3b":       {},
	"hunyuan-7b-dense": {},
	"auto":             {},
	"default":          {},
	"default-1.1":      {},
	"default-1.2":      {},
}

var tier1ExcludedPrefixes = []string{
	"hunyuan-image-",
	"codewise-",
	"completion-gf",
}

var tier2ExcludedExact = map[string]struct{}{
	"balanced-model": {},
	"deep-model":     {},
	"fast-model":     {},
}

var tier2ExcludedPrefixes = []string{
	"deepseek-v3",
	"deepseek-r1",
	"glm-4.6",
	"kimi-k2-",
	"minimax-m2.",
}

func isModelAllowed(id string) bool {
	idLower := strings.ToLower(strings.TrimSpace(id))

	if _, ok := tier1ExcludedExact[idLower]; ok {
		return false
	}
	for _, p := range tier1ExcludedPrefixes {
		if strings.HasPrefix(idLower, p) {
			return false
		}
	}

	if _, ok := tier2ExcludedExact[idLower]; ok {
		return false
	}
	for _, p := range tier2ExcludedPrefixes {
		if strings.HasPrefix(idLower, p) {
			return false
		}
	}

	return true
}

func filterAndMapModels(manifestModels []ManifestModel) []pluginapi.ModelInfo {
	models := make([]pluginapi.ModelInfo, 0, len(manifestModels))
	for _, m := range manifestModels {
		if !isModelAllowed(m.ID) {
			continue
		}

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
			ID:                         m.ID,
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
