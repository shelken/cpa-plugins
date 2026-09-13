package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var httpClient = &http.Client{
	Timeout: 45 * time.Second,
}

func generateRequestID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}

type authStateResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
	} `json:"data"`
}

type authTokenResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		AccessToken      string `json:"accessToken"`
		RefreshToken     string `json:"refreshToken"`
		ExpiresIn        int64  `json:"expiresIn"`
		RefreshExpiresIn int64  `json:"refreshExpiresIn"`
		TokenType        string `json:"tokenType"`
		SessionState     string `json:"sessionState"`
		Scope            string `json:"scope"`
		Domain           string `json:"domain"`
	} `json:"data"`
}

type loginAccountResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		UID      string `json:"uid"`
		Nickname string `json:"nickname"`
	} `json:"data"`
}

type accountsResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Accounts []struct {
			UID       string `json:"uid"`
			Nickname  string `json:"nickname"`
			LastLogin bool   `json:"lastLogin"`
		} `json:"accounts"`
	} `json:"data"`
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

func handleAuthRefresh(ctx context.Context, manifest *ManifestV2, cfg *PluginConfig, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	cred, err := parseCredential(req.StorageJSON)
	if err != nil {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("parse storage json for refresh: %w", err)
	}

	profileName := cfg.IdentityProfile
	if profileName == "" {
		profileName = string(ProfileDesktop)
	}
	profile, err := manifest.GetProfile(profileName)
	if err != nil {
		return pluginapi.AuthRefreshResponse{}, err
	}

	refreshURL := manifest.BuildURL(manifest.Endpoints.TokenRefresh)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, refreshURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("create refresh request: %w", err)
	}

	authHeaders := profile.Headers["auth"]
	for k, v := range authHeaders {
		httpReq.Header.Set(k, v)
	}

	httpReq.Header.Set("Authorization", "Bearer "+cred.AccessToken())
	httpReq.Header.Set("X-Refresh-Token", cred.RefreshToken())
	httpReq.Header.Set("X-Auth-Refresh-Source", "plugin")
	httpReq.Header.Set("X-Request-ID", generateRequestID())
	httpReq.Header.Set("X-User-Id", cred.UserID)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("execute refresh request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("read refresh response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("refresh upstream returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var refreshResp authTokenResponse
	if err := json.Unmarshal(bodyBytes, &refreshResp); err != nil {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("unmarshal refresh response: %w", err)
	}

	if refreshResp.Code != 0 {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("refresh upstream error (code %d): %s", refreshResp.Code, refreshResp.Msg)
	}

	if refreshResp.Data.AccessToken == "" {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("refresh upstream returned empty access token")
	}

	cred.Credentials.Access = refreshResp.Data.AccessToken
	if refreshResp.Data.RefreshToken != "" {
		cred.Credentials.Refresh = refreshResp.Data.RefreshToken
	}
	if refreshResp.Data.ExpiresIn > 0 {
		cred.Credentials.Expires = time.Now().UnixMilli() + refreshResp.Data.ExpiresIn*1000
	}
	if refreshResp.Data.SessionState != "" {
		cred.SessionState = refreshResp.Data.SessionState
	}
	if refreshResp.Data.Scope != "" {
		cred.Scope = refreshResp.Data.Scope
	}
	if refreshResp.Data.Domain != "" {
		cred.Domain = refreshResp.Data.Domain
	}

	newAuthData, err := cred.ToAuthData(req.AuthID)
	if err != nil {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("create refreshed auth data: %w", err)
	}

	return pluginapi.AuthRefreshResponse{
		Auth:             newAuthData,
		NextRefreshAfter: cred.NextRefreshAfter(),
	}, nil
}

func handleAuthLoginStart(ctx context.Context, manifest *ManifestV2, cfg *PluginConfig, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	loginProfileName := cfg.LoginProfile
	if loginProfileName == "" {
		loginProfileName = string(ProfileDesktop)
	}
	profile, err := manifest.GetProfile(loginProfileName)
	if err != nil {
		return pluginapi.AuthLoginStartResponse{}, err
	}

	platform := profile.Login.Platform
	if platform == "" {
		if loginProfileName == string(ProfileCLI) {
			platform = "cli"
		} else {
			platform = "workbuddy"
		}
	}

	stateURL := manifest.BuildURL(manifest.Endpoints.AuthState) + "?platform=" + url.QueryEscape(platform)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, stateURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("create login state request: %w", err)
	}

	for k, v := range profile.Headers["auth"] {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("X-No-Authorization", "true")
	httpReq.Header.Set("X-No-User-Id", "true")
	httpReq.Header.Set("X-No-Enterprise-Id", "true")
	httpReq.Header.Set("X-No-Department-Info", "true")
	httpReq.Header.Set("X-Product", "SaaS")
	httpReq.Header.Set("X-Domain", "copilot.tencent.com")
	httpReq.Header.Set("X-Request-ID", generateRequestID())
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("execute login state request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("read login state response: %w", err)
	}

	var stateResp authStateResponse
	if err := json.Unmarshal(bodyBytes, &stateResp); err != nil {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("unmarshal login state response: %w", err)
	}

	if stateResp.Code != 0 {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("login state request failed (code %d): %s", stateResp.Code, stateResp.Msg)
	}
	if stateResp.Data.State == "" || stateResp.Data.AuthURL == "" {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("login state response missing state or authUrl")
	}

	return pluginapi.AuthLoginStartResponse{
		Provider:  "workbuddy",
		URL:       stateResp.Data.AuthURL,
		State:     stateResp.Data.State,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}, nil
}

func handleAuthLoginPoll(ctx context.Context, manifest *ManifestV2, cfg *PluginConfig, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	loginProfileName := cfg.LoginProfile
	if loginProfileName == "" {
		loginProfileName = string(ProfileDesktop)
	}
	profile, err := manifest.GetProfile(loginProfileName)
	if err != nil {
		return pluginapi.AuthLoginPollResponse{}, err
	}

	tokenURL := manifest.BuildURL(manifest.Endpoints.AuthToken) + "?state=" + url.QueryEscape(req.State)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return pluginapi.AuthLoginPollResponse{}, fmt.Errorf("create login poll request: %w", err)
	}

	for k, v := range profile.Headers["auth"] {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("X-No-Authorization", "true")
	httpReq.Header.Set("X-No-User-Id", "true")
	httpReq.Header.Set("X-No-Enterprise-Id", "true")
	httpReq.Header.Set("X-No-Department-Info", "true")
	httpReq.Header.Set("X-Product", "SaaS")
	httpReq.Header.Set("X-Domain", "copilot.tencent.com")
	httpReq.Header.Set("X-Request-ID", generateRequestID())
	httpReq.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return pluginapi.AuthLoginPollResponse{}, fmt.Errorf("execute login poll request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return pluginapi.AuthLoginPollResponse{}, fmt.Errorf("read login poll response: %w", err)
	}

	var pollResp authTokenResponse
	if err := json.Unmarshal(bodyBytes, &pollResp); err != nil {
		return pluginapi.AuthLoginPollResponse{}, fmt.Errorf("unmarshal login poll response: %w", err)
	}

	if pollResp.Code == 11217 || strings.Contains(strings.ToLower(pollResp.Msg), "login ing") {
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusPending,
			Message: "login in progress",
		}, nil
	}

	if pollResp.Code != 0 {
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: fmt.Sprintf("login error (code %d): %s", pollResp.Code, pollResp.Msg),
		}, nil
	}

	if pollResp.Data.AccessToken == "" {
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "empty access token received from login endpoint",
		}, nil
	}

	uid, nickname := fetchAccountInfo(ctx, manifest, profile, pollResp.Data.AccessToken, req.State)
	if uid == "" {
		jwtUID, jwtName := extractClaimsFromJWT(pollResp.Data.AccessToken)
		uid = jwtUID
		if nickname == "" {
			nickname = jwtName
		}
	}
	if uid == "" {
		uid = "workbuddy-user"
	}
	if nickname == "" {
		nickname = uid
	}

	domain := pollResp.Data.Domain
	if domain == "" {
		domain = "www.workbuddy.cn"
	}

	cred := Credential{
		UserID:      uid,
		DisplayName: nickname,
		Endpoint:    manifest.Endpoints.BaseURL,
		Credentials: TokenCredentials{
			Access:  pollResp.Data.AccessToken,
			Refresh: pollResp.Data.RefreshToken,
			Expires: time.Now().UnixMilli() + pollResp.Data.ExpiresIn*1000,
		},
		SessionState: pollResp.Data.SessionState,
		Scope:        pollResp.Data.Scope,
		Domain:       domain,
	}
	authData, err := cred.ToAuthData(uid + ".json")
	if err != nil {
		return pluginapi.AuthLoginPollResponse{}, fmt.Errorf("create auth data after login: %w", err)
	}

	return pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusSuccess,
		Message: "login successful",
		Auth:    authData,
	}, nil
}

