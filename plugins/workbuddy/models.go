package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// 模型可见性分两层:
//   - 静态清单 (data/static-config.json): 生成器导出时已过滤, 插件侧不重复过滤
//     (TestManifestModelsAllRegistered 钉住这一点)。
//   - 动态清单 (/v3/config): 数据源是上游全量详情, 无生成器把关, 过滤责任在插件侧,
//     即下方 excludeModelReasons 黑名单。黑名单只剔除确定不可用于聊天的模型,
//     上游新增模型不在表内即自动暴露; 旧代模型保留暴露 (与 pi 侧 models-filter.ts
//     的旧前缀排除不同, 服务器网关多暴露无副作用)。
//
// 动态清单在凭据刷新/登录成功时替换内存 manifest, 宿主随后写回 auth 文件,
// watcher 检测 auth 目录变化后重拉 MethodModelStatic, 新模型表由此生效,
// 插件无需任何主动通知。
func mapManifestModels(cfg *PluginConfig, manifestModels []ManifestModel) []pluginapi.ModelInfo {
	models := make([]pluginapi.ModelInfo, 0, len(manifestModels))
	for _, m := range manifestModels {
		modalities := []string{"text"}
		if m.SupportsImages {
			modalities = append(modalities, "image")
		}

		var thinking *pluginapi.ThinkingSupport
		if m.SupportsReasoning {
			canOff := !m.OnlyReasoning
			if m.CanDisableThinking != nil {
				canOff = *m.CanDisableThinking
			}
			efforts := m.SupportedEfforts
			if len(efforts) == 0 && m.DefaultReasoningEffort != "" {
				efforts = []string{m.DefaultReasoningEffort}
			}
			thinking = &pluginapi.ThinkingSupport{
				ZeroAllowed:    canOff,
				DynamicAllowed: false,
				Levels:         efforts,
			}
		}

		displayName := m.DisplayName
		if displayName == "" {
			displayName = m.Name
		}
		if displayName == "" {
			displayName = m.ID
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		ownedBy := m.OwnedBy
		if ownedBy == "" {
			ownedBy = "workbuddy"
		}

		models = append(models, pluginapi.ModelInfo{
			ID:                         registeredModelID(cfg, m.ID),
			Object:                     "model",
			Name:                       name,
			DisplayName:                displayName,
			Description:                creditDescription(m.Credits),
			OwnedBy:                    ownedBy,
			ContextLength:              m.ContextLength,
			MaxCompletionTokens:        m.MaxCompletionTokens,
			SupportedInputModalities:   modalities,
			SupportedOutputModalities:  []string{"text"},
			SupportedGenerationMethods: []string{"chat"},
			Thinking:                   thinking,
		})
	}
	return models
}

// creditDescription 把积分倍率放进模型描述, 宿主 ModelInfo 无专字段
// (sdk/pluginapi/types.go ModelInfo)。倍率是账号计划的实际计费口径,
// 用户选模型时最需要的成本信号。
func creditDescription(credits string) string {
	if credits == "" {
		return ""
	}
	if isFreeCredits(credits) {
		return "积分 " + credits + "（免费）"
	}
	return "积分 " + credits
}

func isFreeCredits(credits string) bool {
	f, err := strconv.ParseFloat(strings.TrimPrefix(strings.ToLower(credits), "x"), 64)
	return err == nil && f == 0
}

// excludeModelReasons 动态清单黑名单: /v3/config 详情中确定不可用于 chat 的模型。
// 判据来自 2026-10-19 抓包 (客户端 5.8.3): 这些模型或带有明确的非 chat 能力标记
// (视频生成 / 无上下文窗口 / 不支持工具调用), 或是同一模型在其他云厂商的渠道副本。
// 任何剔除都必须写进本表并带理由, 未列出的上游模型一律暴露。
//
// 与 pi-codebuddy-provider 的 scripts/static/model-kind.ts 保持同一判据;
// 那里管静态产物的生成, 这里管插件直连上游时的动态清单。
var excludeModelReasons = map[string]string{
	// IDE 补全 / 专用小模型: 无上下文窗口, 不支持工具调用
	"codewise-completions":             "IDE 代码补全, 走补全协议",
	"codewise-jump":                    "IDE 代码跳转",
	"codewise-nes-a4-027-aide":         "IDE 补全变体",
	"codewise-rewrite":                 "IDE 代码重写",
	"completion-gf":                    "FIM 补全",
	"deepseek-v3-0324-taco-completion": "补全变体, 且 name 与 deepseek-v3-0324 撞名",
	"hunyuan-3b":                       "紧凑型补全模型, 无上下文窗口",
	"hunyuan-7b-dense":                 "紧凑型补全模型, 无上下文窗口",

	// 媒体生成: 不是对话模型
	"hunyuan-image-alpha":      "图像生成, 非视觉理解",
	"hunyuan-image-alpha-edit": "图像编辑",
	"kling-video-v3":           "视频生成",
	"minimax-video-h3":         "视频生成",

	// 档位路由与 IDE 内部别名: 不是可独立调用的模型
	"balanced-model": "档位路由别名, 非真实模型",
	"deep-model":     "档位路由别名, 非真实模型",
	"fast-model":     "档位路由别名, 非真实模型",
	"default-1.1":    "IDE 内部 Claude 别名",
	"default-1.2":    "IDE 内部 Claude 别名",

	// 旧模型: 上游未给 supportsToolCall, 无法承载 agent 工具链
	"deepseek-r1-0528": "旧版推理模型, 无工具调用能力",
	"deepseek-v3-0324": "旧版模型, 无工具调用能力",

	// 渠道副本: 同一模型在其他云厂商的部署, 有规范入口时只保留规范 id
	"deepseek-v3-1-lkeap":    "渠道副本 (lkeap), 存在规范入口 deepseek-v3-1",
	"deepseek-v3-0324-lkeap": "渠道副本 (lkeap)",
	"deepseek-r1-0528-lkeap": "渠道副本 (lkeap)",
	"deepseek-v3-1-volc":     "渠道副本 (volc), 存在规范入口 deepseek-v3-1",
	"deepseek-v3-2-volc":     "渠道副本 (volc)",
	"kimi-k2-instruct-taiji": "渠道副本 (taiji)",
}

// mediaTags 出现即代表生成媒体, 不是 chat 模型。
var mediaTags = map[string]bool{
	"text-to-image":  true,
	"image-to-image": true,
	"text-to-video":  true,
	"image-to-video": true,
}

// nonChatReason 按上游能力特征判定模型不可用于 chat, 返回空串表示可用。
// 与 pi-codebuddy-provider 的 scripts/static/model-kind.ts 同一顺序与语义:
// 特征优先于 id 黑名单, 这样上游新增的媒体模型无需先改表就能被拦住。
func nonChatReason(disabled, hasVideoGeneration bool, tags []string, maxInputTokens int64, supportsToolCall *bool) string {
	if disabled {
		return "上游标记 disabled"
	}
	if hasVideoGeneration {
		return "视频生成模型"
	}
	for _, tag := range tags {
		if mediaTags[tag] {
			return "媒体模型 (" + tag + ")"
		}
	}
	if maxInputTokens == 0 {
		return "缺 maxInputTokens, 用途未知"
	}
	// 上游只对支持的模型显式给 true; 旧模型字段缺失等同不支持
	if supportsToolCall == nil || !*supportsToolCall {
		return "不支持工具调用"
	}
	return ""
}

// v3ConfigResponse 是 GET /v3/config 的响应形状 (2026-09-24 抓包)。
// Agents (agents[].name/models) 记录各 agent 引用的模型子集, 当前黑名单
// 方案不使用它, 保留以记录上游形状; models 是全量详情。
type v3ConfigResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Agents []struct {
			Name   string   `json:"name"`
			Models []string `json:"models"`
		} `json:"agents"`
		Models []struct {
			ID                string          `json:"id"`
			Name              string          `json:"name"`
			MaxInputTokens    int64           `json:"maxInputTokens"`
			MaxOutputTokens   int64           `json:"maxOutputTokens"`
			SupportsImages    *bool           `json:"supportsImages"`
			SupportsReasoning *bool           `json:"supportsReasoning"`
			SupportsToolCall  *bool           `json:"supportsToolCall"`
			OnlyReasoning     bool            `json:"onlyReasoning"`
			Disabled          bool            `json:"disabled"`
			VideoGeneration   json.RawMessage `json:"videoGeneration"`
			Tags              []string        `json:"tags"`
			Credits           json.RawMessage `json:"credits"`
			Reasoning         struct {
				SupportedEfforts   []string `json:"supportedEfforts"`
				CanDisableThinking *bool    `json:"canDisableThinking"`
				DefaultEffort      string   `json:"defaultEffort"`
			} `json:"reasoning"`
		} `json:"models"`
	} `json:"data"`
}

