package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type TokenCredentials struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
	Expires int64  `json:"expires"`
}

type Credential struct {
	UserID       string           `json:"userId"`
	DisplayName  string           `json:"displayName"`
	Endpoint     string           `json:"endpoint"`
	Credentials  TokenCredentials `json:"credentials"`
	SessionState string           `json:"sessionState,omitempty"`
	Scope        string           `json:"scope,omitempty"`
	Domain       string           `json:"domain,omitempty"`
	extra        map[string]any   `json:"-"`
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
		"userId":       {},
		"displayName":  {},
		"endpoint":     {},
		"credentials":  {},
		"sessionState": {},
		"scope":        {},
		"domain":       {},
	}
	extra := make(map[string]any)
	for k, v := range rawMap {
		if _, ok := knownKeys[k]; !ok {
			extra[k] = v
		}
	}
	cred.extra = extra

	if cred.Credentials.Access == "" {
		return nil, fmt.Errorf("access token not found in credential")
	}

	if cred.UserID == "" || cred.DisplayName == "" {
		jwtUID, jwtName := extractClaimsFromJWT(cred.Credentials.Access)
		if cred.UserID == "" {
			cred.UserID = jwtUID
		}
		if cred.DisplayName == "" {
			cred.DisplayName = jwtName
		}
	}

	if cred.UserID == "" {
		cred.UserID = "workbuddy-user"
	}
	if cred.DisplayName == "" {
		cred.DisplayName = cred.UserID
	}
	if cred.Endpoint == "" {
		cred.Endpoint = "https://copilot.tencent.com"
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

func (c *Credential) ToStorageJSON() ([]byte, error) {
	out := make(map[string]any)
	for k, v := range c.extra {
		out[k] = v
	}

	out["userId"] = c.UserID
	out["displayName"] = c.DisplayName
	out["endpoint"] = c.Endpoint
	out["credentials"] = map[string]any{
		"access":  c.Credentials.Access,
		"refresh": c.Credentials.Refresh,
		"expires": c.Credentials.Expires,
	}

	if c.SessionState != "" {
		out["sessionState"] = c.SessionState
	}
	if c.Scope != "" {
		out["scope"] = c.Scope
	}
	if c.Domain != "" {
		out["domain"] = c.Domain
	}

	return json.Marshal(out)
}

func (c *Credential) NextRefreshAfter() time.Time {
	if c.Credentials.Expires > 0 {
		expTime := time.UnixMilli(c.Credentials.Expires)
		return expTime.Add(-5 * time.Minute)
	}
	return time.Now().Add(5 * time.Minute)
}

func (c *Credential) IsExpired() bool {
	if c.Credentials.Expires <= 0 {
		return false
	}
	return time.Now().UnixMilli() >= c.Credentials.Expires
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
	metadata["type"] = "workbuddy"
	metadata["sessionState"] = c.SessionState
	metadata["scope"] = c.Scope
	metadata["domain"] = c.Domain
	// 插件没有宿主内置的 refresh lead，以实际剩余有效期声明刷新周期。
	metadata["refresh_interval_seconds"] = int64(300)
	if c.Credentials.Expires > 0 {
		metadata["refresh_interval_seconds"] = max(int64(1), (c.Credentials.Expires-time.Now().UnixMilli())/2000)
		metadata["expires_at"] = time.UnixMilli(c.Credentials.Expires).UTC().Format(time.RFC3339Nano)
	}

	id := c.UserID
	if id == "" {
		id = "workbuddy-default"
	}
	label := c.DisplayName
	if label == "" {
		label = id
	}

	return pluginapi.AuthData{
		Provider:         "workbuddy",
		ID:               id,
		FileName:         fileName,
		Label:            label,
		StorageJSON:      storageJSON,
		NextRefreshAfter: c.NextRefreshAfter(),
		Metadata:         metadata,
	}, nil
}

func extractClaimsFromJWT(token string) (string, string) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", ""
	}

	payloadSegment := parts[1]
	switch len(payloadSegment) % 4 {
	case 2:
		payloadSegment += "=="
	case 3:
		payloadSegment += "="
	}

	data, err := base64.URLEncoding.DecodeString(payloadSegment)
	if err != nil {
		data, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return "", ""
		}
	}

	var claims map[string]any
	if err := json.Unmarshal(data, &claims); err != nil {
		return "", ""
	}

	uidCandidates := []string{"sub", "uid", "user_id", "userId"}
	var uid string
	for _, key := range uidCandidates {
		if val, ok := claims[key].(string); ok && strings.TrimSpace(val) != "" {
			uid = strings.TrimSpace(val)
			break
		}
	}

	nameCandidates := []string{"nickname", "name", "preferred_username", "email"}
	var name string
	for _, key := range nameCandidates {
		if val, ok := claims[key].(string); ok && strings.TrimSpace(val) != "" {
			name = strings.TrimSpace(val)
			break
		}
	}

	return uid, name
}