func fetchAccountInfo(ctx context.Context, manifest *ManifestV2, profile *ProfileConfig, accessToken string, state string) (string, string) {
	if manifest.Endpoints.LoginAccount != "" && state != "" {
		loginAccountURL := manifest.BuildURL(manifest.Endpoints.LoginAccount) + "?state=" + url.QueryEscape(state)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, loginAccountURL, nil)
		if err == nil {
			for k, v := range profile.Headers["auth"] {
				req.Header.Set(k, v)
			}
			req.Header.Set("Authorization", "Bearer "+accessToken)
			req.Header.Set("X-Request-ID", generateRequestID())
			req.Header.Set("X-Product", "SaaS")
			req.Header.Set("X-Domain", "copilot.tencent.com")
			req.Header.Set("Accept", "application/json")

			resp, errDo := httpClient.Do(req)
			if errDo == nil {
				defer resp.Body.Close()
				bodyBytes, _ := io.ReadAll(resp.Body)
				var accResp loginAccountResponse
				if json.Unmarshal(bodyBytes, &accResp) == nil && accResp.Code == 0 && accResp.Data.UID != "" {
					return accResp.Data.UID, accResp.Data.Nickname
				}
			}
		}
	}

	if manifest.Endpoints.Accounts != "" {
		accountsURL := manifest.BuildURL(manifest.Endpoints.Accounts)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, accountsURL, nil)
		if err == nil {
			for k, v := range profile.Headers["account"] {
				req.Header.Set(k, v)
			}
			req.Header.Set("Authorization", "Bearer "+accessToken)
			req.Header.Set("X-Request-ID", generateRequestID())
			req.Header.Set("X-Product", "SaaS")
			req.Header.Set("Accept", "application/json")

			resp, errDo := httpClient.Do(req)
			if errDo == nil {
				defer resp.Body.Close()
				bodyBytes, _ := io.ReadAll(resp.Body)
				var accsResp accountsResponse
				if json.Unmarshal(bodyBytes, &accsResp) == nil && accsResp.Code == 0 && len(accsResp.Data.Accounts) > 0 {
					for _, acc := range accsResp.Data.Accounts {
						if acc.LastLogin && acc.UID != "" {
							return acc.UID, acc.Nickname
						}
					}
					if accsResp.Data.Accounts[0].UID != "" {
						return accsResp.Data.Accounts[0].UID, accsResp.Data.Accounts[0].Nickname
					}
				}
			}
		}
	}

	return "", ""
}
