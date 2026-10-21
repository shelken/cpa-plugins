package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type toolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatMessage struct {
	Role             string     `json:"role"`
	Content          any        `json:"content"`
	Name             string     `json:"name,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	StopReason       string     `json:"stop_reason,omitempty"`
	// Extra 承载官方客户端的 user 消息簿记(agent/startsNewUserRequest/conversationRequestId),
	// 字段名由 static-config 的 body.userMessageFields 声明, 序列化时合并进顶层。
	Extra map[string]any `json:"-"`
}

type chatCompletionRequest struct {
	Model             string        `json:"model"`
	Messages          []chatMessage `json:"messages"`
	Stream            bool          `json:"stream,omitempty"`
	StreamOptions     any           `json:"stream_options,omitempty"`
	Temperature       *float64      `json:"temperature,omitempty"`
	MaxTokens         *int          `json:"max_tokens,omitempty"`
	Tools             []any         `json:"tools,omitempty"`
	ToolChoice        any           `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool         `json:"parallel_tool_calls,omitempty"`
	ReasoningEffort   string        `json:"reasoning_effort,omitempty"`
	ReasoningSummary  string        `json:"reasoning_summary,omitempty"`
	Verbosity         string        `json:"verbosity,omitempty"`
	Store             *bool         `json:"store,omitempty"`
	ExtraVars         any           `json:"extra_vars,omitempty"`
}

func sanitizeMessageContent(content any, supportsImages bool, manifest *ManifestV2) any {
	if content == nil {
		return nil
	}

	if text, ok := content.(string); ok {
		return manifest.SanitizePrompt(text)
	}

	parts, ok := content.([]any)
	if !ok {
		return content
	}

	var cleaned []any
	lastWasOmittedImage := false

	for _, part := range parts {
		partMap, ok := part.(map[string]any)
		if !ok {
			cleaned = append(cleaned, part)
			continue
		}

		pType, _ := partMap["type"].(string)
		if pType == "image" || pType == "image_url" {
			if !supportsImages {
				if !lastWasOmittedImage {
					cleaned = append(cleaned, map[string]any{
						"type": "text",
						"text": "(image omitted: model does not support images)",
					})
					lastWasOmittedImage = true
				}
				continue
			}
		}

		lastWasOmittedImage = false
		if pType == "text" {
			if textVal, ok := partMap["text"].(string); ok {
				partMap["text"] = manifest.SanitizePrompt(textVal)
			}
		}
		cleaned = append(cleaned, partMap)
	}

	return cleaned
}

func cleanMessages(rawMessages []chatMessage, supportsImages bool, manifest *ManifestV2) []chatMessage {
	var cleaned []chatMessage
	droppedToolCallIDs := make(map[string]struct{})

	for i := 0; i < len(rawMessages); i++ {
		msg := rawMessages[i]

		if msg.Role == "assistant" {
			if msg.StopReason == "error" || msg.StopReason == "aborted" {
				for _, tc := range msg.ToolCalls {
					if tc.ID != "" {
						droppedToolCallIDs[tc.ID] = struct{}{}
					}
				}
				continue
			}

			var validToolCalls []toolCall
			for _, tc := range msg.ToolCalls {
				var dummy any
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &dummy); err != nil {
					if tc.ID != "" {
						droppedToolCallIDs[tc.ID] = struct{}{}
					}
					continue
				}
				validToolCalls = append(validToolCalls, tc)
			}
			msg.ToolCalls = validToolCalls

			hasText := false
			if str, ok := msg.Content.(string); ok && strings.TrimSpace(str) != "" {
				hasText = true
			} else if arr, ok := msg.Content.([]any); ok && len(arr) > 0 {
				hasText = true
			}

			if !hasText && strings.TrimSpace(msg.ReasoningContent) == "" && len(msg.ToolCalls) == 0 {
				continue
			}
		}

		if msg.Role == "tool" {
			if _, dropped := droppedToolCallIDs[msg.ToolCallID]; dropped {
				continue
			}
		}

		msg.Content = sanitizeMessageContent(msg.Content, supportsImages, manifest)
		cleaned = append(cleaned, msg)
	}

	var finalMessages []chatMessage
	for i := 0; i < len(cleaned); i++ {
		msg := cleaned[i]
		finalMessages = append(finalMessages, msg)

		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				hasToolResult := false
				for j := i + 1; j < len(cleaned); j++ {
					if cleaned[j].Role == "tool" && cleaned[j].ToolCallID == tc.ID {
						hasToolResult = true
						break
					}
					if cleaned[j].Role == "user" {
						break
					}
				}

				if !hasToolResult {
					finalMessages = append(finalMessages, chatMessage{
						Role:       "tool",
						ToolCallID: tc.ID,
						Content:    "No result provided",
					})
				}
			}
		}
	}

	return finalMessages
}

