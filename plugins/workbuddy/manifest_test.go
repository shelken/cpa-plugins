package main

import (
	"encoding/json"
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
			"userResource": "/v2/billing/meter/get-user-resource",
			"dailyCheckin": "/v2/billing/meter/daily-checkin"
		},
		"profiles": {
			"desktop": {
				"login": {"platform": "workbuddy"},
				"headers": {
					"chat": {
						"User-Agent": "WorkBuddy/{{appVersion}} CLI/{{cliVersion}}"
					},
					"checkin": {
						"User-Agent": "axios/1.13.6"
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
			{"pattern": "CodeBuddy", "replacement": "CodeBuddy"}
		],
		"requestBodies": {"dailyCheckin": {}}
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

	sanitized := m.SanitizePrompt("Hello CodeBuddy!")
	if sanitized != "Hello CodeBuddy!" {
		t.Errorf("expected sanitization, got %q", sanitized)
	}
}

// 签到协议三项必须同时在场: 缺任何一项都会让运行时签到请求不保真。
func TestParseManifestRequiresCheckinContract(t *testing.T) {
	base := func() []byte {
		return []byte(`{
			"schemaVersion": 2,
			"provenance": {"captureUserAgent": "WorkBuddy/5.3.14 WorkBuddy/5.3.14 CLI/2.115.0", "captureClientVersion": "5.3.14"},
			"endpoints": {
				"baseUrl": "https://copilot.tencent.com",
				"chatCompletions": "/v2/chat/completions",
				"authState": "/v2/plugin/auth/state",
				"authToken": "/v2/plugin/auth/token",
				"tokenRefresh": "/v2/plugin/auth/token/refresh",
				"userResource": "/v2/billing/meter/get-user-resource",
				"dailyCheckin": "/v2/billing/meter/daily-checkin"
			},
			"profiles": {
				"desktop": {
					"login": {"platform": "workbuddy"},
					"headers": {"checkin": {"User-Agent": "axios/1.13.6"}},
					"body": {"omit": [], "set": {}, "extraVars": true}
				},
				"cli": {
					"login": {"platform": "cli"},
					"headers": {"chat": {"User-Agent": "CLI/{{cliVersion}}"}},
					"body": {"omit": [], "set": {}, "extraVars": false}
				}
			},
			"requestBodies": {"dailyCheckin": {}}
		}`)
	}

	if _, err := parseManifest(base()); err != nil {
		t.Fatalf("baseline manifest should parse, got %v", err)
	}

	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing endpoint", func(m map[string]any) { delete(m["endpoints"].(map[string]any), "dailyCheckin") }},
		{"missing checkin headers", func(m map[string]any) {
			delete(m["profiles"].(map[string]any)["desktop"].(map[string]any)["headers"].(map[string]any), "checkin")
		}},
		{"missing request body", func(m map[string]any) { delete(m["requestBodies"].(map[string]any), "dailyCheckin") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(base(), &doc); err != nil {
				t.Fatalf("unmarshal baseline: %v", err)
			}
			tc.mutate(doc)
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatalf("marshal mutated manifest: %v", err)
			}
			if _, err := parseManifest(raw); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}
