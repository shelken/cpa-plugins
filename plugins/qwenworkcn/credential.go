package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	TokenRefreshBufferMs = 300_000
	AccessTTLFallbackMs  = 3600_000
)

type TokenCredentials struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
	Expires int64  `json:"expires"`
}

type Credential struct {
	UserID      string           `json:"userId"`
	DisplayName string           `json:"displayName"`
	Endpoint    string           `json:"endpoint"`
	MachineID   string           `json:"machineId"`
	Credentials TokenCredentials `json:"credentials"`
	extra       map[string]any   `json:"-"`
}

func parseCredential(raw []byte) (*Credential, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("credential payload is empty")
	}

	var rawMap map[string]any
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		return nil, fmt.Errorf("unmarshal credential json: %w", err)
	}

	var cred Credential
	if err := json.Unmarshal(raw, &cred); err != nil {
		return nil, fmt.Errorf("decode credential struct: %w", err)
	}

	knownKeys := map[string]struct{}{
		"userId":      {},
		"displayName": {},
		"endpoint":    {},
		"machineId":   {},
		"credentials": {},
	}
	extra := make(map[string]any)
	for k, v := range rawMap {
		if _, ok := knownKeys[k]; !ok {
			extra[k] = v
		}
	}
	cred.extra = extra

	if strings.TrimSpace(cred.Credentials.Access) == "" {
		return nil, fmt.Errorf("access token not found in credential")
	}
	if strings.TrimSpace(cred.Credentials.Refresh) == "" {
		return nil, fmt.Errorf("refresh token not found in credential")
	}
	if strings.TrimSpace(cred.MachineID) == "" {
		return nil, fmt.Errorf("machineId not found in credential")
	}

	if cred.UserID == "" {
		cred.UserID = "qwenworkcn-user"
	}
	if cred.DisplayName == "" {
		cred.DisplayName = cred.UserID
	}
	if cred.Endpoint == "" {
		cred.Endpoint = "https://gateway.qwenwork.cn"
	}

	return &cred, nil
}

func (c *Credential) AccessToken() string {
	return c.Credentials.Access
}

func (c *Credential) RefreshToken() string {
	return c.Credentials.Refresh
}

func (c *Credential) Expires() int64 {
	return c.Credentials.Expires
}

func (c *Credential) NextRefreshAfter() time.Time {
	if c.Credentials.Expires > 0 {
		expTime := time.UnixMilli(c.Credentials.Expires)
		return expTime.Add(-5 * time.Minute)
	}
	return nowFunc().Add(5 * time.Minute)
}

func (c *Credential) IsExpired() bool {
	if c.Credentials.Expires <= 0 {
		return false
	}
	return nowFunc().UnixMilli() >= c.Credentials.Expires
}

func (c *Credential) ToStorageJSON() ([]byte, error) {
	out := make(map[string]any)
	for k, v := range c.extra {
		out[k] = v
	}

	out["userId"] = c.UserID
	out["displayName"] = c.DisplayName
	out["endpoint"] = c.Endpoint
	out["machineId"] = c.MachineID
	out["credentials"] = map[string]any{
		"access":  c.Credentials.Access,
		"refresh": c.Credentials.Refresh,
		"expires": c.Credentials.Expires,
	}

	return json.Marshal(out)
}

func (c *Credential) ToAuthData(fileName string) (pluginapi.AuthData, error) {
	storageJSON, err := c.ToStorageJSON()
	if err != nil {
		return pluginapi.AuthData{}, fmt.Errorf("serialize storage json: %w", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(storageJSON, &metadata); err != nil {
		return pluginapi.AuthData{}, fmt.Errorf("decode credential metadata: %w", err)
	}
	// 宿主保存时 Metadata 优先于 StorageJSON，必须携带本次登录的凭据。
	metadata["type"] = "qwenworkcn"
	// 插件没有宿主内置的 refresh lead，以实际剩余有效期声明刷新周期。
	metadata["refresh_interval_seconds"] = int64(300)
	if c.Credentials.Expires > 0 {
		metadata["refresh_interval_seconds"] = max(int64(1), (c.Credentials.Expires-nowFunc().UnixMilli())/2000)
		metadata["expires_at"] = time.UnixMilli(c.Credentials.Expires).UTC().Format(time.RFC3339Nano)
	}

	id := c.UserID
	if id == "" {
		id = "qwenworkcn-default"
	}
	label := c.DisplayName
	if label == "" {
		label = id
	}

	return pluginapi.AuthData{
		Provider:         "qwenworkcn",
		ID:               id,
		FileName:         fileName,
		Label:            label,
		StorageJSON:      storageJSON,
		NextRefreshAfter: c.NextRefreshAfter(),
		Metadata:         metadata,
	}, nil
}

// stringList 读 JSON 解码后的字符串数组: 经 map[string]any 传下来的数组是 []any, 断言成 []string 必失败。
func stringList(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func asFiniteNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

// parseAccessExpiresAt 从 poll/refresh 返回的 body 中解析 access token 过期时刻 (epoch ms)。
// 优先 expires_in；expires_at 若距今 >24h 视为 refresh 过期（qoder 常见特征），忽略。
func parseAccessExpiresAt(body map[string]any, now time.Time) (int64, bool) {
	if body == nil {
		return 0, false
	}
	nowMs := now.UnixMilli()

	if v, ok := body["expires_in"]; ok {
		if num, valid := asFiniteNumber(v); valid && num > 0 {
			return nowMs + int64(num*1000), true
		}
	}

	var raw any
	if v, ok := body["expires_at"]; ok {
		raw = v
	} else if v, ok := body["expire_time"]; ok {
		raw = v
	}

	if raw == nil {
		return 0, false
	}

	var ms int64
	if num, valid := asFiniteNumber(raw); valid {
		if num < 1e12 {
			ms = int64(num * 1000)
		} else {
			ms = int64(num)
		}
	} else if str, ok := raw.(string); ok && strings.TrimSpace(str) != "" {
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(str))
		if err != nil {
			return 0, false
		}
		ms = t.UnixMilli()
	} else {
		return 0, false
	}

	// 若距今超过 24 小时，说明是 refresh_token 的长效过期时间，不能作为 access_token 的过期时间
	if ms-nowMs > 24*3600_000 {
		return 0, false
	}
	return ms, true
}

func credentialExpiresAt(accessExpiresAt int64, now time.Time) int64 {
	var raw int64
	nowMs := now.UnixMilli()
	if accessExpiresAt > 0 {
		raw = accessExpiresAt
	} else {
		raw = nowMs + AccessTTLFallbackMs
	}
	buffered := raw - TokenRefreshBufferMs
	minExpires := nowMs + 5000
	if buffered < minExpires {
		return minExpires
	}
	return buffered
}