func findModelConfig(models []ManifestModel, modelID string) *ManifestModel {
	for i := range models {
		if strings.EqualFold(models[i].ID, modelID) {
			return &models[i]
		}
	}
	return nil
}

// messagesToJSON 把消息序列化为上游 JSON; chatMessage.Extra 的键值合并进消息顶层。
func messagesToJSON(messages []chatMessage) []any {
	out := make([]any, len(messages))
	for i, msg := range messages {
		data, err := json.Marshal(msg)
		if err != nil {
			// 结构体序列化不会失败(字段全是可 JSON 类型); 保底原样跳过 Extra
			out[i] = msg
			continue
		}
		var m map[string]any
		if json.Unmarshal(data, &m) != nil || m == nil {
			out[i] = msg
			continue
		}
		for k, v := range msg.Extra {
			if _, exists := m[k]; !exists {
				m[k] = v
			}
		}
		out[i] = m
	}
	return out
}

func prepareChatRequestBody(cfg *PluginConfig, reqPayload []byte, manifest *ManifestV2, profile *ProfileConfig, isStream bool) ([]byte, error) {
	var inReq chatCompletionRequest
	if err := json.Unmarshal(reqPayload, &inReq); err != nil {
		return nil, fmt.Errorf("unmarshal chat completions request: %w", err)
	}

	// 宿主看到的是带前缀的注册 id, 上游只认裸 id, 这里在协议保真的边界上剥掉。
	modelID := manifestModelID(cfg, inReq.Model)
	modelDef := findModelConfig(currentModels(manifest), modelID)
	supportsImages := true
	supportsReasoning := true
	defaultEffort := "high"
	if modelDef != nil {
		supportsImages = modelDef.SupportsImages
		supportsReasoning = modelDef.SupportsReasoning
		if modelDef.DefaultReasoningEffort != "" {
			defaultEffort = modelDef.DefaultReasoningEffort
		}
	}

	inReq.Messages = cleanMessages(inReq.Messages, supportsImages, manifest)

	// 官方 user 消息簿记注入: 字段与取值由配置声明(body.userMessageFields),
	// 占位符引用 values 注册表。此处只执行注入, 不携带任何字段知识。
	if len(profile.Body.UserMessageFields) > 0 {
		vars := manifestTemplateVars(manifest)
		cred := &Credential{}
		// 注入只用到会话级派生值(不含凭据字段), 空凭据即安全; 引用凭据类占位符时会报错拦截。
		values, err := resolveValues(manifest.Values, "", cred, vars)
		if err != nil {
			return nil, fmt.Errorf("resolve user field values: %w", err)
		}
		if err := injectUserMessageFields(inReq.Messages, profile.Body.UserMessageFields, values); err != nil {
			return nil, err
		}
	}

	outMap := make(map[string]any)
	outMap["model"] = modelID
	outMap["messages"] = messagesToJSON(inReq.Messages)
	outMap["stream"] = isStream
	if isStream {
		outMap["stream_options"] = map[string]any{
			"include_usage": true,
		}
	}

	if inReq.Temperature != nil {
		outMap["temperature"] = *inReq.Temperature
	}
	if inReq.MaxTokens != nil {
		outMap["max_tokens"] = *inReq.MaxTokens
	}
	if len(inReq.Tools) > 0 {
		outMap["tools"] = inReq.Tools
		if inReq.ToolChoice != nil {
			outMap["tool_choice"] = inReq.ToolChoice
		}
		if inReq.ParallelToolCalls != nil {
			outMap["parallel_tool_calls"] = *inReq.ParallelToolCalls
		}
	}

	if supportsReasoning {
		effort := inReq.ReasoningEffort
		if effort == "" {
			effort = defaultEffort
		}
		outMap["reasoning_effort"] = effort
		outMap["reasoning_summary"] = "auto"
	}

	for k, v := range profile.Body.Set {
		outMap[k] = v
	}

	if profile.Body.ExtraVars {
		outMap["extra_vars"] = map[string]any{}
	}

	for _, omitKey := range profile.Body.Omit {
		delete(outMap, omitKey)
	}

	if len(inReq.Tools) == 0 {
		delete(outMap, "tools")
		delete(outMap, "tool_choice")
		delete(outMap, "parallel_tool_calls")
	}

	return json.Marshal(outMap)
}

