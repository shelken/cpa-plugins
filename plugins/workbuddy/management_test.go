package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
		Routes []struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		} `json:"routes"`
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
	if len(resp.Routes) != 1 {
		t.Fatalf("routes 数量 = %d, want 1", len(resp.Routes))
	}
	if resp.Routes[0].Method != http.MethodPost || resp.Routes[0].Path != "/plugins/workbuddy/checkin" {
		t.Errorf("route = %s %s, want POST /plugins/workbuddy/checkin", resp.Routes[0].Method, resp.Routes[0].Path)
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
	// 默认配置 checkinEnabled()=true, 页面必须携带 enabled 标记供前端渲染按钮。
	if !strings.Contains(string(resp.Body), `data-checkin-enabled="true"`) {
		t.Error("默认配置下页面应带 data-checkin-enabled=true")
	}
}

// 显式关闭签到时页面必须带 disabled 标记, 前端据此隐藏签到按钮。
func TestManagementHandleQuotaPageCheckinDisabled(t *testing.T) {
	withTestConfig(t, mustParseConfig(t, []byte("enable-checkin: false\n")))

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
	if err := json.Unmarshal(raw, &envelope); err != nil || !envelope.OK {
		t.Fatalf("decode envelope: %v raw=%s", err, raw)
	}
	var resp struct {
		StatusCode int
		Body       []byte
	}
	if err := json.Unmarshal(envelope.Result, &resp); err != nil {
		t.Fatalf("decode management response: %v", err)
	}
	if !strings.Contains(string(resp.Body), `data-checkin-enabled="false"`) {
		t.Error("enable-checkin=false 时页面应带 data-checkin-enabled=false")
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

// ---- 签到端点 ----

const testCheckinPath = "/v0/management/plugins/workbuddy/checkin"

// fakeUpstream 清单指向本地 httptest, 避免测试触达真实上游。
func fakeUpstream(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)

	withTestConfig(t, mustParseConfig(t, nil))
	manifest := testManifest(t, upstream.URL)
	pluginStateMu.Lock()
	prevManifest := cachedManifest
	cachedManifest = manifest
	pluginStateMu.Unlock()
	t.Cleanup(func() {
		pluginStateMu.Lock()
		cachedManifest = prevManifest
		pluginStateMu.Unlock()
	})
	return upstream
}

// managementCall 发送 management.handle RPC 并解出 HTTP 形状 (status + JSON body)。
func managementCall(t *testing.T, method, path string, body []byte) (int, map[string]any) {
	t.Helper()
	request, err := json.Marshal(struct {
		pluginapi.ManagementRequest
		HostCallbackID string `json:"host_callback_id"`
	}{
		ManagementRequest: pluginapi.ManagementRequest{
			Method: method,
			Path:   path,
			Body:   body,
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
	if err := json.Unmarshal(raw, &envelope); err != nil || !envelope.OK {
		t.Fatalf("decode envelope: %v raw=%s", err, raw)
	}
	var resp struct {
		StatusCode int
		Body       json.RawMessage
	}
	if err := json.Unmarshal(envelope.Result, &resp); err != nil {
		t.Fatalf("decode management response: %v result=%s", err, envelope.Result)
	}
	var doc map[string]any
	_ = json.Unmarshal(decodeManagementBody(t, resp.Body), &doc)
	return resp.StatusCode, doc
}

func checkinRequestBody(authIndex string) []byte {
	raw, _ := json.Marshal(map[string]string{"auth_index": authIndex})
	return raw
}

// decodeManagementBody 解出 ManagementResponse 的 Body。[]byte 经 JSON 编码是
// base64 字符串, RawMessage 拿到后必须先解一层 JSON 字符串才是原始报文。
func decodeManagementBody(t *testing.T, rawBody json.RawMessage) []byte {
	t.Helper()
	var bodyBytes []byte
	if err := json.Unmarshal(rawBody, &bodyBytes); err != nil {
		t.Fatalf("decode base64 body: %v raw=%s", err, rawBody)
	}
	return bodyBytes
}

// checkinCallWithLoader 直接调用 handleCheckinRPC, 用于注入凭据加载替身。
func checkinCallWithLoader(t *testing.T, loader loadCredentialFunc, body []byte) (int, map[string]any) {
	t.Helper()
	raw, err := handleCheckinRPC(pluginapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   testCheckinPath,
		Body:   body,
	}, "cb-test", loader)
	if err != nil {
		t.Fatalf("handleCheckinRPC: %v", err)
	}
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || !envelope.OK {
		t.Fatalf("decode envelope: %v raw=%s", err, raw)
	}
	var resp struct {
		StatusCode int
		Body       json.RawMessage
	}
	if err := json.Unmarshal(envelope.Result, &resp); err != nil {
		t.Fatalf("decode management response: %v result=%s", err, envelope.Result)
	}
	var doc map[string]any
	_ = json.Unmarshal(decodeManagementBody(t, resp.Body), &doc)
	return resp.StatusCode, doc
}

// workbuddyStorageJSON 是带 type 归属的最小凭据; token 值用于断言绝不外泄。
const workbuddyStorageJSON = `{"type":"workbuddy","userId":"usr-9","displayName":"UT","credentials":{"access":"SECRET-TOKEN","refresh":"r","expires":9999999999999}}`

func TestCheckinRPCSuccess(t *testing.T) {
	fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"credit":150,"streak_days":3}}`))
	})
	loader := func(hostCallbackID, authIndex string) ([]byte, error) {
		return []byte(workbuddyStorageJSON), nil
	}
	// 显式经过注入的 loader, 验证生产/测试路径在凭据加载这一层解耦。
	status, doc := checkinCallWithLoader(t, loader, checkinRequestBody("idx-1"))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%v)", status, doc)
	}
	if doc["status"] != "claimed" || doc["credit"] != float64(150) || doc["streak_days"] != float64(3) {
		t.Errorf("result = %v, want claimed/150/3", doc)
	}
	if raw, _ := json.Marshal(doc); strings.Contains(string(raw), "SECRET-TOKEN") {
		t.Error("响应泄漏 access token")
	}
}

func TestCheckinRPCDisabledReturns403BeforeCredentialLoad(t *testing.T) {
	upstream := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("关闭签到后不应触达上游")
	})
	_ = upstream

	withTestConfig(t, mustParseConfig(t, []byte("enable-checkin: false\n")))
	status, doc := managementCall(t, http.MethodPost, testCheckinPath, checkinRequestBody("idx-1"))
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%v)", status, doc)
	}
	if doc["error"] != "签到功能已关闭" {
		t.Errorf("error = %v, want 签到功能已关闭", doc["error"])
	}
}

func TestCheckinRPCInputValidation(t *testing.T) {
	fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("非法输入不应触达上游")
	})

	status, doc := managementCall(t, http.MethodPost, testCheckinPath, []byte(`{invalid`))
	if status != http.StatusBadRequest || doc["error"] != "auth_index is required" {
		t.Errorf("非法 JSON: status=%d doc=%v, want 400 auth_index is required", status, doc)
	}

	status, doc = managementCall(t, http.MethodPost, testCheckinPath, checkinRequestBody(""))
	if status != http.StatusBadRequest || doc["error"] != "auth_index is required" {
		t.Errorf("空 auth_index: status=%d doc=%v, want 400", status, doc)
	}
}

func TestCheckinRPCUnknownAuthIndexReturns404(t *testing.T) {
	fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("凭据缺失不应触达上游")
	})
	loader := func(hostCallbackID, authIndex string) ([]byte, error) {
		// 错误文本对齐宿主 authByIndex 的 "auth not found for auth_index ..." 措辞
		return nil, fmt.Errorf("host callback %s failed: auth not found for auth_index %s", pluginabi.MethodHostAuthGet, authIndex)
	}

	status, doc := checkinCallWithLoader(t, loader, checkinRequestBody("ghost"))
	if status != http.StatusNotFound || doc["error"] != "auth not found" {
		t.Errorf("status=%d doc=%v, want 404 auth not found", status, doc)
	}
}

func TestCheckinRPCUpstreamErrorReturns502(t *testing.T) {
	fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":-1,"msg":"upstream down"}`))
	})
	loader := func(hostCallbackID, authIndex string) ([]byte, error) {
		return []byte(workbuddyStorageJSON), nil
	}

	status, doc := checkinCallWithLoader(t, loader, checkinRequestBody("idx-1"))
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body=%v)", status, doc)
	}
	if !strings.Contains(fmt.Sprint(doc["error"]), "status 500") {
		t.Errorf("error = %v, 应包含上游状态码", doc["error"])
	}
}

// 归属校验: 其他 provider 的凭据即使被点名也不得代发签到。
func TestCheckinRPCRejectsForeignCredential(t *testing.T) {
	fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("非 workbuddy 凭据不应触达上游")
	})
	loader := func(hostCallbackID, authIndex string) ([]byte, error) {
		return []byte(`{"type":"codex","userId":"u","credentials":{"access":"x"}}`), nil
	}

	status, doc := checkinCallWithLoader(t, loader, checkinRequestBody("idx-1"))
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%v)", status, doc)
	}
	if !strings.Contains(fmt.Sprint(doc["error"]), "not a workbuddy credential") {
		t.Errorf("error = %v, 应说明归属不符", doc["error"])
	}
}
