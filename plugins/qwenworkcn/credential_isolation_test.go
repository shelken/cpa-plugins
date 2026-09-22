package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// upstreamCall 是 mock 上游记下的一次请求: 只留「哪份凭据的令牌到了上游」所需字段。
type upstreamCall struct {
	path          string
	authorization string
	machineID     string
	machineToken  string
}

func isolationCredential(t *testing.T, uid, machineID, token string) *Credential {
	t.Helper()
	return &Credential{
		UserID:    uid,
		Endpoint:  "https://gateway.qwenwork.cn",
		MachineID: machineID,
		Credentials: TokenCredentials{
			Access:  token,
			Refresh: "refresh-" + token,
			Expires: 4102444800000, // 2100-01-01, 不触发刷新路径
		},
	}
}

// TestCredentialIsolationAcrossQuotaAndChat 锁住「账号状态不进全局」这一不变量:
// 同一进程里交替用两份凭据触发额度与对话, 上游每条请求带的令牌与机器标识都必须与当次
// 传入的凭据一一对应。任何把令牌、机器身份或令牌派生结果缓存到包级变量的写法都会让它变红。
func TestCredentialIsolationAcrossQuotaAndChat(t *testing.T) {
	m, err := parseManifest(defaultStaticConfigBytes)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}

	var mu sync.Mutex
	var calls []upstreamCall
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, upstreamCall{
			path:          r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			machineID:     r.Header.Get("Cosy-MachineId"),
			machineToken:  r.Header.Get("Cosy-MachineToken"),
		})
		mu.Unlock()

		switch {
		case strings.HasSuffix(r.URL.Path, "/user/wallets"):
			_, _ = w.Write([]byte(`{"data":{"active_wallets":{"wallets":[{"balance":10,"valid_to":"2026-10-01T00:00:00Z"}]}}}`))
		case strings.Contains(r.URL.Path, "agent_chat_generation"):
			// 上游只出流式, 非流式聚合归插件: 给一帧正文 + 一帧 usage + 终止帧
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"id":"c1","created":1,"model":"pro","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":"stop"}]}`)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"id":"c1","created":1,"model":"pro","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldClient := httpClient
	httpClient = server.Client()
	defer func() { httpClient = oldClient }()

	testManifest := *m
	testManifest.Endpoints.BaseURL = server.URL
	testManifest.WebOrigin = server.URL

	credA := isolationCredential(t, "uid-a", "machine-a", "token-a")
	credB := isolationCredential(t, "uid-b", "machine-b", "token-b")
	storageA, err := credA.ToStorageJSON()
	if err != nil {
		t.Fatalf("ToStorageJSON(A): %v", err)
	}
	storageB, err := credB.ToStorageJSON()
	if err != nil {
		t.Fatalf("ToStorageJSON(B): %v", err)
	}
	chatPayload := []byte(`{"model":"qwenworkcn/pro","messages":[{"role":"user","content":"hi"}]}`)

	quota := func(label string, storage []byte) {
		t.Helper()
		if _, err := handleQuotaFetch(context.Background(), &testManifest, &PluginConfig{}, pluginapi.QuotaFetchRequest{StorageJSON: storage}); err != nil {
			t.Fatalf("额度查询 (%s): %v", label, err)
		}
	}
	chat := func(label string, storage []byte) {
		t.Helper()
		if _, err := handleExecute(context.Background(), &testManifest, &PluginConfig{}, pluginapi.ExecutorRequest{
			StorageJSON: storage, Payload: chatPayload,
		}); err != nil {
			t.Fatalf("对话 (%s): %v", label, err)
		}
	}

	quota("凭据 A", storageA)
	chat("凭据 B", storageB)
	quota("凭据 B", storageB)
	chat("凭据 A", storageA)

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 4 {
		t.Fatalf("上游共收到 %d 条请求, 期望 4 条: %+v", len(calls), calls)
	}

	// 额度走 web 头组的 Bearer 令牌, 对话走 Cosy 签名头 (机器标识与令牌明文可见)
	checkQuota := func(call upstreamCall, label, wantAuth string) {
		t.Helper()
		if !strings.HasSuffix(call.path, "/user/wallets") {
			t.Fatalf("%s 打到了 %s, 期望 wallets 路径", label, call.path)
		}
		if call.authorization != "Bearer "+wantAuth {
			t.Fatalf("%s 的 Authorization 不是本凭据的令牌: %q (期望 Bearer %s)", label, call.authorization, wantAuth)
		}
	}
	checkChat := func(call upstreamCall, label, wantToken, wantMachine string) {
		t.Helper()
		if !strings.Contains(call.path, "agent_chat_generation") {
			t.Fatalf("%s 打到了 %s, 期望对话路径", label, call.path)
		}
		if call.machineToken != wantToken {
			t.Fatalf("%s 的 Cosy-MachineToken = %q, 期望 %q (串了别的凭据)", label, call.machineToken, wantToken)
		}
		if call.machineID != wantMachine {
			t.Fatalf("%s 的 Cosy-MachineId = %q, 期望 %q", label, call.machineID, wantMachine)
		}
		if !strings.HasPrefix(call.authorization, "Bearer COSY.") {
			t.Fatalf("%s 的 Authorization 不是 Cosy 签名形态: %q", label, call.authorization)
		}
	}

	checkQuota(calls[0], "额度 (凭据 A)", "token-a")
	checkChat(calls[1], "对话 (凭据 B)", "token-b", "machine-b")
	checkQuota(calls[2], "额度 (凭据 B)", "token-b")
	checkChat(calls[3], "对话 (凭据 A)", "token-a", "machine-a")
}
