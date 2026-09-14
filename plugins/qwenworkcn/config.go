package main

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type PluginConfig struct {
	Enabled           bool   `yaml:"enabled"`
	Priority          int    `yaml:"priority"`
	EnableModelPrefix *bool  `yaml:"enable-model-prefix"`
	ModelPrefix       string `yaml:"model-prefix"`
}

const DefaultModelPrefix = "qwenworkcn"

func parseConfig(yamlBytes []byte) (*PluginConfig, error) {
	cfg := &PluginConfig{Enabled: true}

	if len(yamlBytes) > 0 {
		if err := yaml.Unmarshal(yamlBytes, cfg); err != nil {
			return nil, fmt.Errorf("unmarshal plugin config yaml: %w", err)
		}
	}

	cfg.ModelPrefix = strings.TrimSpace(cfg.ModelPrefix)

	return cfg, nil
}

// modelPrefix 返回带斜杠的前缀, 关闭时返回空串。nil 视为开启, 与文档描述的默认行为一致。
func (c *PluginConfig) modelPrefix() string {
	if c.EnableModelPrefix != nil && !*c.EnableModelPrefix {
		return ""
	}
	prefix := c.ModelPrefix
	if prefix == "" {
		prefix = DefaultModelPrefix
	}
	return prefix + "/"
}

// registeredModelID 注册侧: 给模型 id 加前缀。
func registeredModelID(cfg *PluginConfig, id string) string {
	return cfg.modelPrefix() + id
}

// manifestModelID 执行侧: 剥掉注册时加的前缀, 不匹配则原样返回。
func manifestModelID(cfg *PluginConfig, modelID string) string {
	if p := cfg.modelPrefix(); p != "" {
		stripped, _ := strings.CutPrefix(modelID, p)
		return stripped
	}
	return modelID
}
