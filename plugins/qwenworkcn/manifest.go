package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

//go:embed data/static-config.json
var defaultStaticConfigBytes []byte

type ManifestV2 struct {
	SchemaVersion int             `json:"schemaVersion"`
	Provenance    Provenance      `json:"provenance"`
	Endpoints     Endpoints       `json:"endpoints"`
	WebOrigin     string          `json:"webOrigin"`
	Profile       Profile         `json:"profile"`
	Models        []ManifestModel `json:"models"`
}

type Provenance struct {
	CaptureUserAgent string `json:"captureUserAgent"`
	GeneratedAt      string `json:"generatedAt"`
	Generator        string `json:"generator"`
	ModelsSource     string `json:"modelsSource"`
}

type Endpoints struct {
	BaseURL            string `json:"baseUrl"`
	ChatCompletions    string `json:"chatCompletions"`
	ChatQuery          string `json:"chatQuery"`
	DeviceTokenPoll    string `json:"deviceTokenPoll"`
	DeviceTokenRefresh string `json:"deviceTokenRefresh"`
	Userinfo           string `json:"userinfo"`
	QuotaUsage         string `json:"quotaUsage"`
}

type Profile struct {
	ClientID    string                       `json:"clientId"`
	RedirectURI string                       `json:"redirectUri"`
	CosyVersion string                       `json:"cosyVersion"`
	Headers     map[string]map[string]string `json:"headers"`
}

type ManifestModel struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	DisplayName            string   `json:"displayName"`
	OwnedBy                string   `json:"ownedBy"`
	ContextLength          int64    `json:"contextLength"`
	MaxCompletionTokens    int64    `json:"maxCompletionTokens"`
	SupportsImages         bool     `json:"supportsImages"`
	SupportsReasoning      bool     `json:"supportsReasoning"`
	OnlyReasoning          bool     `json:"onlyReasoning"`
	SupportedEfforts       []string `json:"supportedEfforts"`
	DefaultReasoningEffort string   `json:"defaultReasoningEffort"`
}

var templateVarRegex = regexp.MustCompile(`\{\{(\w+)\}\}`)

// runtimeTemplateVars 由调用方在请求期注入的变量集合; 加载期遇到它们保持原样。
var runtimeTemplateVars = map[string]struct{}{
	"deviceToken": {},
	"modelKey":    {},
	"modelSource": {},
	"clientType":  {},
	"machineId":   {},
	"requestId":   {},
	"userAgent":   {},
}

