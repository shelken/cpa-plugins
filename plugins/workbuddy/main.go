package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type rpcRegistration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ModelProvider         bool                         `json:"model_provider"`
	AuthProvider          bool                         `json:"auth_provider"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats,omitempty"`
	QuotaProvider         bool                         `json:"quota_provider"`
	ManagementAPI         bool                         `json:"management_api"`
}

type rpcIdentifierResponse struct {
	Identifier string `json:"identifier"`
}

type rpcLifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type rpcStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}

type rpcStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

var (
	pluginStateMu  sync.RWMutex
	currentConfig  *PluginConfig
	cachedManifest *ManifestV2
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	log.Println("[workbuddy] plugin initialized successfully")
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}

	methodStr := C.GoString(method)
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}

	raw, err := handleMethod(methodStr, requestBytes)
	if err != nil {
		raw = errorEnvelope("internal_error", err.Error())
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	log.Println("[workbuddy] plugin shutting down")
}

func callHost(method string, payload any) (json.RawMessage, error) {
	var rawPayload []byte
	if payload != nil {
		var err error
		rawPayload, err = json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal host callback payload %s: %w", method, err)
		}
	}

	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		cPayload := C.CBytes(rawPayload)
		if cPayload == nil {
			return nil, fmt.Errorf("allocate host callback payload %s", method)
		}
		defer C.free(cPayload)
		requestPtr = (*C.uint8_t)(cPayload)
	}

	callCode := C.call_host_api(cMethod, requestPtr, C.size_t(len(rawPayload)), &response)

	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}

	if callCode != 0 {
		return nil, fmt.Errorf("host callback %s returned error code %d", method, int(callCode))
	}

	if len(rawResponse) == 0 {
		return nil, nil
	}

	var env envelope
	if err := json.Unmarshal(rawResponse, &env); err != nil {
		return nil, fmt.Errorf("decode host callback envelope %s: %w", method, err)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}

	return env.Result, nil
}

func callHostStreamEmit(streamID string, payload []byte) error {
	_, err := callHost(pluginabi.MethodHostStreamEmit, rpcStreamEmitRequest{
		StreamID: streamID,
		Payload:  payload,
	})
	return err
}

func callHostStreamClose(streamID string, errMsg string) error {
	_, err := callHost(pluginabi.MethodHostStreamClose, rpcStreamCloseRequest{
		StreamID: streamID,
		Error:    errMsg,
	})
	return err
}

func getManifest() (*ManifestV2, error) {
	pluginStateMu.RLock()
	m := cachedManifest
	pluginStateMu.RUnlock()
	if m != nil {
		return m, nil
	}

	var data []byte
	candidates := []string{
		"plugins/workbuddy/data/static-config.json",
		"data/static-config.json",
	}
	for _, p := range candidates {
		if d, err := os.ReadFile(p); err == nil && len(d) > 0 {
			data = d
			break
		}
	}
	if len(data) == 0 {
		data = defaultStaticConfigBytes
	}

	parsed, err := parseManifest(data)
	if err != nil {
		return nil, err
	}

	pluginStateMu.Lock()
	cachedManifest = parsed
	pluginStateMu.Unlock()
	return parsed, nil
}

