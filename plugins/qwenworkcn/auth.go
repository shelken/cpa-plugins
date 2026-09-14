package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var httpClient = &http.Client{
	Timeout: 45 * time.Second,
}

var uuidRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type loginSession struct {
	Verifier  string
	MachineID string
	CreatedAt time.Time
	failures  int
}

var loginSessions sync.Map

func generatePKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

func handleAuthParse(req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	cred, err := parseCredential(req.RawJSON)
	if err != nil {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}

	authData, err := cred.ToAuthData(req.FileName)
	if err != nil {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}

	return pluginapi.AuthParseResponse{
		Handled: true,
		Auth:    authData,
	}, nil
}

func handleAuthLoginStart(ctx context.Context, manifest *ManifestV2, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("generate pkce: %w", err)
	}

	nonce := uuid.NewString()
	machineID := uuid.NewString()

	authBase := manifest.Endpoints.BaseURL
	u, err := url.Parse(strings.TrimRight(authBase, "/") + "/device/selectAccounts")
	if err != nil {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("parse auth base url: %w", err)
	}

	// 参数顺序严格对齐客户端实测授权 URL: nonce -> challenge -> challenge_method -> redirect_uri -> machine_id -> client_id
	q := url.Values{}
	q.Set("nonce", nonce)
	q.Set("challenge", challenge)
	q.Set("challenge_method", "S256")
	if manifest.Profile.RedirectURI != "" {
		q.Set("redirect_uri", manifest.Profile.RedirectURI)
	}
	q.Set("machine_id", machineID)
	if manifest.Profile.ClientID != "" {
		q.Set("client_id", manifest.Profile.ClientID)
	}
	u.RawQuery = q.Encode()

	loginSessions.Store(nonce, &loginSession{
		Verifier:  verifier,
		MachineID: machineID,
		CreatedAt: nowFunc(),
	})

	return pluginapi.AuthLoginStartResponse{
		Provider:  "qwenworkcn",
		URL:       u.String(),
		State:     nonce,
		ExpiresAt: nowFunc().Add(5 * time.Minute),
	}, nil
}

func handleAuthLoginPoll(ctx context.Context, manifest *ManifestV2, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	val, ok := loginSessions.Load(req.State)
	if !ok {
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "会话已过期，请重新发起登录",
		}, nil
	}
	session := val.(*loginSession)

	headers, err := manifest.RenderHeaderGroup("login.poll", map[string]string{
		"userAgent": "qoderwork/" + manifest.Profile.CosyVersion,
	})
	if err != nil {
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: err.Error(),
		}, nil
	}

	pollURL, err := url.Parse(manifest.BuildURL(manifest.Endpoints.DeviceTokenPoll))
	if err != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: err.Error()}, nil
	}
	q := pollURL.Query()
	q.Set("nonce", req.State)
	q.Set("verifier", session.Verifier)
	q.Set("challenge_method", "S256")
	pollURL.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL.String(), nil)
	if err != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: err.Error()}, nil
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		session.failures++
		if session.failures >= 5 {
			return pluginapi.AuthLoginPollResponse{
				Status:  pluginapi.AuthLoginStatusError,
				Message: fmt.Sprintf("deviceToken/poll 连续 %d 次网络失败: %v", session.failures, err),
			}, nil
		}
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusPending,
			Message: "网络暂时抖动，正在重试...",
		}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		session.failures = 0
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusPending,
			Message: "等待用户在浏览器中完成授权",
		}, nil
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: err.Error()}, nil
	}

	if resp.StatusCode != http.StatusOK {
		session.failures++
		if session.failures >= 5 {
			return pluginapi.AuthLoginPollResponse{
				Status:  pluginapi.AuthLoginStatusError,
				Message: fmt.Sprintf("deviceToken/poll 连续 %d 次失败，最后 HTTP %d", session.failures, resp.StatusCode),
			}, nil
		}
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusPending,
			Message: fmt.Sprintf("等待用户授权 (HTTP %d)", resp.StatusCode),
		}, nil
	}

	session.failures = 0
	var pollResp map[string]any
	if err := json.Unmarshal(respBytes, &pollResp); err != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "解析 poll 响应失败: " + err.Error()}, nil
	}

	var token, refreshToken string
	if t, ok := pollResp["token"].(string); ok && t != "" {
		token = t
	} else if t, ok := pollResp["device_token"].(string); ok && t != "" {
		token = t
	}
	if r, ok := pollResp["refresh_token"].(string); ok && r != "" {
		refreshToken = r
	}

	if token == "" || refreshToken == "" {
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "deviceToken/poll 200 缺 token/refresh_token 字段",
		}, nil
	}

	expMs, _ := parseAccessExpiresAt(pollResp, nowFunc())
	expires := credentialExpiresAt(expMs, nowFunc())

	userinfoHeaders, err := manifest.RenderHeaderGroup("login.userinfo", map[string]string{
		"deviceToken": token,
		"clientType":  "6",
		"machineId":   session.MachineID,
	})
	if err != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: err.Error()}, nil
	}

	userinfoURL := manifest.BuildURL(manifest.Endpoints.Userinfo)
	uiReq, err := http.NewRequestWithContext(ctx, http.MethodGet, userinfoURL, nil)
	if err != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: err.Error()}, nil
	}
	for k, v := range userinfoHeaders {
		uiReq.Header.Set(k, v)
	}

	uiResp, err := httpClient.Do(uiReq)
	if err != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "请求 userinfo 失败: " + err.Error()}, nil
	}
	defer uiResp.Body.Close()

	uiBytes, err := io.ReadAll(uiResp.Body)
	if err != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "读取 userinfo 失败: " + err.Error()}, nil
	}
	if uiResp.StatusCode != http.StatusOK {
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: fmt.Sprintf("userinfo failed: HTTP %d (%s)", uiResp.StatusCode, string(uiBytes)),
		}, nil
	}

	var uiMap map[string]any
	if err := json.Unmarshal(uiBytes, &uiMap); err != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "解析 userinfo 响应失败: " + err.Error()}, nil
	}

	var uid string
	if id, ok := uiMap["id"].(string); ok && id != "" {
		uid = id
	} else if id, ok := uiMap["uid"].(string); ok && id != "" {
		uid = id
	} else if id, ok := uiMap["id"].(float64); ok {
		uid = fmt.Sprintf("%.0f", id)
	} else if id, ok := uiMap["uid"].(float64); ok {
		uid = fmt.Sprintf("%.0f", id)
	}

	if uid == "" {
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "userinfo 响应缺 id/uid",
		}, nil
	}

	displayName := resolveDisplayName(uiMap)

	cred := &Credential{
		UserID:      uid,
		DisplayName: displayName,
		Endpoint:    manifest.Endpoints.BaseURL,
		MachineID:   session.MachineID,
		Credentials: TokenCredentials{
			Access:  token,
			Refresh: refreshToken,
			Expires: expires,
		},
		extra: uiMap,
	}

	fileName := fmt.Sprintf("qwenworkcn-%s.json", uid)

	authData, err := cred.ToAuthData(fileName)
	if err != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "构建 auth data 失败: " + err.Error()}, nil
	}

	loginSessions.Delete(req.State)

	return pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusSuccess,
		Auth:    authData,
		Message: "登录成功",
	}, nil
}

