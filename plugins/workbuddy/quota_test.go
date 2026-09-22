package main

import (
	"math"
	"testing"
)

func TestQuotaDescribe(t *testing.T) {
	desc := handleQuotaDescribe()
	if len(desc.SupportedProviders) != 1 || desc.SupportedProviders[0] != "workbuddy" {
		t.Errorf("expected supported provider workbuddy, got %v", desc.SupportedProviders)
	}
	if desc.DisplayName != "WorkBuddy" {
		t.Errorf("expected display name WorkBuddy, got %s", desc.DisplayName)
	}
	if desc.SupportsReset {
		t.Error("expected SupportsReset false")
	}
}

func TestQuotaCalculation(t *testing.T) {
	used1 := flexFloat(10.5)
	limit1 := flexFloat(100.0)
	used2 := flexFloat(5.25)
	limit2 := flexFloat(50.0)

	accounts := []quotaAccount{
		{
			CycleCapacityUsedPrecise: &used1,
			CycleCapacitySizePrecise: &limit1,
		},
		{
			CycleCapacityUsedPrecise: &used2,
			CycleCapacitySizePrecise: &limit2,
		},
	}

	var used, limit float64
	for _, acc := range accounts {
		if acc.CycleCapacityUsedPrecise != nil {
			used += acc.CycleCapacityUsedPrecise.Float64()
		}
		if acc.CycleCapacitySizePrecise != nil {
			limit += acc.CycleCapacitySizePrecise.Float64()
		}
	}

	used = math.Round(used*1e6) / 1e6
	limit = math.Round(limit*1e6) / 1e6

	remFraction := 0.0
	if limit > 0 {
		remFraction = (limit - used) / limit
	}

	if used != 15.75 {
		t.Errorf("expected used 15.75, got %f", used)
	}
	if limit != 150.0 {
		t.Errorf("expected limit 150.0, got %f", limit)
	}
	expectedFraction := (150.0 - 15.75) / 150.0
	if math.Abs(remFraction-expectedFraction) > 1e-6 {
		t.Errorf("expected remFraction %f, got %f", expectedFraction, remFraction)
	}
}

func TestQuotaResponseStringFieldsRegression(t *testing.T) {
	// Exact server shape where *Precise fields are strings (including decimals and empty strings)
	rawServerJSON := []byte(`{
		"code": 0,
		"msg": "ok",
		"data": {
			"Response": {
				"Data": {
					"Accounts": [
						{
							"PackageName": "CodeBuddy个人体验版",
							"CycleCapacityUsedPrecise": "0",
							"CycleCapacitySizePrecise": "500",
							"CycleCapacityRemainPrecise": "500"
						},
						{
							"PackageName": "CodeBuddy个人体验版",
							"CycleCapacityUsedPrecise": "0.65",
							"CycleCapacitySizePrecise": "100.0",
							"CycleCapacityRemainPrecise": "499.35"
						},
						{
							"PackageName": "赠送包",
							"CycleCapacityUsedPrecise": "",
							"CycleCapacityUsed": 0,
							"CycleCapacitySizePrecise": "50.5"
						}
					]
				}
			}
		}
	}`)

	resp, err := parseQuotaResponse(rawServerJSON)
	if err != nil {
		t.Fatalf("parseQuotaResponse failed on string fields: %v", err)
	}

	// 套餐名来自上游 Accounts[].PackageName, 按出现顺序去重; 上游没有档位 id, 不编造
	if resp.Subscription == nil || resp.Subscription.Plan != "CodeBuddy个人体验版 + 赠送包" {
		t.Errorf("unexpected subscription: %+v", resp.Subscription)
	}
	if resp.Subscription.TierName != "" || resp.Subscription.TierID != "" {
		t.Errorf("档位字段应为空 (上游无此数据), 实际 %+v", resp.Subscription)
	}

	if len(resp.Groups) != 1 || len(resp.Groups[0].Buckets) != 1 {
		t.Fatalf("unexpected quota groups: %+v", resp.Groups)
	}

	bucket := resp.Groups[0].Buckets[0]
	if bucket.RemainingFraction < 0 || bucket.RemainingFraction > 1 {
		t.Errorf("RemainingFraction out of bounds [0, 1]: %f", bucket.RemainingFraction)
	}

	expectedUsed := 0.65
	expectedLimit := 500.0 + 100.0 + 50.5 // 650.5
	expectedFraction := (expectedLimit - expectedUsed) / expectedLimit

	if math.Abs(bucket.RemainingFraction-expectedFraction) > 1e-6 {
		t.Errorf("RemainingFraction = %f, want %f", bucket.RemainingFraction, expectedFraction)
	}
}