func getConfig() *PluginConfig {
	pluginStateMu.RLock()
	defer pluginStateMu.RUnlock()
	if currentConfig == nil {
		return &PluginConfig{
			Enabled:         true,
			IdentityProfile: string(ProfileDesktop),
			LoginProfile:    string(ProfileDesktop),
		}
	}
	return currentConfig
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var req rpcLifecycleRequest
		if len(request) > 0 {
			_ = json.Unmarshal(request, &req)
		}

		cfg, err := parseConfig(req.ConfigYAML)
		if err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}

		pluginStateMu.Lock()
		currentConfig = cfg
		cachedManifest = nil
		pluginStateMu.Unlock()

		if _, err := getManifest(); err != nil {
			log.Printf("[workbuddy] warning: load manifest: %v", err)
		}

		return okEnvelope(rpcRegistration{
			SchemaVersion: pluginabi.SchemaVersion,
			Metadata: pluginapi.Metadata{
				Name:             "WorkBuddy",
				Version:          "0.2.5",
				Author:           "shelken",
				GitHubRepository: "https://github.com/shelken/cpa-plugins",
				ConfigFields: []pluginapi.ConfigField{
					{
						Name:        "identity-profile",
						Type:        pluginapi.ConfigFieldTypeEnum,
						EnumValues:  []string{string(ProfileDesktop), string(ProfileCLI)},
						Description: "上行请求使用的身份档: desktop 与桌面客户端一致, cli 与 CLI 一致。默认 desktop。",
					},
					{
						Name:        "login-profile",
						Type:        pluginapi.ConfigFieldTypeEnum,
						EnumValues:  []string{string(ProfileDesktop), string(ProfileCLI)},
						Description: "扫码登录走哪个平台: desktop 用 workbuddy 平台, cli 用 cli 平台。默认 desktop。",
					},
					{
						Name:        "enable-model-prefix",
						Type:        pluginapi.ConfigFieldTypeBoolean,
						Description: "注册模型 id 是否带前缀。默认 true。",
					},
					{
						Name:        "enable-checkin",
						Type:        pluginapi.ConfigFieldTypeBoolean,
						Description: "是否允许管理页手动签到 (单账号与一键全部)。默认 true。",
					},
					{
						Name:        "model-prefix",
						Type:        pluginapi.ConfigFieldTypeString,
						Description: "模型 id 前缀, 默认 workbuddy, 仅 enable-model-prefix 为 true 时生效。",
					},
				},
			},
			Capabilities: registrationCapability{
				ModelProvider:         true,
				AuthProvider:          true,
				Executor:              true,
				ExecutorModelScope:    pluginapi.ExecutorModelScopeStatic,
				ExecutorInputFormats:  []string{"chat-completions"},
				ExecutorOutputFormats: []string{"chat-completions"},
				QuotaProvider:         true,
				ManagementAPI:         true,
			},
		})

	case pluginabi.MethodPluginQuiesce, pluginabi.MethodPluginShutdown:
		return okEnvelope(map[string]any{})

	case pluginabi.MethodAuthIdentifier:
		return okEnvelope(rpcIdentifierResponse{Identifier: "workbuddy"})

	case pluginabi.MethodAuthParse:
		var req pluginapi.AuthParseRequest
		if err := json.Unmarshal(request, &req); err != nil {
			return nil, fmt.Errorf("unmarshal auth parse request: %w", err)
		}
		resp, err := handleAuthParse(req)
		if err != nil {
			return nil, err
		}
		return okEnvelope(resp)

	case pluginabi.MethodAuthLoginStart:
		var req pluginapi.AuthLoginStartRequest
		if err := json.Unmarshal(request, &req); err != nil {
			return nil, fmt.Errorf("unmarshal auth login start request: %w", err)
		}
		m, err := getManifest()
		if err != nil {
			return nil, err
		}
		resp, err := handleAuthLoginStart(context.Background(), m, getConfig(), req)
		if err != nil {
			return nil, err
		}
		return okEnvelope(resp)

	case pluginabi.MethodAuthLoginPoll:
		var req pluginapi.AuthLoginPollRequest
		if err := json.Unmarshal(request, &req); err != nil {
			return nil, fmt.Errorf("unmarshal auth login poll request: %w", err)
		}
		m, err := getManifest()
		if err != nil {
			return nil, err
		}
		resp, err := handleAuthLoginPoll(context.Background(), m, getConfig(), req)
		if err != nil {
			return nil, err
		}
		return okEnvelope(resp)

	case pluginabi.MethodAuthRefresh:
		var req pluginapi.AuthRefreshRequest
		if err := json.Unmarshal(request, &req); err != nil {
			return nil, fmt.Errorf("unmarshal auth refresh request: %w", err)
		}
		m, err := getManifest()
		if err != nil {
			return nil, err
		}
		resp, err := handleAuthRefresh(context.Background(), m, getConfig(), req)
		if err != nil {
			return nil, err
		}
		return okEnvelope(resp)

	case pluginabi.MethodModelStatic:
		m, err := getManifest()
		if err != nil {
			return nil, err
		}
		models := mapManifestModels(getConfig(), m.Models)
		return okEnvelope(pluginapi.ModelResponse{
			Provider: "workbuddy",
			Models:   models,
		})

	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(rpcIdentifierResponse{Identifier: "workbuddy"})

	case pluginabi.MethodExecutorExecuteStream:
		var rpcReq struct {
			pluginapi.ExecutorRequest
			StreamID       string `json:"stream_id"`
			HostCallbackID string `json:"host_callback_id"`
		}
		if err := json.Unmarshal(request, &rpcReq); err != nil {
			return nil, fmt.Errorf("unmarshal executor stream request: %w", err)
		}
		m, err := getManifest()
		if err != nil {
			return nil, err
		}
		resp, err := handleExecuteStream(context.Background(), m, getConfig(), rpcReq.ExecutorRequest, rpcReq.StreamID)
		if err != nil {
			return nil, err
		}
		return okEnvelope(resp)

	case pluginabi.MethodExecutorExecute:
		var rpcReq struct {
			pluginapi.ExecutorRequest
			HostCallbackID string `json:"host_callback_id"`
		}
		if err := json.Unmarshal(request, &rpcReq); err != nil {
			return nil, fmt.Errorf("unmarshal executor execute request: %w", err)
		}
		m, err := getManifest()
		if err != nil {
			return nil, err
		}
		resp, err := handleExecute(context.Background(), m, getConfig(), rpcReq.ExecutorRequest)
		if err != nil {
			return nil, err
		}
		return okEnvelope(resp)

	case pluginabi.MethodExecutorCountTokens:
		var rpcReq struct {
			pluginapi.ExecutorRequest
		}
		if err := json.Unmarshal(request, &rpcReq); err != nil {
			return nil, fmt.Errorf("unmarshal executor count tokens request: %w", err)
		}
		resp, err := handleCountTokens(rpcReq.ExecutorRequest)
		if err != nil {
			return nil, err
		}
		return okEnvelope(resp)

	case pluginabi.MethodQuotaIdentifier:
		return okEnvelope(rpcIdentifierResponse{Identifier: "workbuddy"})

	case pluginabi.MethodQuotaDescribe:
		resp := handleQuotaDescribe()
		return okEnvelope(resp)

	case pluginabi.MethodQuotaFetch:
		var rpcReq struct {
			pluginapi.QuotaFetchRequest
			HostCallbackID string `json:"host_callback_id"`
		}
		if err := json.Unmarshal(request, &rpcReq); err != nil {
			return nil, fmt.Errorf("unmarshal quota fetch request: %w", err)
		}
		m, err := getManifest()
		if err != nil {
			return nil, err
		}
		resp, err := handleQuotaFetch(context.Background(), m, getConfig(), rpcReq.QuotaFetchRequest)
		if err != nil {
			return nil, err
		}
		return okEnvelope(resp)

	case pluginabi.MethodManagementRegister:
		return handleManagementRegister()

	case pluginabi.MethodManagementHandle:
		return handleManagementHandle(request)

	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func okEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{
		OK:    false,
		Error: &envelopeError{Code: code, Message: message},
	})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	response.ptr = C.CBytes(raw)
	response.len = C.size_t(len(raw))
}
