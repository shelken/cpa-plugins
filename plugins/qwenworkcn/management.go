package main

import (
	_ "embed"
	"encoding/json"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

//go:embed data/quota-page.html
var quotaPageHTML string

// 插件内只声明相对路径; 宿主会补全成 /v0/resource/plugins/<id>/quota。
const quotaResourcePath = "/quota"

const pluginID = "qwenworkcn"

type managementRegistrationResponse struct {
	Resources []pluginapi.ResourceRoute `json:"resources,omitempty"`
}

// handleManagementRegister 声明浏览器可访问的 resource 页面。
// 面板把额度渲染硬编码给固定几家 provider, 插件自备页面才能让 QwenWork 凭据在面板上看到额度。
func handleManagementRegister() ([]byte, error) {
	return okEnvelope(managementRegistrationResponse{
		Resources: []pluginapi.ResourceRoute{{
			Path:        quotaResourcePath,
			Menu:        "QwenWork Quota",
			Description: "Query quota for QwenWork credentials",
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
		return okEnvelope(pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       []byte(quotaPageHTML),
		})
	}
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: http.StatusNotFound,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(`{"error":"not found"}`),
	})
}
