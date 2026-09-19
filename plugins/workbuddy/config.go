package main

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type ProfileType string

const (
	ProfileDesktop ProfileType = "desktop"
	ProfileCLI     ProfileType = "cli"
)

type PluginConfig struct {
	Enabled           bool   `yaml:"enabled"`
	Priority          int    `yaml:"priority"`
	IdentityProfile   string `yaml:"identity-profile"`
	LoginProfile      string `yaml:"login-profile"`
	EnableModelPrefix *bool  `yaml:"enable-model-prefix"`
	ModelPrefix       string `yaml:"model-prefix"`
	EnableCheckin     *bool  `yaml:"enable-checkin"`
}

const DefaultModelPrefix = "workbuddy"

func parseConfig(yamlBytes []byte) (*PluginConfig, error) {
	cfg := &PluginConfig{
		Enabled:         true,
		IdentityProfile: string(ProfileDesktop),
		LoginProfile:    string(ProfileDesktop),
	}

	if len(yamlBytes) > 0 {
		if err := yaml.Unmarshal(yamlBytes, cfg); err != nil {
			return nil, fmt.Errorf("unmarshal plugin config yaml: %w", err)
		}
	}

	cfg.IdentityProfile = strings.ToLower(strings.TrimSpace(cfg.IdentityProfile))
	if cfg.IdentityProfile == "" {
		cfg.IdentityProfile = string(ProfileDesktop)
	} else if cfg.IdentityProfile != string(ProfileDesktop) && cfg.IdentityProfile != string(ProfileCLI) {
		return nil, fmt.Errorf("invalid identity-profile %q, must be %q or %q", cfg.IdentityProfile, ProfileDesktop, ProfileCLI)
	}

	cfg.LoginProfile = strings.ToLower(strings.TrimSpace(cfg.LoginProfile))
	if cfg.LoginProfile == "" {
		cfg.LoginProfile = string(ProfileDesktop)
	} else if cfg.LoginProfile != string(ProfileDesktop) && cfg.LoginProfile != string(ProfileCLI) {
		return nil, fmt.Errorf("invalid login-profile %q, must be %q or %q", cfg.LoginProfile, ProfileDesktop, ProfileCLI)
	}

	cfg.ModelPrefix = strings.TrimSpace(cfg.ModelPrefix)

	return cfg, nil
}

// checkinEnabled 返回管理页手动签到开关。nil 视为开启, 与文档描述的默认行为一致。
func (c *PluginConfig) checkinEnabled() bool {
	return c.EnableCheckin == nil || *c.EnableCheckin
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
