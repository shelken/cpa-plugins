package main

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

var fixedTestVectors = struct {
	Raw16     []byte
	PS109     []byte
	RequestID string
	CosyDate  string
}{
	Raw16:     []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
	PS109:     bytes.Repeat([]byte{5}, 109),
	RequestID: "11111111-2222-4333-8444-555555555555",
	CosyDate:  "1720000000",
}

func TestEncodeCosyBodyRoundtrip(t *testing.T) {
	cases := []string{
		"hi",
		strings.Repeat("x", 17),
		`{"a":1,"c":"中"}`,
	}
	for _, text := range cases {
		encoded, err := encodeCosyBody(text)
		if err != nil {
			t.Fatalf("encodeCosyBody(%q): %v", text, err)
		}
		decoded, err := decodeCosyBody(encoded)
		if err != nil {
			t.Fatalf("decodeCosyBody(%q): %v", encoded, err)
		}
		if decoded != text {
			t.Fatalf("roundtrip mismatch: got %q, want %q", decoded, text)
		}
	}
}

func TestSessionKeyAscii(t *testing.T) {
	sk := string(sessionKeyAscii(fixedTestVectors.Raw16))
	want := "100f0e0d0c0b4a09"
	if sk != want {
		t.Fatalf("sessionKeyAscii = %q, want %q", sk, want)
	}
}

func TestRuntimeFromRawStable(t *testing.T) {
	payload := `{"uid":"u1","organization_id":"","organization_tags":[],"data_policy_agreed":true}`
	rt, err := runtimeFromRaw(fixedTestVectors.Raw16, fixedTestVectors.PS109, payload)
	if err != nil {
		t.Fatalf("runtimeFromRaw: %v", err)
	}

	wantKey := "TECQP7ukJLbb82h+pudMK/bf3WcbZ/86oTRPsJeXW8EDD59DyRL1guKpUmXpT/ei4EdOGHERhzq+DvdBWcpt2neaB7P2lps/hqwjJ6vvTg5XoIeuJ6jUwUSDVZ+IUYgDaiQoMMTE3HUUT7YSRh26JczMJ2+boOYzo/kPvR13DHo="
	wantEncryptUserInfo := "IHVZsyVanCkGw8c4vqpQb5tyZXSAlTSFQ0tQXzmf+o60Cj/CGdl/ksfDw7OYrDJBNkudqsJarW4Ybr9srODQbvHUz4QPzoe/gPPA/dP0dbDRwiMh6W0TOsA8sKKrOwBG"

	if rt.Key != wantKey {
		t.Fatalf("rt.Key = %q, want %q", rt.Key, wantKey)
	}
	if rt.EncryptUserInfo != wantEncryptUserInfo {
		t.Fatalf("rt.EncryptUserInfo = %q, want %q", rt.EncryptUserInfo, wantEncryptUserInfo)
	}

	rawKey, err := base64.StdEncoding.DecodeString(rt.Key)
	if err != nil || len(rawKey) != 128 {
		t.Fatalf("rt.Key decoded length = %d, want 128 (err=%v)", len(rawKey), err)
	}
	rawUserInfo, err := base64.StdEncoding.DecodeString(rt.EncryptUserInfo)
	if err != nil || len(rawUserInfo)%16 != 0 {
		t.Fatalf("rt.EncryptUserInfo decoded length = %d (want %%16 == 0)", len(rawUserInfo))
	}
}

