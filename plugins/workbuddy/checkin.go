package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// checkinTimeout 对齐源实现 pi-codebuddy-provider/src/checkin.ts claimCheckin 的 5s 超时。
const checkinTimeout = 5 * time.Second

// checkinResult 是管理页消费的签到结果:
//   - claimed: 本次请求成功领取, credit/streak_days 为上游返回的可选奖励字段
//   - already_claimed: 上游判定今日已领 (code=-1 msg=already_claimed 或 code=10001)
type checkinResult struct {
	Status     string `json:"status"`
	Credit     *int   `json:"credit,omitempty"`
	StreakDays *int   `json:"streak_days,omitempty"`
}

const (
	checkinStatusClaimed        = "claimed"
	checkinStatusAlreadyClaimed = "already_claimed"
)

type checkinUpstreamResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data *struct {
		Credit     *int `json:"credit"`
		StreakDays *int `json:"streak_days"`
	} `json:"data"`
}

// claimDailyCheckin 对单个凭据发起一次每日签到。固定走 desktop 档的 checkin 头组:
// 该端点只存在于桌面主进程 (workbuddyDesktop.mainProcess), 与 identity-profile 配置无关。
// 无状态不重试: 幂等性由上游 already_claimed 响应保证, 签到记录不写回凭据。
func claimDailyCheckin(ctx context.Context, manifest *ManifestV2, cred *Credential) (checkinResult, error) {
	profile, err := manifest.GetProfile(string(ProfileDesktop))
	if err != nil {
		return checkinResult{}, fmt.Errorf("get desktop profile for checkin: %w", err)
	}
	bodyRaw, ok := manifest.RequestBodies["dailyCheckin"]
	if !ok {
		return checkinResult{}, fmt.Errorf("manifest requestBodies.dailyCheckin is missing")
	}
	rawBody, err := json.Marshal(bodyRaw)
	if err != nil {
		return checkinResult{}, fmt.Errorf("marshal checkin request body: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, checkinTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, manifest.BuildURL(manifest.Endpoints.DailyCheckin), bytes.NewReader(rawBody))
	if err != nil {
		return checkinResult{}, fmt.Errorf("create checkin request: %w", err)
	}

	for k, v := range profile.Headers["checkin"] {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Authorization", "Bearer "+cred.AccessToken())
	httpReq.Header.Set("X-User-Id", cred.UserID)

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return checkinResult{}, fmt.Errorf("execute checkin request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return checkinResult{}, fmt.Errorf("read checkin response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return checkinResult{}, fmt.Errorf("checkin upstream returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return parseCheckinResponse(bodyBytes)
}

// 响应映射跟随 pi-codebuddy-provider/src/checkin.ts claimCheckin 的实测口径 (2026-08-09):
// 成功是 code==0 且 data 存在; 已领是 code==-1 msg==already_claimed 或 code==10001。
func parseCheckinResponse(bodyBytes []byte) (checkinResult, error) {
	var parsed checkinUpstreamResponse
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		return checkinResult{}, fmt.Errorf("unmarshal checkin response: %w", err)
	}

	if parsed.Code == 0 {
		if parsed.Data == nil {
			return checkinResult{}, fmt.Errorf("checkin response code=0 but data is missing")
		}
		result := checkinResult{Status: checkinStatusClaimed}
		if parsed.Data.Credit != nil {
			credit := *parsed.Data.Credit
			result.Credit = &credit
		}
		if parsed.Data.StreakDays != nil {
			streak := *parsed.Data.StreakDays
			result.StreakDays = &streak
		}
		return result, nil
	}

	if (parsed.Code == -1 && parsed.Msg == "already_claimed") || parsed.Code == 10001 {
		return checkinResult{Status: checkinStatusAlreadyClaimed}, nil
	}

	return checkinResult{}, fmt.Errorf("checkin rejected code=%d msg=%s", parsed.Code, parsed.Msg)
}