// parseModelsConfig 把 /v3/config 响应归一成清单模型:
// 黑名单剔除 + supportsImages 三态归一 (缺失视为 true, 防零值误关图片链路)
// + 同名模型消歧。上游 supportsReasoning 缺失时同样视为 true。
func parseModelsConfig(body []byte) ([]ManifestModel, error) {
	var resp v3ConfigResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal v3/config: %w", err)
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("v3/config upstream error (code %d): %s", resp.Code, resp.Msg)
	}

	out := make([]ManifestModel, 0, len(resp.Data.Models))
	for _, m := range resp.Data.Models {
		if reason := nonChatReason(m.Disabled, len(m.VideoGeneration) > 0, m.Tags, m.MaxInputTokens, m.SupportsToolCall); reason != "" {
			log.Printf("workbuddy: 跳过非 chat 模型 %s (%s)", m.ID, reason)
			continue
		}
		if _, excluded := excludeModelReasons[m.ID]; excluded {
			continue
		}
		mm := ManifestModel{
			ID:                     m.ID,
			Name:                   m.Name,
			DisplayName:            m.Name,
			OwnedBy:                "workbuddy",
			ContextLength:          m.MaxInputTokens,
			MaxCompletionTokens:    m.MaxOutputTokens,
			SupportsImages:         m.SupportsImages == nil || *m.SupportsImages,
			SupportsReasoning:      m.SupportsReasoning == nil || *m.SupportsReasoning,
			OnlyReasoning:          m.OnlyReasoning,
			SupportedEfforts:       m.Reasoning.SupportedEfforts,
			DefaultReasoningEffort: m.Reasoning.DefaultEffort,
			Credits:                parseCredits(m.Credits),
		}
		if m.Reasoning.CanDisableThinking != nil {
			mm.CanDisableThinking = m.Reasoning.CanDisableThinking
		}
		out = append(out, mm)
	}
	disambiguateNames(out)
	return out, nil
}

