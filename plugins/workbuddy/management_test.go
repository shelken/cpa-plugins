package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 宿主按 pluginabi.Envelope 解码插件响应, 断言 resource 注册形状能被正确消费。
func TestManagementRegisterDeclaresQuotaResource(t *testing.T) {
	raw, err := handleMethod(pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatalf("handleMethod(management.register): %v", err)
	}

	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !envelope.OK {
		t.Fatalf("envelope.ok = false: %s", string(raw))
	}

	var resp struct {
		Resources []struct {
			Path        string `json:"path"`
			Menu        string `json:"menu"`
			Description string `json:"description"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(envelope.Result, &resp); err != nil {
		t.Fatalf("decode registration result: %v", err)
	}
	if len(resp.Resources) != 1 {
		t.Fatalf("resources 数量 = %d, want 1", len(resp.Resources))
	}
	resource := resp.Resources[0]
	if resource.Path != "/quota" {
		t.Errorf("resource path = %q, want /quota", resource.Path)
	}
	if resource.Menu == "" {
		t.Error("resource menu 为空, 面板侧边栏将不会出现菜单")
	}
}

// resource 页面 GET 必须返回 200 + HTML; 宿主会在 schema>=6 下原样透传 body。
func TestManagementHandleServesQuotaPage(t *testing.T) {
	request, err := json.Marshal(struct {
		pluginapi.ManagementRequest
		HostCallbackID string `json:"host_callback_id"`
	}{
		ManagementRequest: pluginapi.ManagementRequest{
			Method: http.MethodGet,
			Path:   "/v0/resource/plugins/workbuddy/quota",
		},
	})
	if err != nil {
		t.Fatalf("marshal management request: %v", err)
	}

	raw, err := handleMethod(pluginabi.MethodManagementHandle, request)
	if err != nil {
		t.Fatalf("handleMethod(management.handle): %v", err)
	}

	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !envelope.OK {
		t.Fatalf("envelope.ok = false: %s", string(raw))
	}

	var resp struct {
		StatusCode int
		Body       []byte
	}
	if err := json.Unmarshal(envelope.Result, &resp); err != nil {
		t.Fatalf("decode management response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(resp.Body), "<!doctype html") {
		t.Errorf("body 缺少 <!doctype html, 实际前缀: %q", truncateForTest(string(resp.Body), 60))
	}
}

// 非目标路径必须 404, 防止 resource handler 吞掉其他插件的请求形状。
func TestManagementHandleUnknownPathReturns404(t *testing.T) {
	request, err := json.Marshal(struct {
		pluginapi.ManagementRequest
		HostCallbackID string `json:"host_callback_id"`
	}{
		ManagementRequest: pluginapi.ManagementRequest{
			Method: http.MethodGet,
			Path:   "/v0/resource/plugins/workbuddy/other",
		},
	})
	if err != nil {
		t.Fatalf("marshal management request: %v", err)
	}

	raw, err := handleMethod(pluginabi.MethodManagementHandle, request)
	if err != nil {
		t.Fatalf("handleMethod(management.handle): %v", err)
	}

	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	var resp struct {
		StatusCode int
	}
	if err := json.Unmarshal(envelope.Result, &resp); err != nil {
		t.Fatalf("decode management response: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want 404", resp.StatusCode)
	}
}

func truncateForTest(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
