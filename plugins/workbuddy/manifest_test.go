package main

import (
	"strings"
	"testing"
)

func TestParseManifestValidation(t *testing.T) {
	_, err := parseManifest([]byte(`{"schemaVersion": 1}`))
	if err == nil {
		t.Fatal("expected error for schemaVersion 1, got nil")
	}

	validJSON := []byte(`{
		"schemaVersion": 2,
		"provenance": {
			"captureUserAgent": "WorkBuddy/5.3.14 WorkBuddy/5.3.14 CLI/2.115.0",
			"captureClientVersion": "5.3.14"
		},
		"endpoints": {
			"baseUrl": "https://copilot.tencent.com",
			"chatCompletions": "/v2/chat/completions",
			"authState": "/v2/plugin/auth/state",
			"authToken": "/v2/plugin/auth/token",
			"tokenRefresh": "/v2/plugin/auth/token/refresh",
			"userResource": "/v2/billing/meter/get-user-resource"
		},
		"profiles": {
			"desktop": {
				"login": {"platform": "workbuddy"},
				"headers": {
					"chat": {
						"User-Agent": "WorkBuddy/{{appVersion}} CLI/{{cliVersion}}"
					}
				},
				"body": {
					"omit": ["store"],
					"set": {"verbosity": "high"},
					"extraVars": true
				}
			},
			"cli": {
				"login": {"platform": "cli"},
				"headers": {
					"chat": {
						"User-Agent": "CLI/{{cliVersion}}"
					}
				},
				"body": {
					"omit": [],
					"set": {"store": false},
					"extraVars": false
				}
			}
		},
		"sanitizations": [
			{"pattern": "Claude Code", "replacement": "CodeBuddy"}
		]
	}`)

	m, err := parseManifest(validJSON)
	if err != nil {
		t.Fatalf("unexpected error parsing manifest: %v", err)
	}

	desktopChatUA := m.Profiles["desktop"].Headers["chat"]["User-Agent"]
	if !strings.Contains(desktopChatUA, "5.3.14") || !strings.Contains(desktopChatUA, "2.115.0") {
		t.Errorf("expected placeholder replacement in desktop UA, got %q", desktopChatUA)
	}

	cliChatUA := m.Profiles["cli"].Headers["chat"]["User-Agent"]
	if !strings.Contains(cliChatUA, "2.115.0") {
		t.Errorf("expected placeholder replacement in cli UA, got %q", cliChatUA)
	}

	sanitized := m.SanitizePrompt("Hello Claude Code!")
	if sanitized != "Hello CodeBuddy!" {
		t.Errorf("expected sanitization, got %q", sanitized)
	}
}
