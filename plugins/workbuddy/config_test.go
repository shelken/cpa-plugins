package main

import (
	"testing"
)

func mustParseConfig(t *testing.T, yamlBytes []byte) *PluginConfig {
	t.Helper()
	cfg, err := parseConfig(yamlBytes)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return cfg
}

// withTestConfig 临时替换插件全局配置, 测试结束恢复。config/management handler 共用同一状态。
func withTestConfig(t *testing.T, cfg *PluginConfig) {
	t.Helper()
	pluginStateMu.Lock()
	prev := currentConfig
	currentConfig = cfg
	pluginStateMu.Unlock()
	t.Cleanup(func() {
		pluginStateMu.Lock()
		currentConfig = prev
		pluginStateMu.Unlock()
	})
}

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Enabled {
		t.Errorf("expected Enabled true, got %v", cfg.Enabled)
	}
	if cfg.IdentityProfile != string(ProfileDesktop) {
		t.Errorf("expected IdentityProfile desktop, got %s", cfg.IdentityProfile)
	}
	if cfg.LoginProfile != string(ProfileDesktop) {
		t.Errorf("expected LoginProfile desktop, got %s", cfg.LoginProfile)
	}
	if cfg.modelPrefix() != "workbuddy/" {
		t.Errorf("expected modelPrefix workbuddy/, got %q", cfg.modelPrefix())
	}
}

func TestParseConfigExplicit(t *testing.T) {
	yamlData := []byte(`
enabled: true
priority: 5
identity-profile: cli
login-profile: cli
`)
	cfg, err := parseConfig(yamlData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Priority != 5 {
		t.Errorf("expected Priority 5, got %d", cfg.Priority)
	}
	if cfg.IdentityProfile != string(ProfileCLI) {
		t.Errorf("expected IdentityProfile cli, got %s", cfg.IdentityProfile)
	}
	if cfg.LoginProfile != string(ProfileCLI) {
		t.Errorf("expected LoginProfile cli, got %s", cfg.LoginProfile)
	}
}

func TestParseConfigInvalidProfile(t *testing.T) {
	yamlData := []byte(`
identity-profile: invalid
`)
	_, err := parseConfig(yamlData)
	if err == nil {
		t.Fatal("expected error for invalid profile, got nil")
	}
}

// 签到开关缺省为 true, 显式 false 才关闭; 与管理字段描述一致。
func TestParseConfigCheckinEnabled(t *testing.T) {
	defaultCfg := mustParseConfig(t, nil)
	if !defaultCfg.checkinEnabled() {
		t.Error("expected checkin enabled by default")
	}

	enabledCfg := mustParseConfig(t, []byte("enable-checkin: true\n"))
	if !enabledCfg.checkinEnabled() {
		t.Error("expected checkin enabled when explicitly true")
	}

	disabledCfg := mustParseConfig(t, []byte("enable-checkin: false\n"))
	if disabledCfg.checkinEnabled() {
		t.Error("expected checkin disabled when explicitly false")
	}
}

func TestModelPrefixDisabled(t *testing.T) {
	yamlData := []byte("enable-model-prefix: false\n")
	cfg, err := parseConfig(yamlData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := registeredModelID(cfg, "hy3"); got != "hy3" {
		t.Errorf("expected bare id hy3 when prefix disabled, got %q", got)
	}
}

func TestModelPrefixCustom(t *testing.T) {
	yamlData := []byte("model-prefix: custom\n")
	cfg, err := parseConfig(yamlData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := registeredModelID(cfg, "hy3"); got != "custom/hy3" {
		t.Errorf("expected custom/hy3, got %q", got)
	}
	if got := manifestModelID(cfg, "custom/hy3"); got != "hy3" {
		t.Errorf("expected stripped id hy3, got %q", got)
	}
}
