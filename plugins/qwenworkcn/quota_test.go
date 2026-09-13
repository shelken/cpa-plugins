package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestQuotaDescribe(t *testing.T) {
	resp := handleQuotaDescribe()
	if len(resp.SupportedProviders) != 1 || resp.SupportedProviders[0] != "qwenworkcn" {
		t.Fatalf("SupportedProviders = %v, want [qwenworkcn]", resp.SupportedProviders)
	}
	if resp.DisplayName != "QwenWork" {
		t.Fatalf("DisplayName = %q, want QwenWork", resp.DisplayName)
	}
}

func TestQuotaFetchUsageSuccess(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/quota/usage" {
			if r.Header.Get("Authorization") != "Bearer test-tok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"user_quota": {"total": 100, "used": 30, "remaining": 70},
				"add_on_quota": {"total": 50, "used": 10, "remaining": 40}
			}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	oldClient := httpClient
	httpClient = server.Client()
	defer func() { httpClient = oldClient }()

	testManifest := *m
	testManifest.Endpoints.BaseURL = server.URL

	cred := Credential{
		UserID:    "u1",
		Endpoint:  server.URL,
		MachineID: "m1",
		Credentials: TokenCredentials{
			Access:  "test-tok",
			Refresh: "ref-tok",
		},
	}
	storageJSON, _ := cred.ToStorageJSON()

	resp, err := handleQuotaFetch(context.Background(), &testManifest, &PluginConfig{}, pluginapi.QuotaFetchRequest{
		StorageJSON: storageJSON,
	})
	if err != nil {
		t.Fatalf("handleQuotaFetch: %v", err)
	}

	if resp.Subscription.Plan != "QwenWork" {
		t.Fatalf("Plan = %q, want QwenWork", resp.Subscription.Plan)
	}
	if len(resp.Groups) != 1 || len(resp.Groups[0].Buckets) != 1 {
		t.Fatalf("Groups invalid: %+v", resp.Groups)
	}
	bucket := resp.Groups[0].Buckets[0]
	// 110 / 150 = 0.7333333333333333
	expectedFraction := 110.0 / 150.0
	if diff := bucket.RemainingFraction - expectedFraction; diff > 1e-4 || diff < -1e-4 {
		t.Fatalf("RemainingFraction = %f, want %f", bucket.RemainingFraction, expectedFraction)
	}
	if !strings.Contains(bucket.Description, "已用 40 / 总量 150") || !strings.Contains(bucket.Description, "剩余 110") {
		t.Fatalf("Description = %q", bucket.Description)
	}
}

func TestQuotaFetchWalletsFallback(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/quota/usage":
			// 真机上 qwenwork 的 usage 返回全 null
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user_quota": null, "add_on_quota": null}`))
		case "/user/wallets":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"data": {
					"active_wallets": {
						"wallets": [
							{"balance": 30, "valid_to": "2026-10-01"},
							{"balance": 70, "valid_to": "2026-09-15"},
							{"balance": "bad_entry", "valid_to": "2026-12-01"}
						]
					}
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldClient := httpClient
	httpClient = server.Client()
	defer func() { httpClient = oldClient }()

	testManifest := *m
	testManifest.Endpoints.BaseURL = server.URL
	testManifest.WebOrigin = server.URL

	cred := Credential{
		UserID:    "u1",
		Endpoint:  server.URL,
		MachineID: "m1",
		Credentials: TokenCredentials{
			Access:  "test-tok",
			Refresh: "ref-tok",
		},
	}
	storageJSON, _ := cred.ToStorageJSON()

	resp, err := handleQuotaFetch(context.Background(), &testManifest, &PluginConfig{}, pluginapi.QuotaFetchRequest{
		StorageJSON: storageJSON,
	})
	if err != nil {
		t.Fatalf("handleQuotaFetch: %v", err)
	}

	if resp.Subscription.TierName != "Wallet" {
		t.Fatalf("TierName = %q, want Wallet", resp.Subscription.TierName)
	}
	bucket := resp.Groups[0].Buckets[0]
	if bucket.RemainingFraction != 1.0 {
		t.Fatalf("RemainingFraction = %f, want 1.0", bucket.RemainingFraction)
	}
	if !strings.Contains(bucket.Description, "余额 100 credits") || !strings.Contains(bucket.Description, "最早 2026-09-15 到期") {
		t.Fatalf("Description = %q, want 100 credits and 2026-09-15", bucket.Description)
	}
}

func TestQuotaFetch401Error(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"INVALID_TOKEN"}`))
	}))
	defer server.Close()

	oldClient := httpClient
	httpClient = server.Client()
	defer func() { httpClient = oldClient }()

	testManifest := *m
	testManifest.Endpoints.BaseURL = server.URL
	testManifest.WebOrigin = server.URL

	cred := Credential{
		UserID:    "u1",
		Endpoint:  server.URL,
		MachineID: "m1",
		Credentials: TokenCredentials{
			Access:  "expired-tok",
			Refresh: "ref-tok",
		},
	}
	storageJSON, _ := cred.ToStorageJSON()

	if _, err := handleQuotaFetch(context.Background(), &testManifest, &PluginConfig{}, pluginapi.QuotaFetchRequest{
		StorageJSON: storageJSON,
	}); err == nil {
		t.Fatal("expected error on 401, got nil")
	}
}

func TestQuotaFetchEmptyAll(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/quota/usage":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case "/user/wallets":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"active_wallets":{"wallets":[]}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldClient := httpClient
	httpClient = server.Client()
	defer func() { httpClient = oldClient }()

	testManifest := *m
	testManifest.Endpoints.BaseURL = server.URL
	testManifest.WebOrigin = server.URL

	cred := Credential{
		UserID:    "u1",
		Endpoint:  server.URL,
		MachineID: "m1",
		Credentials: TokenCredentials{
			Access:  "tok",
			Refresh: "ref",
		},
	}
	storageJSON, _ := cred.ToStorageJSON()

	resp, err := handleQuotaFetch(context.Background(), &testManifest, &PluginConfig{}, pluginapi.QuotaFetchRequest{
		StorageJSON: storageJSON,
	})
	if err != nil {
		t.Fatalf("handleQuotaFetch: %v", err)
	}

	bucket := resp.Groups[0].Buckets[0]
	if bucket.RemainingFraction != 0.0 {
		t.Fatalf("RemainingFraction = %f, want 0.0", bucket.RemainingFraction)
	}
	if !strings.Contains(bucket.Description, "无可用额度包且钱包为空") {
		t.Fatalf("Description = %q", bucket.Description)
	}
}
