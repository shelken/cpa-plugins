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

// 主源是钱包: 用量包里即便有历史余额, 也不能盖住钱包里真正可用的额度 (ADR-0001 方案 A)。
func TestQuotaFetchWalletsBeatsUsagePack(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/wallets":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"active_wallets":{"wallets":[{"balance":97,"valid_to":"2026-09-15T00:00:00+08:00"}]}}}`))
		case "/api/v2/quota/usage":
			// 历史用量包已用尽, 不代表账号没钱
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user_quota":{"total":100,"used":100,"remaining":0}}`))
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
		t.Fatalf("TierName = %q, want Wallet: 钱包余额被用量包盖住了", resp.Subscription.TierName)
	}
	if !strings.Contains(resp.Groups[0].Buckets[0].Description, "余额 97 credits") {
		t.Fatalf("Description = %q", resp.Groups[0].Buckets[0].Description)
	}
}

// 主源故障时兜底仍要生效, 不能因为钱包查不到就说账号没额度。
func TestQuotaFetchUsageFallbackWhenWalletsFails(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/wallets":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
		case "/api/v2/quota/usage":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user_quota":{"total":100,"used":30,"remaining":70}}`))
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
	if !strings.Contains(resp.Groups[0].Buckets[0].Description, "剩余 70") {
		t.Fatalf("Description = %q, want usage fallback", resp.Groups[0].Buckets[0].Description)
	}
}

// 两个源都失败时必须报错: 报成 0 额度会让管理面把可用账号显示成额度耗尽。
func TestQuotaFetchUpstreamFailureIsNotZeroQuota(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"gateway down"}`))
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

	if _, err := handleQuotaFetch(context.Background(), &testManifest, &PluginConfig{}, pluginapi.QuotaFetchRequest{
		StorageJSON: storageJSON,
	}); err == nil {
		t.Fatal("两个额度源都返回 502 时必须报错, 实际报成了成功的 0 额度")
	}
}

// 永久有效的钱包没有 valid_to, 不能让它把其他钱包的真实到期日盖掉。
func TestQuotaFetchWalletWithoutValidToKeepsEarliestExpiry(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user/wallets" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"active_wallets":{"wallets":[
				{"balance":10,"valid_to":""},
				{"balance":20,"valid_to":"2026-11-01T00:00:00+08:00"},
				{"balance":70,"valid_to":"2026-09-15T00:00:00+08:00"}
			]}}}`))
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
	desc := resp.Groups[0].Buckets[0].Description
	if !strings.Contains(desc, "余额 100 credits") {
		t.Fatalf("Description = %q, want 100 credits", desc)
	}
	if !strings.Contains(desc, "最早 2026-09-15") {
		t.Fatalf("Description = %q: 无 valid_to 的钱包盖掉了其他钱包的最早到期日", desc)
	}
}
