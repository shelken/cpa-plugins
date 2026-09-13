package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestParseCredential(t *testing.T) {
	// 1. 正常凭据解析与字段透传
	validJSON := `{
		"userId": "u123",
		"displayName": "Alice",
		"endpoint": "https://gateway.qwenwork.cn",
		"machineId": "m-abc-123",
		"credentials": {
			"access": "tok-access-1",
			"refresh": "tok-refresh-1",
			"expires": 1800000000000
		},
		"organization_id": "org-99",
		"data_policy_agreed": true
	}`

	cred, err := parseCredential([]byte(validJSON))
	if err != nil {
		t.Fatalf("parseCredential: %v", err)
	}
	if cred.UserID != "u123" {
		t.Fatalf("UserID = %q, want u123", cred.UserID)
	}
	if cred.DisplayName != "Alice" {
		t.Fatalf("DisplayName = %q, want Alice", cred.DisplayName)
	}
	if cred.MachineID != "m-abc-123" {
		t.Fatalf("MachineID = %q, want m-abc-123", cred.MachineID)
	}
	if cred.AccessToken() != "tok-access-1" {
		t.Fatalf("AccessToken = %q, want tok-access-1", cred.AccessToken())
	}
	if cred.RefreshToken() != "tok-refresh-1" {
		t.Fatalf("RefreshToken = %q, want tok-refresh-1", cred.RefreshToken())
	}
	if cred.Expires() != 1800000000000 {
		t.Fatalf("Expires = %d, want 1800000000000", cred.Expires())
	}
	if cred.extra["organization_id"] != "org-99" {
		t.Fatalf("extra[organization_id] = %v, want org-99", cred.extra["organization_id"])
	}

	// 2. ToStorageJSON 往返与 extra 保留
	storageBytes, err := cred.ToStorageJSON()
	if err != nil {
		t.Fatalf("ToStorageJSON: %v", err)
	}
	var storageMap map[string]any
	if err := json.Unmarshal(storageBytes, &storageMap); err != nil {
		t.Fatalf("unmarshal storage json: %v", err)
	}
	if storageMap["organization_id"] != "org-99" {
		t.Fatalf("organization_id not preserved in storage json: %v", storageMap)
	}

	// 3. 缺少必需字段 fail-fast
	missingAccess := `{"machineId":"m","credentials":{"refresh":"r"}}`
	if _, err := parseCredential([]byte(missingAccess)); err == nil {
		t.Fatal("expected error on missing access, got nil")
	}

	missingRefresh := `{"machineId":"m","credentials":{"access":"a"}}`
	if _, err := parseCredential([]byte(missingRefresh)); err == nil {
		t.Fatal("expected error on missing refresh, got nil")
	}

	missingMid := `{"credentials":{"access":"a","refresh":"r"}}`
	if _, err := parseCredential([]byte(missingMid)); err == nil {
		t.Fatal("expected error on missing machineId, got nil")
	}
}

func TestParseAccessExpiresAt(t *testing.T) {
	now := time.Unix(1700000000, 0) // 1700000000000 ms

	// 1. expires_in 优先
	body1 := map[string]any{
		"expires_in": 3600,
		"expires_at": now.UnixMilli() + 7200*1000,
	}
	ms, ok := parseAccessExpiresAt(body1, now)
	if !ok || ms != now.UnixMilli()+3600*1000 {
		t.Fatalf("expires_in parse: got (%d, %v), want (%d, true)", ms, ok, now.UnixMilli()+3600*1000)
	}

	// 2. expires_at 距今 > 24h 视为 refresh 过期，忽略
	body2 := map[string]any{
		"expires_at": now.UnixMilli() + 30*24*3600*1000,
	}
	ms, ok = parseAccessExpiresAt(body2, now)
	if ok || ms != 0 {
		t.Fatalf("refresh expiration should be ignored: got (%d, %v)", ms, ok)
	}

	// 3. expires_at 距今 <= 24h 正常解析
	body3 := map[string]any{
		"expires_at": now.UnixMilli() + 2*3600*1000,
	}
	ms, ok = parseAccessExpiresAt(body3, now)
	if !ok || ms != now.UnixMilli()+2*3600*1000 {
		t.Fatalf("access expiration <= 24h: got (%d, %v)", ms, ok)
	}

	// 4. 空/不存在
	body4 := map[string]any{}
	ms, ok = parseAccessExpiresAt(body4, now)
	if ok || ms != 0 {
		t.Fatalf("empty body should return false: got (%d, %v)", ms, ok)
	}
}