// parseCredits 归一上游积分倍率: "x0.00" / "x0.18 credits" / "x0.18 CREDITS" / 0
// → "x0.00" 形态。null 或读不出的值返回空串, 既不标免费也不显示,
// 与 magpie wbFreeCredits 同一保守语义: 读不出的倍率不当作免费。
func parseCredits(raw json.RawMessage) string {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, "credits")
	s = strings.TrimPrefix(strings.TrimSpace(s), "x")
	if s == "" || s == "null" {
		return ""
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("x%.2f", f)
}

// disambiguateNames 给同名模型的 DisplayName 加 id 差量后缀:
// hy3, hy3-b, hy3-x → Hy3, Hy3 (B), Hy3 (X); 无公共前缀时退化为完整 id。
// 上游详情里 hy3/hy3-x 的 name 同为 "Hy3", 不消歧则客户端无法区分。
func disambiguateNames(models []ManifestModel) {
	first := map[string]string{}
	for i := range models {
		m := &models[i]
		base := m.DisplayName
		if base == "" {
			base = m.Name
		}
		if base == "" {
			base = m.ID
		}
		firstID, dup := first[base]
		if !dup {
			first[base] = m.ID
			continue
		}
		tag := m.ID
		if rest, ok := strings.CutPrefix(m.ID, firstID+"-"); ok && rest != "" {
			tag = strings.ToUpper(rest)
		}
		m.DisplayName = base + " (" + tag + ")"
	}
}

