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

var cliVersionRegex = regexp.MustCompile(`CLI/([0-9.]+)`)

type ManifestV2 struct {
	SchemaVersion   int                       `json:"schemaVersion"`
	Provenance      ProvenanceConfig          `json:"provenance"`
	Endpoints       EndpointsConfig           `json:"endpoints"`
	Profiles        map[string]ProfileConfig  `json:"profiles"`
	IdentityHeaders map[string][]string       `json:"identityHeaders"`
	RequestBodies   map[string]map[string]any `json:"requestBodies"`
	Sanitizations   []SanitizationRule        `json:"sanitizations"`
	Models          []ManifestModel           `json:"models"`
}

type ProvenanceConfig struct {
	CaptureSession       string `json:"captureSession"`
	CaptureUserAgent     string `json:"captureUserAgent"`
	CaptureClientVersion string `json:"captureClientVersion"`
	GeneratedAt          string `json:"generatedAt"`
	Generator            string `json:"generator"`
}

type EndpointsConfig struct {
	BaseURL         string `json:"baseUrl"`
	ChatCompletions string `json:"chatCompletions"`
	AuthState       string `json:"authState"`
	AuthToken       string `json:"authToken"`
	LoginAccount    string `json:"loginAccount"`
	TokenRefresh    string `json:"tokenRefresh"`
	Accounts        string `json:"accounts"`
	UserResource    string `json:"userResource"`
	ModelsConfig    string `json:"modelsConfig"`
}

type ProfileConfig struct {
	Login   ProfileLoginConfig           `json:"login"`
	Headers map[string]map[string]string `json:"headers"`
	Body    ProfileBodyConfig            `json:"body"`
}

type ProfileLoginConfig struct {
	Platform string `json:"platform"`
}

type ProfileBodyConfig struct {
	Omit      []string       `json:"omit"`
	Set       map[string]any `json:"set"`
	ExtraVars bool           `json:"extraVars"`
}

type SanitizationRule struct {
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
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
	CanDisableThinking     *bool    `json:"canDisableThinking,omitempty"`
	SupportedEfforts       []string `json:"supportedEfforts"`
	DefaultReasoningEffort string   `json:"defaultReasoningEffort"`
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
	if m.Endpoints.AuthState == "" {
		return nil, fmt.Errorf("manifest endpoints.authState is required")
	}
	if m.Endpoints.AuthToken == "" {
		return nil, fmt.Errorf("manifest endpoints.authToken is required")
	}
	if m.Endpoints.TokenRefresh == "" {
		return nil, fmt.Errorf("manifest endpoints.tokenRefresh is required")
	}
	if m.Endpoints.UserResource == "" {
		return nil, fmt.Errorf("manifest endpoints.userResource is required")
	}

	if m.Profiles == nil {
		return nil, fmt.Errorf("manifest profiles map is required")
	}
	if _, ok := m.Profiles[string(ProfileDesktop)]; !ok {
		return nil, fmt.Errorf("manifest missing profile %q", ProfileDesktop)
	}
	if _, ok := m.Profiles[string(ProfileCLI)]; !ok {
		return nil, fmt.Errorf("manifest missing profile %q", ProfileCLI)
	}

	appVersion := m.Provenance.CaptureClientVersion
	cliVersion := ""
	if match := cliVersionRegex.FindStringSubmatch(m.Provenance.CaptureUserAgent); len(match) > 1 {
		cliVersion = match[1]
	}

	for pKey, prof := range m.Profiles {
		for hGroup, hMap := range prof.Headers {
			newHMap := make(map[string]string, len(hMap))
			for k, v := range hMap {
				if appVersion != "" {
					v = strings.ReplaceAll(v, "{{appVersion}}", appVersion)
				}
				if cliVersion != "" {
					v = strings.ReplaceAll(v, "{{cliVersion}}", cliVersion)
				}
				newHMap[k] = v
			}
			prof.Headers[hGroup] = newHMap
		}
		m.Profiles[pKey] = prof
	}

	return &m, nil
}

func (m *ManifestV2) GetProfile(profileName string) (*ProfileConfig, error) {
	if m == nil || m.Profiles == nil {
		return nil, fmt.Errorf("manifest profiles not initialized")
	}
	prof, ok := m.Profiles[profileName]
	if !ok {
		return nil, fmt.Errorf("profile %q not found in manifest", profileName)
	}
	return &prof, nil
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

func (m *ManifestV2) SanitizePrompt(text string) string {
	res := text
	for _, rule := range m.Sanitizations {
		if rule.Pattern != "" {
			res = strings.ReplaceAll(res, rule.Pattern, rule.Replacement)
		}
	}
	return res
}
