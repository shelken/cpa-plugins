package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseCredentialUpstreamFormat(t *testing.T) {
	futureExp := time.Now().Add(2 * time.Hour).UnixMilli()
	raw, _ := json.Marshal(map[string]any{
		"userId":       "usr-12345",
		"displayName":  "TestUser",
		"endpoint":     "https://copilot.tencent.com",
		"sessionState": "sess-state",
		"scope":        "openid profile",
		"domain":       "www.workbuddy.cn",
		"credentials": map[string]any{
			"access":  "tok-access",
			"refresh": "tok-refresh",
			"expires": futureExp,
		},
		// Upstream extra fields to tolerate and preserve
		"health":       "ok",
		"source":       "oauth",
		"phone":        "13800000000",
		"accountOrder": 1,
		"usage":        map[string]any{"requests": 10},
	})

	cred, err := parseCredential(raw)
	if err != nil {
		t.Fatalf("parse credential failed: %v", err)
	}

	if cred.UserID != "usr-12345" {
		t.Errorf("expected UserID usr-12345, got %s", cred.UserID)
	}
	if cred.AccessToken() != "tok-access" {
		t.Errorf("expected AccessToken tok-access, got %s", cred.AccessToken())
	}
	if cred.RefreshToken() != "tok-refresh" {
		t.Errorf("expected RefreshToken tok-refresh, got %s", cred.RefreshToken())
	}
	if cred.Expires() != futureExp {
		t.Errorf("expected Expires %d, got %d", futureExp, cred.Expires())
	}

	authData, err := cred.ToAuthData("usr-12345.json")
	if err != nil {
		t.Fatalf("ToAuthData failed: %v", err)
	}
	if authData.ID != "usr-12345" {
		t.Errorf("expected AuthData.ID usr-12345, got %s", authData.ID)
	}
	if authData.Provider != "workbuddy" {
		t.Errorf("expected AuthData.Provider workbuddy, got %s", authData.Provider)
	}

	expectedRefresh := time.UnixMilli(futureExp).Add(-5 * time.Minute)
	if !authData.NextRefreshAfter.Equal(expectedRefresh) {
		t.Errorf("expected NextRefreshAfter %v, got %v", expectedRefresh, authData.NextRefreshAfter)
	}

	// Verify StorageJSON has nested credentials and preserves extra fields
	var storedMap map[string]any
	if err := json.Unmarshal(authData.StorageJSON, &storedMap); err != nil {
		t.Fatalf("unmarshal storage json failed: %v", err)
	}

	credsMap, ok := storedMap["credentials"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested credentials object in storage json, got %v", storedMap["credentials"])
	}
	if credsMap["access"] != "tok-access" {
		t.Errorf("expected credentials.access tok-access, got %v", credsMap["access"])
	}
	if credsMap["refresh"] != "tok-refresh" {
		t.Errorf("expected credentials.refresh tok-refresh, got %v", credsMap["refresh"])
	}

	// Verify extra fields preserved
	if storedMap["health"] != "ok" {
		t.Errorf("expected health ok preserved, got %v", storedMap["health"])
	}
	if storedMap["phone"] != "13800000000" {
		t.Errorf("expected phone preserved, got %v", storedMap["phone"])
	}
}