func TestSignInferRequestFixed(t *testing.T) {
	plain := `{"messages":[{"role":"user","content":"yo"}]}`
	// 金标向量只在 cosyVersion=1.1.18 下有效 (cosyVersion 进 Authorization p1 payload)。
	// 显式固定, 不吃 manifest: 数据刷新 (版本跟随) 不应反复打断金标测试。
	fixedCosyVersion := "1.1.18"
	staticHeaders := map[string]string{
		"Cosy-Version":          fixedCosyVersion,
		"Cosy-Data-Policy":      "disagree",
		"Cosy-ClientType":       "6",
		"Cosy-Business-Product": "qoder_work",
		"Cosy-Business-Type":    "agent",
		"Cosy-Scene":            "qwork",
		"Cosy-MachineOS":        "darwin",
		"Cosy-MachineType":      "aarch64",
		"X-Model-Key":           "qmodel_latest",
		"X-Model-Source":        "system",
	}
	out, err := signCosyRequest(SignInferInput{
		Endpoint:      "https://gateway.qwenwork.cn",
		MachineID:     "mid",
		DeviceToken:   "DEVICE-TOKEN",
		Body:          plain,
		CosyVersion:   fixedCosyVersion,
		StaticHeaders: staticHeaders,
		User:          &SignInferUser{UID: "u9"},
		RequestID:     fixedTestVectors.RequestID,
		CosyDate:      fixedTestVectors.CosyDate,
		FixedRuntime: &FixedRuntime{
			Raw16: fixedTestVectors.Raw16,
			PS109: fixedTestVectors.PS109,
		},
	})
	if err != nil {
		t.Fatalf("signCosyRequest: %v", err)
	}

	wantBody := "u(QwDSQwBMn*mSq*eF.WNOWrD,ByBEF^DxB*GoKMmYKtDxj^#SJLNYBySkr*"
	if out.Body != wantBody {
		t.Fatalf("out.Body = %q, want %q", out.Body, wantBody)
	}

	decodedBody, err := decodeCosyBody(out.Body)
	if err != nil || decodedBody != plain {
		t.Fatalf("decodeCosyBody mismatch: got %q, want %q (err=%v)", decodedBody, plain, err)
	}

	wantAuth := "Bearer COSY.eyJ2ZXJzaW9uIjoidjEiLCJyZXF1ZXN0SWQiOiIxMTExMTExMS0yMjIyLTQzMzMtODQ0NC01NTU1NTU1NTU1NTUiLCJpbmZvIjoiNG41bXB4WUtrOW9ma3l5S0djNkpqcFB3ejFpZDlQbGJJVmhYSFNpNEtQVzlOeVZIVklmTktWczlBeVkzNjI4K3dBY1FNNUJDeUoxQ3lKRE1OQUsxbDhQZm52MjNXK0JZVE5IaHRQakRoNzgzSTZ5ZzlhOFkydzVGWGdsbXVHajkiLCJjb3N5VmVyc2lvbiI6IjEuMS4xOCIsImlkZVZlcnNpb24iOiIifQ==.c8d9c4f5af6e9b2ebd0f4e6737ba4940"
	if gotAuth := out.Headers["Authorization"]; gotAuth != wantAuth {
		t.Fatalf("Authorization = %q\nwant = %q", gotAuth, wantAuth)
	}

	if out.Headers["Cosy-MachineToken"] != "DEVICE-TOKEN" {
		t.Fatalf("Cosy-MachineToken = %q, want DEVICE-TOKEN", out.Headers["Cosy-MachineToken"])
	}
	if out.Headers["Cosy-MachineId"] != "mid" {
		t.Fatalf("Cosy-MachineId = %q, want mid", out.Headers["Cosy-MachineId"])
	}
	if out.Headers["Cosy-User"] != "u9" {
		t.Fatalf("Cosy-User = %q, want u9", out.Headers["Cosy-User"])
	}
	if out.Headers["Cosy-ClientType"] != "6" {
		t.Fatalf("Cosy-ClientType = %q, want 6", out.Headers["Cosy-ClientType"])
	}
	if out.Headers["Cosy-Data-Policy"] != "disagree" {
		t.Fatalf("Cosy-Data-Policy = %q, want disagree", out.Headers["Cosy-Data-Policy"])
	}
	if out.Headers["X-Model-Key"] != "qmodel_latest" {
		t.Fatalf("X-Model-Key = %q, want qmodel_latest", out.Headers["X-Model-Key"])
	}
	if !strings.Contains(out.URL, CosyChatPath) || !strings.Contains(out.URL, "Encode=1") {
		t.Fatalf("out.URL invalid: %q", out.URL)
	}
}

func TestSignCosyRequestFailFast(t *testing.T) {
	validHeaders := map[string]string{
		"Cosy-Version":     "1.1.18",
		"Cosy-Data-Policy": "disagree",
	}

	// 1. deviceToken 为空
	if _, err := signCosyRequest(SignInferInput{
		MachineID:     "mid",
		DeviceToken:   "",
		CosyVersion:   "1.1.18",
		StaticHeaders: validHeaders,
	}); err == nil {
		t.Fatal("expected error for empty deviceToken, got nil")
	}

	// 2. machineId 为空
	if _, err := signCosyRequest(SignInferInput{
		MachineID:     "",
		DeviceToken:   "dt",
		CosyVersion:   "1.1.18",
		StaticHeaders: validHeaders,
	}); err == nil {
		t.Fatal("expected error for empty machineId, got nil")
	}

	// 3. cosyVersion 为空
	if _, err := signCosyRequest(SignInferInput{
		MachineID:     "mid",
		DeviceToken:   "dt",
		CosyVersion:   "",
		StaticHeaders: validHeaders,
	}); err == nil {
		t.Fatal("expected error for empty cosyVersion, got nil")
	}

	// 4. staticHeaders 为空
	if _, err := signCosyRequest(SignInferInput{
		MachineID:     "mid",
		DeviceToken:   "dt",
		CosyVersion:   "1.1.18",
		StaticHeaders: nil,
	}); err == nil {
		t.Fatal("expected error for nil staticHeaders, got nil")
	}

	// 5. staticHeaders 缺少 Cosy-Version
	if _, err := signCosyRequest(SignInferInput{
		MachineID:   "mid",
		DeviceToken: "dt",
		CosyVersion: "1.1.18",
		StaticHeaders: map[string]string{
			"Cosy-Data-Policy": "disagree",
		},
	}); err == nil {
		t.Fatal("expected error for missing Cosy-Version, got nil")
	}

	// 6. staticHeaders 缺少 Cosy-Data-Policy
	if _, err := signCosyRequest(SignInferInput{
		MachineID:   "mid",
		DeviceToken: "dt",
		CosyVersion: "1.1.18",
		StaticHeaders: map[string]string{
			"Cosy-Version": "1.1.18",
		},
	}); err == nil {
		t.Fatal("expected error for missing Cosy-Data-Policy, got nil")
	}
}
