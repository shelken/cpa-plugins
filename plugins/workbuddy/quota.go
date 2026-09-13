package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 形状照抄 data/static-config.json requestBodies.userResource 抓包 (客户端 5.3.14, generatedAt 2026-09-13)。
// 字段必须是数字/数组, 不含 OnlyValidPeriod; 发送字符串会让上游 400 (cannot unmarshal string into bool/int)。
type quotaRequestPayload struct {
	PageNumber  int    `json:"PageNumber"`
	PageSize    int    `json:"PageSize"`
	ProductCode string `json:"ProductCode"`
	Status      []int  `json:"Status"`
}

type flexFloat float64

func (f *flexFloat) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*f = 0
		return nil
	}

	if len(trimmed) >= 2 && trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
		s := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
		if s == "" {
			*f = 0
			return nil
		}
		val, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("parse flexFloat from string %q: %w", s, err)
		}
		*f = flexFloat(val)
		return nil
	}

	var num float64
	if err := json.Unmarshal(data, &num); err != nil {
		return fmt.Errorf("unmarshal flexFloat from number %s: %w", string(data), err)
	}
	*f = flexFloat(num)
	return nil
}

func (f *flexFloat) Float64() float64 {
	if f == nil {
		return 0
	}
	return float64(*f)
}

type quotaAccount struct {
	CycleCapacityUsedPrecise   *flexFloat `json:"CycleCapacityUsedPrecise"`
	CycleCapacityUsed          *flexFloat `json:"CycleCapacityUsed"`
	CycleCapacitySizePrecise   *flexFloat `json:"CycleCapacitySizePrecise"`
	CycleCapacitySize          *flexFloat `json:"CycleCapacitySize"`
	CycleCapacityRemainPrecise *flexFloat `json:"CycleCapacityRemainPrecise"`
	CycleCapacityRemain        *flexFloat `json:"CycleCapacityRemain"`
	CapacityUsedPrecise        *flexFloat `json:"CapacityUsedPrecise"`
	CapacityUsed               *flexFloat `json:"CapacityUsed"`
	CapacitySizePrecise        *flexFloat `json:"CapacitySizePrecise"`
	CapacitySize               *flexFloat `json:"CapacitySize"`
}

type quotaResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Response struct {
			Data struct {
				Accounts []quotaAccount `json:"Accounts"`
			} `json:"Data"`
		} `json:"Response"`
	} `json:"data"`
	Response struct {
		Data struct {
			Accounts []quotaAccount `json:"Accounts"`
		} `json:"Data"`
	} `json:"Response"`
}

func handleQuotaDescribe() pluginapi.QuotaDescribeResponse {
	return pluginapi.QuotaDescribeResponse{
		SupportedProviders: []string{"workbuddy"},
		DisplayName:        "WorkBuddy",
		SupportsReset:      false,
	}
}

func handleQuotaFetch(ctx context.Context, manifest *ManifestV2, cfg *PluginConfig, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
	cred, err := parseCredential(req.StorageJSON)
	if err != nil {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("parse storage json for quota fetch: %w", err)
	}

	profileName := cfg.IdentityProfile
	if profileName == "" {
		profileName = string(ProfileDesktop)
	}
	profile, err := manifest.GetProfile(profileName)
	if err != nil {
		return pluginapi.QuotaFetchResponse{}, err
	}

	quotaReqBody := quotaRequestPayload{
		PageNumber:  1,
		PageSize:    100,
		ProductCode: "p_tcaca",
		Status:      []int{0, 3},
	}

	rawBody, err := json.Marshal(quotaReqBody)
	if err != nil {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("marshal quota request body: %w", err)
	}

	quotaURL := manifest.BuildURL(manifest.Endpoints.UserResource)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, quotaURL, bytes.NewReader(rawBody))
	if err != nil {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("create quota request: %w", err)
	}

	for k, v := range profile.Headers["account"] {
		httpReq.Header.Set(k, v)
	}
	if httpReq.Header.Get("User-Agent") == "" {
		for k, v := range profile.Headers["chat"] {
			if k == "User-Agent" {
				httpReq.Header.Set(k, v)
			}
		}
	}

	httpReq.Header.Set("Authorization", "Bearer "+cred.AccessToken())
	httpReq.Header.Set("X-User-Id", cred.UserID)
	httpReq.Header.Set("X-Request-ID", generateRequestID())
	httpReq.Header.Set("X-Product", "SaaS")
	if cred.Domain != "" {
		httpReq.Header.Set("X-Domain", cred.Domain)
	} else if httpReq.Header.Get("X-Domain") == "" {
		httpReq.Header.Set("X-Domain", "www.workbuddy.cn")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("execute quota request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("read quota response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("quota upstream returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return parseQuotaResponse(bodyBytes)
}

func parseQuotaResponse(bodyBytes []byte) (pluginapi.QuotaFetchResponse, error) {
	var quotaResp quotaResponse
	if err := json.Unmarshal(bodyBytes, &quotaResp); err != nil {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("unmarshal quota response: %w", err)
	}

	accounts := quotaResp.Data.Response.Data.Accounts
	if len(accounts) == 0 {
		accounts = quotaResp.Response.Data.Accounts
	}

	var used, limit float64
	for _, acc := range accounts {
		if acc.CycleCapacityUsedPrecise != nil {
			used += acc.CycleCapacityUsedPrecise.Float64()
		} else if acc.CycleCapacityUsed != nil {
			used += acc.CycleCapacityUsed.Float64()
		} else if acc.CapacityUsedPrecise != nil {
			used += acc.CapacityUsedPrecise.Float64()
		} else if acc.CapacityUsed != nil {
			used += acc.CapacityUsed.Float64()
		}

		if acc.CycleCapacitySizePrecise != nil {
			limit += acc.CycleCapacitySizePrecise.Float64()
		} else if acc.CycleCapacitySize != nil {
			limit += acc.CycleCapacitySize.Float64()
		} else if acc.CapacitySizePrecise != nil {
			limit += acc.CapacitySizePrecise.Float64()
		} else if acc.CapacitySize != nil {
			limit += acc.CapacitySize.Float64()
		}
	}

	used = math.Round(used*1e6) / 1e6
	limit = math.Round(limit*1e6) / 1e6

	remFraction := 0.0
	if limit > 0 {
		remFraction = (limit - used) / limit
		if remFraction < 0 {
			remFraction = 0.0
		} else if remFraction > 1 {
			remFraction = 1.0
		}
	}

	return pluginapi.QuotaFetchResponse{
		Subscription: &pluginapi.QuotaSubscription{
			Plan:     "WorkBuddy",
			TierName: "p_tcaca",
			TierID:   "p_tcaca",
		},
		Groups: []pluginapi.QuotaGroup{
			{
				DisplayName: "Credits",
				Buckets: []pluginapi.QuotaBucket{
					{
						Window:            "Cycle",
						RemainingFraction: remFraction,
						Description:       fmt.Sprintf("已用 %.2f / 总量 %.2f credits", used, limit),
					},
				},
			},
		},
	}, nil
}