// 对话请求不挂整体超时: 长思考流的响应体要读几十秒到几分钟, http.Client.Timeout
// 会在中途掐断连接, 表现成尾部帧与 usage 整段消失。连接阶段仍保留上限, 响应体
// 读取交给上游自然结束。
var chatHTTPClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 60 * time.Second
	return &http.Client{Transport: transport}
}()

// 宿主在鉴权选择阶段算好的规范会话 id 的元数据键, 定义见
// sdk/cliproxy/executor.CanonicalSessionIDMetadataKey。
const canonicalSessionIDKey = "canonical_session_id"

// 会话级标识必须在同一轮对话里保持不变, 否则上游把每一轮都当成新会话, 前缀缓存
// 无法复用。宿主已经把规范会话 id 算好并放进元数据, 直接用它的取值就能和客户端
// 保持一致; 元数据缺失时按会话内不变的部分兜底。
func sessionSeed(req pluginapi.ExecutorRequest, payload []byte) string {
	if raw, ok := req.Metadata[canonicalSessionIDKey]; ok {
		if seed, ok := raw.(string); ok && strings.TrimSpace(seed) != "" {
			return seed
		}
	}

	var inReq chatCompletionRequest
	if err := json.Unmarshal(payload, &inReq); err != nil {
		return req.AuthID
	}

	var sb strings.Builder
	sb.WriteString(req.AuthID)
	appendContent := func(msg chatMessage) {
		raw, err := json.Marshal(msg.Content)
		if err != nil {
			return
		}
		sb.Write(raw)
	}
	for _, msg := range inReq.Messages {
		if msg.Role == "system" || msg.Role == "developer" {
			appendContent(msg)
		}
	}
	for _, msg := range inReq.Messages {
		if msg.Role == "user" {
			appendContent(msg)
			break
		}
	}

	return sb.String()
}

// buildChatHeaders 按配置头组渲染 chat 请求头。
// 身份头的名字与取值全部来自 manifest(profiles.<档>.headers.chat + values 注册表),
// 本函数只负责求值与渲染; 取值语义见 identity.go。
func buildChatHeaders(manifest *ManifestV2, profile *ProfileConfig, cred *Credential, seed string) (http.Header, error) {
	values, err := resolveValues(manifest.Values, seed, cred, manifestTemplateVars(manifest))
	if err != nil {
		return nil, err
	}
	group := profile.Headers["chat"]
	if len(group) == 0 {
		return nil, fmt.Errorf("profile %q headers.chat is empty", "chat")
	}
	return renderHeaders(group, values)
}

