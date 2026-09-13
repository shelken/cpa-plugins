package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type quotaPack struct {
	Total     float64
	Used      float64
	Remaining float64
}

func readQuotaPack(raw any) (*quotaPack, bool) {
	m, ok := raw.(map[string]any)
	if !ok || m == nil {
		return nil, false
	}
	t, okT := asFiniteNumber(m["total"])
	u, okU := asFiniteNumber(m["used"])
	r, okR := asFiniteNumber(m["remaining"])
	if !okT || !okU || !okR {
		return nil, false
	}
	return &quotaPack{Total: t, Used: u, Remaining: r}, true
}

type walletItem struct {
	Balance float64 `json:"balance"`
	ValidTo string  `json:"valid_to"`
}

func handleQuotaDescribe() pluginapi.QuotaDescribeResponse {
	return pluginapi.QuotaDescribeResponse{
		SupportedProviders: []string{"qwenworkcn"},
		DisplayName:        "QwenWork",
		SupportsReset:      false,
	}
}

func handleQuotaFetch(ctx context.Context, manifest *ManifestV2, cfg *PluginConfig, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
	cred, err := parseCredential(req.StorageJSON)
	if err != nil {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("parse storage json for quota fetch: %w", err)
	}

	// 1. 尝试从 openApi /api/v2/quota/usage 查询额度包
	usageResp, errUsage := fetchQuotaUsage(ctx, manifest, cred)
	if errUsage != nil {
		if strings.Contains(errUsage.Error(), "HTTP 401") {
			return pluginapi.QuotaFetchResponse{}, errUsage
		}
	} else if usageResp != nil {
		return *usageResp, nil
	}

	// 2. usage 无包时，从 webOrigin /user/wallets 查询钱包余额（qwenwork 主源）
	walletResp, errWallet := fetchWalletsQuota(ctx, manifest, cred)
	if errWallet != nil {
		if strings.Contains(errWallet.Error(), "HTTP 401") {
			return pluginapi.QuotaFetchResponse{}, errWallet
		}
	} else if walletResp != nil {
		return *walletResp, nil
	}

	// 3. 两者都无数据返回零值组
	return pluginapi.QuotaFetchResponse{
		Subscription: &pluginapi.QuotaSubscription{
			Plan:     "QwenWork",
			TierName: "default",
			TierID:   "default",
		},
		Groups: []pluginapi.QuotaGroup{
			{
				DisplayName: "Credits",
				Buckets: []pluginapi.QuotaBucket{
					{
						Window:            "None",
						RemainingFraction: 0.0,
						Description:       "无可用额度包且钱包为空",
					},
				},
			},
		},
	}, nil
}