func renderHeaderTemplates(headers map[string]string, vars map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		var err error
		out[k] = templateVarRegex.ReplaceAllStringFunc(v, func(whole string) string {
			if err != nil {
				return whole
			}
			name := templateVarRegex.FindStringSubmatch(whole)[1]
			if val, ok := vars[name]; ok {
				return val
			}
			if _, runtime := runtimeTemplateVars[name]; runtime {
				return whole
			}
			err = fmt.Errorf("header %q 含未提供且非运行期的模板变量: %s", k, name)
			return whole
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func parseManifest(data []byte) (*ManifestV2, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("manifest data is empty")
	}

	var m ManifestV2
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("unmarshal static config v2: %w", err)
	}

	if m.SchemaVersion != 2 {
		return nil, fmt.Errorf("unsupported manifest schemaVersion: %d (expected 2)", m.SchemaVersion)
	}
	if m.Endpoints.BaseURL == "" {
		return nil, fmt.Errorf("manifest endpoints.baseUrl is required")
	}
	if m.Endpoints.ChatCompletions == "" {
		return nil, fmt.Errorf("manifest endpoints.chatCompletions is required")
	}
	if m.Endpoints.DeviceTokenPoll == "" {
		return nil, fmt.Errorf("manifest endpoints.deviceTokenPoll is required")
	}
	if m.Endpoints.DeviceTokenRefresh == "" {
		return nil, fmt.Errorf("manifest endpoints.deviceTokenRefresh is required")
	}
	if m.Endpoints.Userinfo == "" {
		return nil, fmt.Errorf("manifest endpoints.userinfo is required")
	}
	if m.Endpoints.QuotaUsage == "" {
		return nil, fmt.Errorf("manifest endpoints.quotaUsage is required")
	}
	if m.Profile.ClientID == "" {
		return nil, fmt.Errorf("manifest profile.clientId is required")
	}
	if m.Profile.CosyVersion == "" {
		return nil, fmt.Errorf("manifest profile.cosyVersion is required")
	}
	if len(m.Models) == 0 {
		return nil, fmt.Errorf("manifest models is required")
	}

	// 七组头一个都不能少: 缺组说明清单与生成器契约漂移, fail-fast。
	// 已知值 (cosyVersion) 加载期渲染; 运行期变量保留给请求期。
	headerGroups := []string{"chat", "base", "openApi", "web", "login.poll", "login.refresh", "login.userinfo"}
	if m.Profile.Headers == nil {
		return nil, fmt.Errorf("manifest profile.headers is required")
	}
	knownVars := map[string]string{"cosyVersion": m.Profile.CosyVersion}
	rendered := make(map[string]map[string]string, len(headerGroups))
	for _, group := range headerGroups {
		raw, ok := m.Profile.Headers[group]
		if !ok {
			return nil, fmt.Errorf("manifest profile.headers missing group %q", group)
		}
		out, err := renderHeaderTemplates(raw, knownVars)
		if err != nil {
			return nil, fmt.Errorf("manifest group %q: %w", group, err)
		}
		rendered[group] = out
	}
	m.Profile.Headers = rendered

	return &m, nil
}

// HeaderGroup 返回渲染后的静态头组; 不存在的组返回错误 (fail-fast, 不静默空集)。
func (m *ManifestV2) HeaderGroup(group string) (map[string]string, error) {
	headers, ok := m.Profile.Headers[group]
	if !ok {
		return nil, fmt.Errorf("manifest header group %q not found", group)
	}
	return headers, nil
}

// RenderHeaderGroup 返回渲染完全部运行期模板变量后的头集合。
// 若仍有任何未渲染的 {{...}} 模板变量，则直接返回错误 (fail-fast，严禁带占位符发往上游)。
func (m *ManifestV2) RenderHeaderGroup(group string, vars map[string]string) (map[string]string, error) {
	headers, err := m.HeaderGroup(group)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		rendered := templateVarRegex.ReplaceAllStringFunc(v, func(whole string) string {
			matches := templateVarRegex.FindStringSubmatch(whole)
			if len(matches) < 2 {
				return whole
			}
			name := matches[1]
			if val, ok := vars[name]; ok {
				return val
			}
			return whole
		})
		if loc := templateVarRegex.FindString(rendered); loc != "" {
			return nil, fmt.Errorf("header group %q: header %q 含有未解析的模板变量 %s", group, k, loc)
		}
		out[k] = rendered
	}
	return out, nil
}

func (m *ManifestV2) BuildURL(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	base := strings.TrimRight(m.Endpoints.BaseURL, "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

// ChatURL = {base}{chat path}?{query}。端点与查询串分离存放, 与参考仓
// buildChatUrl((endpoint, CHAT_PATH)) + 固定 query 的拼装一致。
func (m *ManifestV2) ChatURL() string {
	query := m.Endpoints.ChatQuery
	if query == "" {
		query = "FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"
	}
	return m.BuildURL(m.Endpoints.ChatCompletions) + "?" + query
}

func (m *ManifestV2) WebURL(path string) string {
	base := strings.TrimRight(m.WebOrigin, "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

func findModelConfig(manifest *ManifestV2, modelID string) *ManifestModel {
	for i := range manifest.Models {
		if strings.EqualFold(manifest.Models[i].ID, modelID) {
			return &manifest.Models[i]
		}
	}
	return nil
}