func handleExecuteStream(ctx context.Context, manifest *ManifestV2, cfg *PluginConfig, req pluginapi.ExecutorRequest, streamID string) (map[string]any, error) {
	cred, err := parseCredential(req.StorageJSON)
	if err != nil {
		return nil, fmt.Errorf("parse storage json for stream execute: %w", err)
	}

	profileName := cfg.IdentityProfile
	if profileName == "" {
		profileName = string(ProfileDesktop)
	}
	profile, err := manifest.GetProfile(profileName)
	if err != nil {
		return nil, err
	}

	payload := req.Payload
	if len(payload) == 0 {
		payload = req.OriginalRequest
	}

	bodyBytes, err := prepareChatRequestBody(cfg, payload, manifest, profile, true)
	if err != nil {
		return nil, fmt.Errorf("prepare stream chat body: %w", err)
	}

	seedPayload := req.OriginalRequest
	if len(seedPayload) == 0 {
		seedPayload = payload
	}

	chatURL := manifest.BuildURL(manifest.Endpoints.ChatCompletions)
	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, chatURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create stream chat request: %w", err)
	}

	headers, err := buildChatHeaders(manifest, profile, cred, sessionSeed(req, seedPayload))
	if err != nil {
		return nil, fmt.Errorf("build stream chat headers: %w", err)
	}
	httpReq.Header = headers

	go func() {
		// 宿主侧 close 是幂等的 (stream_bridge.go:231 取走条目后第二次直接返回), 所以这里
		// 不会盖掉错误原因; 但错误路径先关一次、defer 再关一次等于每次失败多发一次无用 RPC。
		// once 守卫把「一条 stream 只关一次」写进代码: 首个原因生效, defer 只兜住正常收尾。
		var closeOnce sync.Once
		closeStream := func(errMsg string) {
			closeOnce.Do(func() { _ = callHostStreamClose(streamID, errMsg) })
		}
		defer closeStream("")

		resp, errDo := chatHTTPClient.Do(httpReq)
		if errDo != nil {
			closeStream(errDo.Error())
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			errBytes, _ := io.ReadAll(resp.Body)
			closeStream(fmt.Sprintf("upstream chat error %d: %s", resp.StatusCode, string(errBytes)))
			return
		}

		reader := bufio.NewReader(resp.Body)
		for {
			line, errRead := reader.ReadBytes('\n')
			if len(line) > 0 {
				lineStr := string(line)
				trimmed := strings.TrimSpace(lineStr)

				if strings.HasPrefix(trimmed, ":") {
					continue
				}

				if trimmed == "data: [DONE]" {
					break
				}

				if payload, ok := ssePayload(trimmed); ok {
					emitErr := callHostStreamEmit(streamID, []byte(payload))
					if emitErr != nil {
						break
					}
				}
			}

			if errRead != nil {
				if errRead != io.EOF {
					closeStream(errRead.Error())
				}
				break
			}
		}
	}()

	return map[string]any{
		"headers": map[string][]string{
			"content-type": {"text/event-stream; charset=utf-8"},
		},
	}, nil
}

func handleExecute(ctx context.Context, manifest *ManifestV2, cfg *PluginConfig, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	cred, err := parseCredential(req.StorageJSON)
	if err != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("parse storage json for execute: %w", err)
	}

	profileName := cfg.IdentityProfile
	if profileName == "" {
		profileName = string(ProfileDesktop)
	}
	profile, err := manifest.GetProfile(profileName)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}

	payload := req.Payload
	if len(payload) == 0 {
		payload = req.OriginalRequest
	}

	// 上游只接受流式对话, 发 stream:false 会被拒 (400/11101) 并把凭据打成不可用,
	// 因此这里始终按流式请求上游, 在插件内聚合成单条完整响应再交给宿主。
	bodyBytes, err := prepareChatRequestBody(cfg, payload, manifest, profile, true)
	if err != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("prepare non-stream chat body: %w", err)
	}

	seedPayload := req.OriginalRequest
	if len(seedPayload) == 0 {
		seedPayload = payload
	}

	chatURL := manifest.BuildURL(manifest.Endpoints.ChatCompletions)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("create non-stream chat request: %w", err)
	}

	headers, err := buildChatHeaders(manifest, profile, cred, sessionSeed(req, seedPayload))
	if err != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("build chat headers: %w", err)
	}
	httpReq.Header = headers

	resp, err := chatHTTPClient.Do(httpReq)
	if err != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("execute non-stream chat request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("read non-stream chat response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("upstream chat error %d: %s", resp.StatusCode, string(respBytes))
	}

	aggregated, err := aggregateChatStream(respBytes)
	if err != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("aggregate upstream stream: %w", err)
	}

	return pluginapi.ExecutorResponse{
		Payload: aggregated,
	}, nil
}

