package main

import (
	_ "embed"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

//go:embed data/quota-page.html
var quotaPageHTML string

// 插件内只声明相对路径; 宿主会补全成 /v0/resource/plugins/<id>/quota。
const quotaResourcePath = "/quota"

const pluginID = "workbuddy"

// 管理页手动签到路由: 宿主最终暴露为 POST /v0/management/plugins/workbuddy/checkin。
const checkinRoutePath = "/plugins/" + pluginID + "/checkin"

type managementRegistrationResponse struct {
	Routes    []pluginapi.ManagementRoute `json:"routes,omitempty"`
	Resources []pluginapi.ResourceRoute   `json:"resources,omitempty"`
}

// handleManagementRegister 声明浏览器可访问的 resource 页面与签到管理路由。
// 宿主把 resource 注册到 /v0/resource/plugins/workbuddy/quota, 面板据此渲染侧边栏菜单。
func handleManagementRegister() ([]byte, error) {
	return okEnvelope(managementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{{
			Method:      http.MethodPost,
			Path:        checkinRoutePath,
			Description: "Claim daily checkin for one WorkBuddy credential (body: {\"auth_index\": \"...\"})",
		}},
		Resources: []pluginapi.ResourceRoute{{
			Path:        quotaResourcePath,
			Menu:        "WorkBuddy Quota",
			Description: "Query quota for WorkBuddy credentials",
		}},
	})
}

// handleManagementHandle 处理宿主转发过来的 resource/management HTTP 请求。
// resource 页面本身不含敏感数据, 凭据数据全部经管理 API (密钥门禁) 获取。
func handleManagementHandle(request []byte) ([]byte, error) {
	var rpcReq struct {
		pluginapi.ManagementRequest
		HostCallbackID string `json:"host_callback_id"`
	}
	if err := json.Unmarshal(request, &rpcReq); err != nil {
		return nil, err
	}

	if rpcReq.Method == http.MethodGet && rpcReq.Path == "/v0/resource/plugins/"+pluginID+quotaResourcePath {
		return okEnvelope(managementPageResponse(getConfig().checkinEnabled()))
	}
	if rpcReq.Method == http.MethodPost && rpcReq.Path == "/v0/management"+checkinRoutePath {
		return handleCheckinRPC(rpcReq.ManagementRequest, rpcReq.HostCallbackID, loadCredentialViaHost)
	}
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: http.StatusNotFound,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(`{"error":"not found"}`),
	})
}

// managementPageResponse 渲染额度页 HTML。签到开关以固定布尔 data attribute 注入,
// 只输出 true/false 两个字面量, 绝不把任意配置文本拼进页面。
func managementPageResponse(checkinEnabled bool) pluginapi.ManagementResponse {
	enabledAttr := "true"
	if !checkinEnabled {
		enabledAttr = "false"
	}
	page := strings.Replace(quotaPageHTML, "<body>", `<body data-checkin-enabled="`+enabledAttr+`">`, 1)
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       []byte(page),
	}
}

// loadCredentialFunc 按 auth_index 取原始凭据 JSON。生产实现走宿主回调, 测试注入内存替身。
type loadCredentialFunc func(hostCallbackID, authIndex string) ([]byte, error)

// loadCredentialViaHost 通过 host.auth.get 宿主回调读取凭据原文。
// hostCallbackID 来自 management.handle 请求, 供宿主还原回调上下文。
func loadCredentialViaHost(hostCallbackID, authIndex string) ([]byte, error) {
	_ = hostCallbackID
	raw, err := callHost(pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	if err != nil {
		return nil, err
	}
	var resp pluginapi.HostAuthGetResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode host auth get response: %w", err)
	}
	return resp.JSON, nil
}

// handleCheckinRPC 处理 POST /v0/management/plugins/workbuddy/checkin。
// 浏览器只提交 auth_index; access token 始终留在宿主与插件进程内。
func handleCheckinRPC(req pluginapi.ManagementRequest, hostCallbackID string, loadCredential loadCredentialFunc) ([]byte, error) {
	if !getConfig().checkinEnabled() {
		return okEnvelope(managementJSON(http.StatusForbidden, `{"error":"签到功能已关闭"}`))
	}

	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil || strings.TrimSpace(body.AuthIndex) == "" {
		return okEnvelope(managementJSON(http.StatusBadRequest, `{"error":"auth_index is required"}`))
	}

	rawCredential, err := loadCredential(hostCallbackID, strings.TrimSpace(body.AuthIndex))
	if err != nil {
		message := err.Error()
		if strings.Contains(message, "auth not found") || strings.Contains(message, "auth file not found") {
			return okEnvelope(managementJSON(http.StatusNotFound, `{"error":"auth not found"}`))
		}
		return okEnvelope(managementJSON(http.StatusBadGateway, fmt.Sprintf(`{"error":%s}`, jsonString(message))))
	}

	var ownership struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(rawCredential, &ownership); err != nil || ownership.Type != pluginID {
		return okEnvelope(managementJSON(http.StatusBadRequest, `{"error":"credential is not a workbuddy credential"}`))
	}

	cred, err := parseCredential(rawCredential)
	if err != nil {
		return okEnvelope(managementJSON(http.StatusBadRequest, fmt.Sprintf(`{"error":%s}`, jsonString(err.Error()))))
	}

	manifest, err := getManifest()
	if err != nil {
		return okEnvelope(managementJSON(http.StatusInternalServerError, fmt.Sprintf(`{"error":%s}`, jsonString(err.Error()))))
	}

	result, err := claimDailyCheckin(context.Background(), manifest, cred)
	if err != nil {
		return okEnvelope(managementJSON(http.StatusBadGateway, fmt.Sprintf(`{"error":%s}`, jsonString(err.Error()))))
	}

	raw, err := json.Marshal(result)
	if err != nil {
		return okEnvelope(managementJSON(http.StatusInternalServerError, `{"error":"marshal checkin result"}`))
	}
	return okEnvelope(managementJSON(http.StatusOK, string(raw)))
}

// managementJSON 包装统一形状的 JSON 管理响应。
func managementJSON(status int, body string) pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(body),
	}
}

// jsonString 把任意错误文本编码成 JSON 字符串字面量, 避免手拼转义出错。
func jsonString(text string) string {
	raw, err := json.Marshal(text)
	if err != nil {
		return `""`
	}
	return string(raw)
}