func handleAuthRefresh(ctx context.Context, manifest *ManifestV2, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	cred, err := parseCredential(req.StorageJSON)
	if err != nil {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("parse storage json for refresh: %w", err)
	}

	headers, err := manifest.RenderHeaderGroup("login.refresh", map[string]string{
		"userAgent": "qoderwork/" + manifest.Profile.CosyVersion,
	})
	if err != nil {
		return pluginapi.AuthRefreshResponse{}, err
	}

	refreshURL := manifest.BuildURL(manifest.Endpoints.DeviceTokenRefresh)
	bodyMap := map[string]string{"refresh_token": cred.RefreshToken()}
	bodyBytes, _ := json.Marshal(bodyMap)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, refreshURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("create refresh request: %w", err)
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("execute refresh request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("deviceToken/refresh failed: HTTP %d (body: %s)", resp.StatusCode, string(respBytes))
	}

	var respMap map[string]any
	if err := json.Unmarshal(respBytes, &respMap); err != nil {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("unmarshal refresh response: %w", err)
	}

	var token string
	if t, ok := respMap["device_token"].(string); ok && t != "" {
		token = t
	} else if t, ok := respMap["token"].(string); ok && t != "" {
		token = t
	}

	var refreshToken string
	if r, ok := respMap["refresh_token"].(string); ok && r != "" {
		refreshToken = r
	}

	if token == "" || refreshToken == "" {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("deviceToken/refresh 200 缺 device_token/refresh_token 字段")
	}

	cred.Credentials.Access = token
	cred.Credentials.Refresh = refreshToken

	expMs, _ := parseAccessExpiresAt(respMap, nowFunc())
	cred.Credentials.Expires = credentialExpiresAt(expMs, nowFunc())

	newAuthData, err := cred.ToAuthData(req.AuthID)
	if err != nil {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("create refreshed auth data: %w", err)
	}

	return pluginapi.AuthRefreshResponse{
		Auth:             newAuthData,
		NextRefreshAfter: cred.NextRefreshAfter(),
	}, nil
}

func isUUIDLike(s string) bool {
	return uuidRegex.MatchString(strings.TrimSpace(s))
}

func firstNick(info map[string]any) string {
	for _, k := range []string{"nickname", "name", "username"} {
		if v, ok := info[k].(string); ok && strings.TrimSpace(v) != "" && !isUUIDLike(v) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func asPhone(v any) string {
	switch val := v.(type) {
	case string:
		if strings.TrimSpace(val) != "" {
			return strings.TrimSpace(val)
		}
	case float64:
		return fmt.Sprintf("%.0f", val)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	}
	return ""
}

func phoneOf(info map[string]any) string {
	if detail, ok := info["detail"].(map[string]any); ok {
		for _, k := range []string{"phone", "mobile", "mobile_phone", "phone_number", "masked_phone"} {
			if p := asPhone(detail[k]); p != "" {
				return p
			}
		}
	}
	for _, k := range []string{"phone", "mobile", "mobile_phone", "phone_number", "masked_phone"} {
		if p := asPhone(info[k]); p != "" {
			return p
		}
	}
	return ""
}

func idTail(info map[string]any) string {
	var rawID string
	if v, ok := info["id"].(string); ok && v != "" {
		rawID = v
	} else if v, ok := info["uid"].(string); ok && v != "" {
		rawID = v
	} else if v, ok := info["id"].(float64); ok {
		rawID = fmt.Sprintf("%.0f", v)
	} else if v, ok := info["uid"].(float64); ok {
		rawID = fmt.Sprintf("%.0f", v)
	}

	if rawID != "" {
		if len(rawID) <= 8 {
			return rawID
		}
		return "…" + rawID[len(rawID)-6:]
	}
	return "user"
}

func resolveDisplayName(info map[string]any) string {
	nick := firstNick(info)
	phone := phoneOf(info)
	if nick != "" && phone != "" {
		return fmt.Sprintf("%s(%s)", nick, phone)
	}
	if nick != "" {
		return nick
	}
	if phone != "" {
		return phone
	}
	if email, ok := info["email"].(string); ok && strings.TrimSpace(email) != "" {
		return strings.TrimSpace(email)
	}
	return idTail(info)
}
