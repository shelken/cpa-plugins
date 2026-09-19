package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testManifest 是 claimDailyCheckin 的最小有效清单: 与 data/static-config.json 的签到契约同构。
func testManifest(t *testing.T, upstreamURL string) *ManifestV2 {
	t.Helper()
	raw := []byte(`{
		"schemaVersion": 2,
		"provenance": {"captureUserAgent": "WorkBuddy/5.3.14 WorkBuddy/5.3.14 CLI/2.115.0", "captureClientVersion": "5.3.14"},
		"endpoints": {
			"baseUrl": "` + upstreamURL + `",
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
				"headers": {"checkin": {"User-Agent": "axios/1.13.6", "X-Domain": "www.workbuddy.cn"}},
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
	m, err := parseManifest(raw)
	if err != nil {
		t.Fatalf("parse test manifest: %v", err)
	}
	return m
}

func testCredential() *Credential {
	cred, err := parseCredential([]byte(`{
		"type": "workbuddy",
		"userId": "usr-1",
		"displayName": "Tester",
		"endpoint": "https://copilot.tencent.com",
		"credentials": {"access": "tok-access", "refresh": "tok-refresh", "expires": 9999999999999}
	}`))
	if err != nil {
		panic(err)
	}
	return cred
}

// 签到请求必须保真: POST 正确路径, 空对象 body, desktop checkin 头 + 动态身份头。
func TestClaimDailyCheckinSendsProtocolFidelityRequest(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	var gotHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"credit":150,"streak_days":3}}`))
	}))
	defer upstream.Close()

	result, err := claimDailyCheckin(context.Background(), testManifest(t, upstream.URL), testCredential())
	if err != nil {
		t.Fatalf("claimDailyCheckin: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/v2/billing/meter/daily-checkin" {
		t.Errorf("path = %s, want /v2/billing/meter/daily-checkin", gotPath)
	}
	if gotBody != "{}" {
		t.Errorf("body = %s, want {}", gotBody)
	}
	if gotHeaders.Get("User-Agent") != "axios/1.13.6" {
		t.Errorf("User-Agent = %q, want axios/1.13.6 (desktop checkin 头组)", gotHeaders.Get("User-Agent"))
	}
	if gotHeaders.Get("X-Domain") != "www.workbuddy.cn" {
		t.Errorf("X-Domain = %q, want www.workbuddy.cn", gotHeaders.Get("X-Domain"))
	}
	if gotHeaders.Get("Authorization") != "Bearer tok-access" {
		t.Errorf("Authorization = %q, want Bearer token", gotHeaders.Get("Authorization"))
	}
	if gotHeaders.Get("X-User-Id") != "usr-1" {
		t.Errorf("X-User-Id = %q, want usr-1", gotHeaders.Get("X-User-Id"))
	}

	if result.Status != checkinStatusClaimed {
		t.Errorf("status = %q, want claimed", result.Status)
	}
	if result.Credit == nil || *result.Credit != 150 {
		t.Errorf("credit = %v, want 150", result.Credit)
	}
	if result.StreakDays == nil || *result.StreakDays != 3 {
		t.Errorf("streak_days = %v, want 3", result.StreakDays)
	}
}

func TestClaimDailyCheckinAlreadyClaimedVariants(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"code -1 msg", `{"code":-1,"msg":"already_claimed"}`},
		{"code 10001", `{"code":10001,"msg":"今天已签到，请明天再来"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer upstream.Close()

			result, err := claimDailyCheckin(context.Background(), testManifest(t, upstream.URL), testCredential())
			if err != nil {
				t.Fatalf("claimDailyCheckin: %v", err)
			}
			if result.Status != checkinStatusAlreadyClaimed {
				t.Errorf("status = %q, want already_claimed", result.Status)
			}
			if result.Credit != nil || result.StreakDays != nil {
				t.Errorf("already_claimed 不应携带奖励字段: %+v", result)
			}
		})
	}
}

func TestClaimDailyCheckinErrors(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		contains string
	}{
		{"non-2xx", http.StatusBadGateway, `{"code":-1,"msg":"boom"}`, "status 502"},
		{"invalid json", http.StatusOK, `not-json`, "unmarshal"},
		{"code 0 without data", http.StatusOK, `{"code":0}`, "data is missing"},
		{"other business code", http.StatusOK, `{"code":-2,"msg":"rate limited"}`, "code=-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer upstream.Close()

			_, err := claimDailyCheckin(context.Background(), testManifest(t, upstream.URL), testCredential())
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.contains) {
				t.Errorf("error %q 应包含 %q", err.Error(), tc.contains)
			}
		})
	}
}

// 5s 超时来自源实现 claimCheckin; 上游挂起时必须在时限内返回错误而非永久阻塞。
func TestClaimDailyCheckinHonorsTimeout(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	// LIFO: 先 close(release) 放行阻塞的 handler, 再 Close 服务器, 否则 Close 永久等待。
	defer upstream.Close()
	defer close(release)

	start := time.Now()
	_, err := claimDailyCheckin(context.Background(), testManifest(t, upstream.URL), testCredential())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("超时未生效: 耗时 %s, 应在 5s 时限附近返回", elapsed)
	}
}

// 管理端点成功响应的 JSON 形状必须与 checkinResult 一致, 且不含任何凭据字段。
func TestCheckinResultJSONShape(t *testing.T) {
	credit := 150
	streak := 3
	raw, err := json.Marshal(checkinResult{Status: checkinStatusClaimed, Credit: &credit, StreakDays: &streak})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc["status"] != "claimed" || doc["credit"] != float64(150) || doc["streak_days"] != float64(3) {
		t.Errorf("unexpected shape: %s", raw)
	}

	bare, _ := json.Marshal(checkinResult{Status: checkinStatusAlreadyClaimed})
	if string(bare) != `{"status":"already_claimed"}` {
		t.Errorf("omitempty 失效: %s", bare)
	}
}