func TestCredentialExpiresAt(t *testing.T) {
	now := time.Unix(1700000000, 0)
	nowMs := now.UnixMilli()

	// 1. 指定 access 过期时间: 减 300s
	accessExp := nowMs + 3600*1000
	want := accessExp - 300_000
	if got := credentialExpiresAt(accessExp, now); got != want {
		t.Fatalf("credentialExpiresAt = %d, want %d", got, want)
	}

	// 2. 未指定: fallback now+1h - 300s
	wantFallback := nowMs + 3600*1000 - 300_000
	if got := credentialExpiresAt(0, now); got != wantFallback {
		t.Fatalf("credentialExpiresAt fallback = %d, want %d", got, wantFallback)
	}

	// 3. 过期时间很短: 保底 now+5s
	shortExp := nowMs + 10_000
	wantMin := nowMs + 5000
	if got := credentialExpiresAt(shortExp, now); got != wantMin {
		t.Fatalf("credentialExpiresAt min bound = %d, want %d", got, wantMin)
	}
}

func TestResolveDisplayName(t *testing.T) {
	// 1. 昵称 + 手机号
	info1 := map[string]any{
		"nickname": "Bob",
		"phone":    "13800001111",
	}
	if got := resolveDisplayName(info1); got != "Bob(13800001111)" {
		t.Fatalf("resolveDisplayName = %q, want Bob(13800001111)", got)
	}

	// 2. 只有昵称
	info2 := map[string]any{
		"nickname": "Charlie",
	}
	if got := resolveDisplayName(info2); got != "Charlie" {
		t.Fatalf("resolveDisplayName = %q, want Charlie", got)
	}

	// 3. UUID 形态 username 跳过，取 detail.phone
	info3 := map[string]any{
		"username": "8afddf7e-1111-2222-3333-444444444444",
		"detail": map[string]any{
			"mobile": "13912345678",
		},
	}
	if got := resolveDisplayName(info3); got != "13912345678" {
		t.Fatalf("resolveDisplayName = %q, want 13912345678", got)
	}

	// 4. 只有 email 兜底
	info4 := map[string]any{
		"email": "dev@example.com",
	}
	if got := resolveDisplayName(info4); got != "dev@example.com" {
		t.Fatalf("resolveDisplayName = %q, want dev@example.com", got)
	}

	// 5. 只有长 id 截取后 6 位
	info5 := map[string]any{
		"uid": "1234567890abcdef",
	}
	if got := resolveDisplayName(info5); got != "…abcdef" {
		t.Fatalf("resolveDisplayName = %q, want …abcdef", got)
	}
}

func TestAuthLoginStart(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}

	resp, err := handleAuthLoginStart(context.Background(), m, pluginapi.AuthLoginStartRequest{})
	if err != nil {
		t.Fatalf("handleAuthLoginStart: %v", err)
	}
	if resp.Provider != "qwenworkcn" {
		t.Fatalf("Provider = %q, want qwenworkcn", resp.Provider)
	}
	if resp.State == "" {
		t.Fatal("State is empty")
	}

	u, err := url.Parse(resp.URL)
	if err != nil {
		t.Fatalf("invalid AuthURL %q: %v", resp.URL, err)
	}
	q := u.Query()
	if q.Get("nonce") != resp.State {
		t.Fatalf("nonce in query %q != State %q", q.Get("nonce"), resp.State)
	}
	if q.Get("challenge") == "" || q.Get("challenge_method") != "S256" {
		t.Fatalf("invalid challenge in query: %v", q)
	}
	if q.Get("machine_id") == "" {
		t.Fatal("machine_id is empty in query")
	}
	if q.Get("client_id") != m.Profile.ClientID {
		t.Fatalf("client_id in query = %q, want %q", q.Get("client_id"), m.Profile.ClientID)
	}

	// 验证 session 存入注册表
	if _, ok := loginSessions.Load(resp.State); !ok {
		t.Fatal("session not found in loginSessions")
	}
}

