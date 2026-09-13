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
	Enabled         bool   `yaml:"enabled"`
	Priority        int    `yaml:"priority"`
	IdentityProfile string `yaml:"identity-profile"`
	LoginProfile    string `yaml:"login-profile"`
}

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

	return cfg, nil
}