// 宿主会自己给每段载荷补上 SSE 的 data: 前缀, 插件交出去的必须是裸载荷,
// 否则客户端收到的是 "data: data: {...}"。
func ssePayload(trimmedLine string) (string, bool) {
	if !strings.HasPrefix(trimmedLine, "data:") {
		return "", false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(trimmedLine, "data:"))
	if payload == "" {
		return "", false
	}
	return payload, true
}

type chatCompletionMessage struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCall `json:"tool_calls,omitempty"`
}

type chatCompletionChoice struct {
	Index        int                   `json:"index"`
	Message      chatCompletionMessage `json:"message"`
	FinishReason string                `json:"finish_reason"`
}

type chatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []chatCompletionChoice `json:"choices"`
	Usage   json.RawMessage        `json:"usage,omitempty"`
}

// 把上游的流式增量拼回一条完整响应。工具调用按索引合并, 参数分片直接续接。
func aggregateChatStream(raw []byte) ([]byte, error) {
	out := chatCompletionResponse{Object: "chat.completion"}
	var content, reasoning strings.Builder
	calls := make(map[int]*toolCall)
	var order []int
	finish := ""
	seen := false

	for _, line := range strings.Split(string(raw), "\n") {
		payload, ok := ssePayload(strings.TrimSpace(line))
		if !ok || payload == "[DONE]" {
			continue
		}

		var chunk struct {
			ID      string `json:"id"`
			Created int64  `json:"created"`
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content          string     `json:"content"`
					ReasoningContent string     `json:"reasoning_content"`
					ToolCalls        []toolCall `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage json.RawMessage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		seen = true

		if chunk.ID != "" {
			out.ID = chunk.ID
		}
		if chunk.Created != 0 {
			out.Created = chunk.Created
		}
		if chunk.Model != "" {
			out.Model = chunk.Model
		}

		for _, choice := range chunk.Choices {
			content.WriteString(choice.Delta.Content)
			reasoning.WriteString(choice.Delta.ReasoningContent)
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
			for _, call := range choice.Delta.ToolCalls {
				index := 0
				if call.Index != nil {
					index = *call.Index
				}
				existing, ok := calls[index]
				if !ok {
					clone := call
					clone.Index = nil
					calls[index] = &clone
					order = append(order, index)
					continue
				}
				if call.ID != "" {
					existing.ID = call.ID
				}
				if call.Type != "" {
					existing.Type = call.Type
				}
				if call.Function.Name != "" {
					existing.Function.Name = call.Function.Name
				}
				existing.Function.Arguments += call.Function.Arguments
			}
		}

		if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
			out.Usage = chunk.Usage
		}
	}

	if !seen {
		return nil, fmt.Errorf("上游流式响应里没有可解析的帧")
	}

	sort.Ints(order)
	var toolCalls []toolCall
	for _, index := range order {
		toolCalls = append(toolCalls, *calls[index])
	}

	out.Choices = []chatCompletionChoice{{
		Index: 0,
		Message: chatCompletionMessage{
			Role:             "assistant",
			Content:          content.String(),
			ReasoningContent: reasoning.String(),
			ToolCalls:        toolCalls,
		},
		FinishReason: finish,
	}}

	return json.Marshal(out)
}

func handleCountTokens(req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	payload := req.Payload
	if len(payload) == 0 {
		payload = req.OriginalRequest
	}

	count := len(payload) / 4
	if count <= 0 && len(payload) > 0 {
		count = 1
	}

	out := fmt.Sprintf(`{"total_tokens":%d,"input_tokens":%d}`, count, count)
	return pluginapi.ExecutorResponse{
		Payload: []byte(out),
	}, nil
}