func fetchQuotaUsage(ctx context.Context, manifest *ManifestV2, cred *Credential) (*pluginapi.QuotaFetchResponse, error) {
	headers, err := manifest.RenderHeaderGroup("openApi", map[string]string{
		"deviceToken": cred.AccessToken(),
	})
	if err != nil {
		return nil, fmt.Errorf("render openApi headers: %w", err)
	}

	urlStr := manifest.BuildURL(manifest.Endpoints.QuotaUsage)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("create quota usage request: %w", err)
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("execute quota usage request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read quota usage response body: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("quota usage upstream returned HTTP 401: %s", string(bodyBytes))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("quota usage upstream returned HTTP %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var root map[string]any
	if err := json.Unmarshal(bodyBytes, &root); err != nil {
		return nil, fmt.Errorf("unmarshal quota usage: %w", err)
	}

	var userRaw, addOnRaw any
	if v, ok := root["user_quota"]; ok {
		userRaw = v
	} else if v, ok := root["userQuota"]; ok {
		userRaw = v
	}
	if v, ok := root["add_on_quota"]; ok {
		addOnRaw = v
	} else if v, ok := root["addOnQuota"]; ok {
		addOnRaw = v
	}

	userPack, hasUser := readQuotaPack(userRaw)
	addOnPack, hasAddOn := readQuotaPack(addOnRaw)

	if !hasUser && !hasAddOn {
		return nil, nil
	}

	var total, used, remaining float64
	if hasUser {
		total += userPack.Total
		used += userPack.Used
		remaining += userPack.Remaining
	}
	if hasAddOn {
		total += addOnPack.Total
		used += addOnPack.Used
		remaining += addOnPack.Remaining
	}

	remFraction := 0.0
	if total > 0 {
		remFraction = remaining / total
		if remFraction < 0 {
			remFraction = 0.0
		} else if remFraction > 1 {
			remFraction = 1.0
		}
	}

	return &pluginapi.QuotaFetchResponse{
		Subscription: &pluginapi.QuotaSubscription{
			Plan:     "QwenWork",
			TierName: "default",
			TierID:   "default",
		},
		Groups: []pluginapi.QuotaGroup{
			{
				DisplayName: "Credits",
				Buckets: []pluginapi.QuotaBucket{
					{
						Window:            "Usage",
						RemainingFraction: remFraction,
						Description:       fmt.Sprintf("已用 %.0f / 总量 %.0f credits (剩余 %.0f)", used, total, remaining),
					},
				},
			},
		},
	}, nil
}

func fetchWalletsQuota(ctx context.Context, manifest *ManifestV2, cred *Credential) (*pluginapi.QuotaFetchResponse, error) {
	if manifest.WebOrigin == "" {
		return nil, nil
	}

	headers, err := manifest.RenderHeaderGroup("web", map[string]string{
		"deviceToken": cred.AccessToken(),
	})
	if err != nil {
		return nil, fmt.Errorf("render web headers: %w", err)
	}

	urlStr := manifest.WebURL("/user/wallets")
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("create wallets request: %w", err)
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("execute wallets request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read wallets response body: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("wallets upstream returned HTTP 401: %s", string(bodyBytes))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wallets upstream returned HTTP %d: %s", resp.StatusCode, string(bodyBytes))
	}

	wallets := parseWalletsBody(bodyBytes)
	if len(wallets) == 0 {
		return nil, nil
	}

	var remaining float64
	for _, w := range wallets {
		remaining += w.Balance
	}

	desc := fmt.Sprintf("余额 %.0f credits", remaining)
	if wallets[0].ValidTo != "" {
		desc += fmt.Sprintf(", 最早 %s 到期", wallets[0].ValidTo)
	}

	return &pluginapi.QuotaFetchResponse{
		Subscription: &pluginapi.QuotaSubscription{
			Plan:     "QwenWork",
			TierName: "Wallet",
			TierID:   "wallet",
		},
		Groups: []pluginapi.QuotaGroup{
			{
				DisplayName: "Credits",
				Buckets: []pluginapi.QuotaBucket{
					{
						Window:            "Wallet",
						RemainingFraction: 1.0,
						Description:       desc,
					},
				},
			},
		},
	}, nil
}

func parseWalletsBody(bodyBytes []byte) []walletItem {
	var root struct {
		Data struct {
			ActiveWallets struct {
				Wallets []struct {
					Balance any    `json:"balance"`
					ValidTo string `json:"valid_to"`
				} `json:"wallets"`
			} `json:"active_wallets"`
		} `json:"data"`
	}

	if err := json.Unmarshal(bodyBytes, &root); err != nil {
		return nil
	}

	rawList := root.Data.ActiveWallets.Wallets
	if len(rawList) == 0 {
		return nil
	}

	var out []walletItem
	for _, w := range rawList {
		bal, ok := asFiniteNumber(w.Balance)
		if !ok || bal < 0 {
			continue
		}
		out = append(out, walletItem{
			Balance: bal,
			ValidTo: strings.TrimSpace(w.ValidTo),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].ValidTo < out[j].ValidTo
	})

	return out
}