func TestAuthLoginPoll(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}

	// 1. 会话不存在
	pollResp, err := handleAuthLoginPoll(context.Background(), m, pluginapi.AuthLoginPollRequest{
		State: "nonexistent-nonce",
	})
	if err != nil {
		t.Fatalf("handleAuthLoginPoll: %v", err)
	}
	if pollResp.Status != pluginapi.AuthLoginStatusError {
		t.Fatalf("status = %s, want error", pollResp.Status)
	}

	// 模拟上游网关
	var pollStatus int
	var pollBody string
	var userinfoStatus int
	var userinfoBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/deviceToken/poll":
			w.WriteHeader(pollStatus)
			_, _ = w.Write([]byte(pollBody))
		case "/api/v1/userinfo":
			w.WriteHeader(userinfoStatus)
			_, _ = w.Write([]byte(userinfoBody))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// 临时修改 httpClient 与 baseUrl
	oldClient := httpClient
	httpClient = server.Client()
	defer func() { httpClient = oldClient }()

	testManifest := *m
	testManifest.Endpoints.BaseURL = server.URL

	// 发起一次 start 建立合法 session
	startResp, err := handleAuthLoginStart(context.Background(), &testManifest, pluginapi.AuthLoginStartRequest{})
	if err != nil {
		t.Fatalf("start login: %v", err)
	}

	// 2. 模拟 404 Pending (用户尚未完成网页授权)
	pollStatus = http.StatusNotFound
	pollBody = `{"code":"NOT_FOUND"}`
	pollResp, err = handleAuthLoginPoll(context.Background(), &testManifest, pluginapi.AuthLoginPollRequest{
		State: startResp.State,
	})
	if err != nil {
		t.Fatalf("poll pending: %v", err)
	}
	if pollResp.Status != pluginapi.AuthLoginStatusPending {
		t.Fatalf("status = %s, want pending", pollResp.Status)
	}

	// 3. 模拟 200 Success
	pollStatus = http.StatusOK
	pollBody = `{"token":"device-tok-abc","refresh_token":"ref-tok-xyz","expires_in":7200}`
	userinfoStatus = http.StatusOK
	userinfoBody = `{"id":"9876543210","nickname":"Tester","phone":"13800002222"}`

	pollResp, err = handleAuthLoginPoll(context.Background(), &testManifest, pluginapi.AuthLoginPollRequest{
		State: startResp.State,
	})
	if err != nil {
		t.Fatalf("poll success: %v", err)
	}
	if pollResp.Status != pluginapi.AuthLoginStatusSuccess {
		t.Fatalf("status = %s, want success (msg=%s)", pollResp.Status, pollResp.Message)
	}
	if pollResp.Auth.ID != "9876543210" {
		t.Fatalf("Auth.ID = %q, want 9876543210", pollResp.Auth.ID)
	}
	if pollResp.Auth.Label != "Tester(13800002222)" {
		t.Fatalf("Auth.Label = %q, want Tester(13800002222)", pollResp.Auth.Label)
	}

	// 验证成功后 session 从注册表删除
	if _, ok := loginSessions.Load(startResp.State); ok {
		t.Fatal("session should be removed from loginSessions after success")
	}
}

func TestAuthRefresh(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}

	var refreshStatus int
	var refreshBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/deviceToken/refresh" {
			w.WriteHeader(refreshStatus)
			_, _ = w.Write([]byte(refreshBody))
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

	initialCred := Credential{
		UserID:      "u1",
		DisplayName: "User1",
		Endpoint:    server.URL,
		MachineID:   "m1",
		Credentials: TokenCredentials{
			Access:  "old-access",
			Refresh: "old-refresh",
			Expires: 1700000000000,
		},
	}
	storageJSON, _ := initialCred.ToStorageJSON()

	// 1. 成功刷新
	refreshStatus = http.StatusOK
	refreshBody = `{"device_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`

	resp, err := handleAuthRefresh(context.Background(), &testManifest, pluginapi.AuthRefreshRequest{
		AuthID:      "auth-1",
		StorageJSON: storageJSON,
	})
	if err != nil {
		t.Fatalf("handleAuthRefresh: %v", err)
	}

	refreshedCred, err := parseCredential(resp.Auth.StorageJSON)
	if err != nil {
		t.Fatalf("parse refreshed storage json: %v", err)
	}
	if refreshedCred.AccessToken() != "new-access" {
		t.Fatalf("new access = %q, want new-access", refreshedCred.AccessToken())
	}
	if refreshedCred.RefreshToken() != "new-refresh" {
		t.Fatalf("new refresh = %q, want new-refresh", refreshedCred.RefreshToken())
	}

	// 2. 失败报错 (401)
	refreshStatus = http.StatusUnauthorized
	refreshBody = `{"code":"INVALID_TOKEN"}`
	if _, err := handleAuthRefresh(context.Background(), &testManifest, pluginapi.AuthRefreshRequest{
		AuthID:      "auth-1",
		StorageJSON: storageJSON,
	}); err == nil {
		t.Fatal("expected error on 401, got nil")
	}
}

func TestAuthParse(t *testing.T) {
	valid := `{"credentials":{"access":"a","refresh":"r"},"machineId":"m"}`
	resp, err := handleAuthParse(pluginapi.AuthParseRequest{
		FileName: "qwenworkcn-test.json",
		RawJSON:  []byte(valid),
	})
	if err != nil || !resp.Handled {
		t.Fatalf("handleAuthParse valid: Handled=%v, err=%v", resp.Handled, err)
	}

	invalid := `{"bad":"json"`
	resp, err = handleAuthParse(pluginapi.AuthParseRequest{
		FileName: "qwenworkcn-test.json",
		RawJSON:  []byte(invalid),
	})
	if err != nil || resp.Handled {
		t.Fatalf("handleAuthParse invalid: Handled=%v, err=%v", resp.Handled, err)
	}
}