// fetchModelsConfig 请求 /v3/config, 头组按 2026-09-24 抓包: 客户端 UA、
// 账号 Bearer、X-User-Id/X-Domain/X-Product, 标记 XHR。
func fetchModelsConfig(ctx context.Context, manifest *ManifestV2, profile *ProfileConfig, cred *Credential) ([]byte, error) {
	endpoint := manifest.Endpoints.ModelsConfig
	if endpoint == "" {
		return nil, fmt.Errorf("manifest endpoints.modelsConfig is not set")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifest.BuildURL(endpoint), nil)
	if err != nil {
		return nil, fmt.Errorf("create models config request: %w", err)
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if ua := profile.Headers["chat"]["User-Agent"]; ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken())
	req.Header.Set("X-User-Id", cred.UserID)
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	domain := cred.Domain
	if domain == "" {
		if u, err := url.Parse(req.URL.String()); err == nil && u.Host != "" {
			domain = u.Host
		}
	}
	if domain != "" {
		req.Header.Set("X-Domain", domain)
	}
	req.Header.Set("X-Request-ID", generateRequestID())

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute models config request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read models config response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models config upstream returned status %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

// modelsConfigTimeout 是单次 /v3/config 拉取的独立上界: 触发点在凭据刷新的
// 同步路径里, 宿主传来的 ctx 是 Background 无法取消, 不加界时上游挂住会把
// 刷新响应拖到 httpClient.Timeout=45s, 连带推迟其余凭据的轮转。
const modelsConfigTimeout = 10 * time.Second

// applyLiveModels 拉取动态模型清单并存入独立的 liveModels 状态。
// 不替换 cachedManifest: 宿主在 auth 文件变化时会调用插件 Reconfigure,
// 后者会丢弃静态 manifest 缓存, 若动态清单挂在它身上, 每次凭据落盘
// 都会把模型表打回静态清单。两条生命周期必须分离:
//   - cachedManifest: 静态清单缓存, Reconfigure 可失效重建
//   - liveModels: 动态清单, 一旦拉取即存活到下次拉取/进程结束
// 失败只记日志: 请求路径与静态清单不受影响, 下一轮凭据刷新 (小时级)
// 自然重试, 无需退避计时器。
func applyLiveModels(ctx context.Context, manifest *ManifestV2, profile *ProfileConfig, cred *Credential) bool {
	ctx, cancel := context.WithTimeout(ctx, modelsConfigTimeout)
	defer cancel()
	body, err := fetchModelsConfig(ctx, manifest, profile, cred)
	if err != nil {
		log.Printf("[workbuddy] dynamic model list unavailable: %v", err)
		return false
	}
	models, err := parseModelsConfig(body)
	if err != nil {
		log.Printf("[workbuddy] parse dynamic model list: %v", err)
		return false
	}
	if len(models) == 0 {
		log.Printf("[workbuddy] dynamic model list empty, keeping current models")
		return false
	}

	pluginStateMu.Lock()
	liveModels = models
	pluginStateMu.Unlock()
	log.Printf("[workbuddy] live model list applied: %d models", len(models))
	return true
}

// currentModels 返回当前生效的模型表: 动态清单优先, 未拉取时静态清单兜底。
// 读侧 (MethodModelStatic / executor 的模型能力判定) 都经此取数。
func currentModels(manifest *ManifestV2) []ManifestModel {
	pluginStateMu.RLock()
	live := liveModels
	pluginStateMu.RUnlock()
	if live != nil {
		return live
	}
	return manifest.Models
}
