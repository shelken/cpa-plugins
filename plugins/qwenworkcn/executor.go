package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
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
	Contents         []any      `json:"contents,omitempty"`
	Name             string     `json:"name,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	StopReason       string     `json:"stop_reason,omitempty"`
}

// chatCompletionRequest 只声明上游信封确实有落点的字段。
// 其余 OpenAI 采样参数 (temperature / verbosity / store / stream_options) 在本协议里没有位置:
// 官方客户端抓包的 parameters 只有 context_length 与 max_tokens, 静态清单也没有请求体白名单,
// 凭空加字段等于发明协议。客户端仍可发送它们 (JSON 解码会忽略未知字段), 插件只是不承诺效果,
// 因此这里不声明 —— 声明了却又读不到, 会让下一个维护者以为它们已被支持。
type chatCompletionRequest struct {
	Model             string        `json:"model"`
	Messages          []chatMessage `json:"messages"`
	Stream            bool          `json:"stream,omitempty"`
	MaxTokens         *int          `json:"max_tokens,omitempty"`
	Tools             []any         `json:"tools,omitempty"`
	ToolChoice        any           `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool         `json:"parallel_tool_calls,omitempty"`
	ReasoningEffort   string        `json:"reasoning_effort,omitempty"`
	ReasoningSummary  string        `json:"reasoning_summary,omitempty"`
	ExtraVars         any           `json:"extra_vars,omitempty"`
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func sanitizeMessageContent(content any, supportsImages bool) any {
	if content == nil {
		return nil
	}

	if text, ok := content.(string); ok {
		return text
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
		cleaned = append(cleaned, partMap)
	}

	return cleaned
}

func cleanMessages(rawMessages []chatMessage, supportsImages bool) []chatMessage {
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
				// 无参函数的 arguments 是空串, 不是坏 JSON; 丢掉它会让随后的 tool 结果帧一起被剔除。
				if args := strings.TrimSpace(tc.Function.Arguments); args != "" {
					var dummy any
					if err := json.Unmarshal([]byte(args), &dummy); err != nil {
						if tc.ID != "" {
							droppedToolCallIDs[tc.ID] = struct{}{}
						}
						continue
					}
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

		msg.Content = sanitizeMessageContent(msg.Content, supportsImages)
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

func withUserContentsDualWrite(messages []chatMessage) []chatMessage {
	out := make([]chatMessage, len(messages))
	for i, m := range messages {
		if m.Role == "user" {
			if text, ok := m.Content.(string); ok && len(m.Contents) == 0 {
				m.Contents = []any{
					map[string]any{
						"type": "text",
						"text": text,
					},
				}
			}
		}
		out[i] = m
	}
	return out
}

const canonicalSessionIDKey = "canonical_session_id"

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

func deriveUUID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	raw := [16]byte{}
	copy(raw[:], sum[:16])
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

var chatHTTPClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 60 * time.Second
	return &http.Client{Transport: transport}
}()

func buildChatRequestBody(
	cfg *PluginConfig,
	reqPayload []byte,
	manifest *ManifestV2,
	sessionID string,
) (string, []byte, error) {
	var inReq chatCompletionRequest
	if err := json.Unmarshal(reqPayload, &inReq); err != nil {
		return "", nil, fmt.Errorf("unmarshal chat completions request: %w", err)
	}

	modelID := manifestModelID(cfg, inReq.Model)
	modelDef := findModelConfig(manifest, modelID)
	supportsImages := true
	supportsReasoning := true
	var maxOutputTokens int64 = 128000
	displayName := modelID
	var contextLength int64 = 1000000
	defaultEffort := "high"

	if modelDef != nil {
		supportsImages = modelDef.SupportsImages
		supportsReasoning = modelDef.SupportsReasoning
		if modelDef.MaxCompletionTokens > 0 {
			maxOutputTokens = modelDef.MaxCompletionTokens
		}
		if modelDef.DisplayName != "" {
			displayName = modelDef.DisplayName
		}
		if modelDef.ContextLength > 0 {
			contextLength = modelDef.ContextLength
		}
		if modelDef.DefaultReasoningEffort != "" {
			defaultEffort = modelDef.DefaultReasoningEffort
		}
	}

	cleanedMsgs := cleanMessages(inReq.Messages, supportsImages)
	dualWrittenMsgs := withUserContentsDualWrite(cleanedMsgs)

	parameters := make(map[string]any)
	maxTokens := maxOutputTokens
	if inReq.MaxTokens != nil && *inReq.MaxTokens > 0 {
		maxTokens = int64(*inReq.MaxTokens)
	}
	parameters["max_tokens"] = maxTokens

	effort := inReq.ReasoningEffort
	if effort == "" {
		effort = defaultEffort
	}
	parameters["reasoning_effort"] = effort
	parameters["context_length"] = contextLength

	tools := inReq.Tools
	if tools == nil {
		tools = []any{}
	}
	if len(tools) > 0 {
		// 客户端未指定时保持官方客户端的 auto 基准, 指定时必须透传其意图 (点名函数、required、none)
		if inReq.ToolChoice == nil {
			parameters["tool_choice"] = "auto"
		} else {
			parameters["tool_choice"] = inReq.ToolChoice
		}
		if inReq.ParallelToolCalls != nil {
			parameters["parallel_tool_calls"] = *inReq.ParallelToolCalls
		}
	}

	modelConfig := map[string]any{
		"key":               modelID,
		"name":              modelID,
		"display_name":      displayName,
		"format":            "openai",
		"source":            "system",
		"api_key":           "",
		"url":               "",
		"is_vl":             supportsImages,
		"is_reasoning":      supportsReasoning,
		"max_output_tokens": maxOutputTokens,
	}

	// 服务端按 body.business 的 product/type 解析模型目录, 与 Cosy 业务头同源;
	// 缺任一项上游即回 503 Model catalog unavailable (2026-09-22 实测)。
	chatHeaders, err := manifest.RenderHeaderGroup("chat", map[string]string{
		"modelKey":    modelID,
		"modelSource": "system",
	})
	if err != nil {
		return "", nil, fmt.Errorf("render chat headers for business identity: %w", err)
	}
	businessProduct := chatHeaders["Cosy-Business-Product"]
	businessType := chatHeaders["Cosy-Business-Type"]
	if businessProduct == "" || businessType == "" {
		return "", nil, errors.New("manifest chat headers missing Cosy-Business-Product/Type")
	}

	reqID := uuid.NewString()

	envelope := map[string]any{
		"request_id":     reqID,
		"request_set_id": sessionID,
		"chat_record_id": sessionID,
		"session_id":     sessionID,
		"stream":         true,
		"chat_task":      "FREE_INPUT",
		"chat_context": map[string]any{
			"text":       "",
			"features":   []any{},
			"extra":      map[string]any{},
			"chatPrompt": false,
			"imageUrls":  []any{},
		},
		"is_reply":         true,
		"is_retry":         false,
		"source":           1,
		"version":          "3",
		"agent_id":         "agent_common",
		"task_id":          "common",
		"session_type":     "qoder_work",
		"aliyun_user_type": "",
		"model_config":     modelConfig,
		"custom_model":     nil,
		"system":           "",
		"messages":         dualWrittenMsgs,
		"tools":            tools,
		"parameters":       parameters,
		"business": map[string]any{
			"product":          businessProduct,
			"type":             businessType,
			"version":          "1",
			"feature_switches": map[string]any{},
		},
	}

	bodyBytes, err := json.Marshal(envelope)
	if err != nil {
		return "", nil, fmt.Errorf("marshal cosy chat envelope: %w", err)
	}

	return modelID, bodyBytes, nil
}

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

type unwrapResult struct {
	IsDone   bool
	Skip     bool
	ErrorMsg string
	RawChunk string
}

// unwrapSSEFrame 解析单条 SSE data 载荷，支持形态 A (网关包装) 与形态 B (直出)。
func unwrapSSEFrame(payload string) unwrapResult {
	if payload == "[DONE]" {
		return unwrapResult{IsDone: true}
	}
	if strings.HasPrefix(payload, "[NOTIFICATIONS]") {
		return unwrapResult{Skip: true}
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return unwrapResult{ErrorMsg: fmt.Sprintf("chat SSE 帧非 JSON: %s", truncate(payload, 120))}
	}

	// 形态 A：网关包装 { body: string, statusCodeValue: int }
	if bodyStr, ok := parsed["body"].(string); ok {
		status := 200
		if v, valid := asFiniteNumber(parsed["statusCodeValue"]); valid {
			status = int(v)
		} else if v, valid := asFiniteNumber(parsed["status_code"]); valid {
			status = int(v)
		}

		if status != 200 {
			return unwrapResult{ErrorMsg: fmt.Sprintf("upstream gateway error HTTP %d: %s", status, truncate(bodyStr, 200))}
		}

		inner := strings.TrimSpace(bodyStr)
		if inner == "" || strings.HasPrefix(inner, "[NOTIFICATIONS]") {
			return unwrapResult{Skip: true}
		}
		if inner == "[DONE]" {
			return unwrapResult{IsDone: true}
		}

		// 内层也是上游帧: 业务错误 code 必须在这一层认出来, 否则错误 JSON 会被当成 chunk 发给客户端,
		// 非流式聚合路径则会因为帧里没有 choices 而静默丢弃, 最后只报"没有可解析的帧"。
		return validateChunkPayload(inner)
	}

	return validateParsedChunk(payload, parsed)
}

// validateChunkPayload 校验一条裸 chunk 载荷, 供形态 A 的内层复用。
func validateChunkPayload(payload string) unwrapResult {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return unwrapResult{ErrorMsg: fmt.Sprintf("chat SSE 帧非 JSON: %s", truncate(payload, 120))}
	}
	return validateParsedChunk(payload, parsed)
}

func validateParsedChunk(payload string, parsed map[string]any) unwrapResult {
	// 业务错误 code 字段检查
	if codeVal, hasCode := parsed["code"]; hasCode && codeVal != nil && codeVal != float64(0) && codeVal != 0 {
		msg := fmt.Sprintf("%v", parsed["message"])
		return unwrapResult{ErrorMsg: fmt.Sprintf("chat 业务错误 code=%v message=%s", codeVal, msg)}
	}

	// 无 body、choices、usage、code 的尾帧 (如 duration) 忽略
	if parsed["choices"] == nil && parsed["usage"] == nil {
		return unwrapResult{Skip: true}
	}

	// 形态 B：直出 chunk
	return unwrapResult{RawChunk: payload}
}

func handleExecuteStream(ctx context.Context, manifest *ManifestV2, cfg *PluginConfig, req pluginapi.ExecutorRequest, streamID string) (map[string]any, error) {
	cred, err := parseCredential(req.StorageJSON)
	if err != nil {
		return nil, fmt.Errorf("parse storage json for stream execute: %w", err)
	}

	payload := req.Payload
	if len(payload) == 0 {
		payload = req.OriginalRequest
	}

	seedPayload := req.OriginalRequest
	if len(seedPayload) == 0 {
		seedPayload = payload
	}

	sessionID := deriveUUID("qwenworkcn-session\x00" + sessionSeed(req, seedPayload))

	modelID, bodyBytes, err := buildChatRequestBody(cfg, payload, manifest, sessionID)
	if err != nil {
		return nil, fmt.Errorf("build chat request body: %w", err)
	}

	staticHeaders, err := manifest.RenderHeaderGroup("chat", map[string]string{
		"modelKey":    modelID,
		"modelSource": "system",
	})
	if err != nil {
		return nil, fmt.Errorf("render chat headers: %w", err)
	}

	orgID, _ := cred.extra["organization_id"].(string)
	orgTags := stringList(cred.extra["organization_tags"])
	var agreedPtr *bool
	if v, ok := cred.extra["data_policy_agreed"].(bool); ok {
		agreedPtr = &v
	}

	signed, err := signCosyRequest(SignInferInput{
		Endpoint:      manifest.Endpoints.BaseURL,
		MachineID:     cred.MachineID,
		DeviceToken:   cred.AccessToken(),
		Body:          string(bodyBytes),
		CosyVersion:   manifest.Profile.CosyVersion,
		StaticHeaders: staticHeaders,
		User: &SignInferUser{
			UID:              cred.UserID,
			OrganizationID:   orgID,
			OrganizationTags: orgTags,
			DataPolicyAgreed: agreedPtr,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("sign cosy request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, signed.URL, bytes.NewReader([]byte(signed.Body)))
	if err != nil {
		return nil, fmt.Errorf("create stream chat request: %w", err)
	}
	for k, v := range signed.Headers {
		httpReq.Header.Set(k, v)
	}

	go func() {
		defer func() {
			_ = callHostStreamClose(streamID, "")
		}()

		resp, errDo := chatHTTPClient.Do(httpReq)
		if errDo != nil {
			_ = callHostStreamClose(streamID, errDo.Error())
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			errBytes, _ := io.ReadAll(resp.Body)
			_ = callHostStreamClose(streamID, fmt.Sprintf("upstream chat error %d: %s", resp.StatusCode, string(errBytes)))
			return
		}

		reader := bufio.NewReader(resp.Body)
		for {
			line, errRead := reader.ReadBytes('\n')
			if len(line) > 0 {
				trimmed := strings.TrimSpace(string(line))
				if strings.HasPrefix(trimmed, ":") {
					continue
				}

				if payload, ok := ssePayload(trimmed); ok {
					res := unwrapSSEFrame(payload)
					if res.ErrorMsg != "" {
						_ = callHostStreamClose(streamID, res.ErrorMsg)
						return
					}
					if res.IsDone {
						_ = callHostStreamEmit(streamID, []byte("[DONE]"))
						return
					}
					if res.Skip || res.RawChunk == "" {
						continue
					}

					emitErr := callHostStreamEmit(streamID, []byte(res.RawChunk))
					if emitErr != nil {
						return
					}
				}
			}

			if errRead != nil {
				if errRead != io.EOF {
					_ = callHostStreamClose(streamID, errRead.Error())
				}
				return
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

	payload := req.Payload
	if len(payload) == 0 {
		payload = req.OriginalRequest
	}

	seedPayload := req.OriginalRequest
	if len(seedPayload) == 0 {
		seedPayload = payload
	}

	sessionID := deriveUUID("qwenworkcn-session\x00" + sessionSeed(req, seedPayload))

	modelID, bodyBytes, err := buildChatRequestBody(cfg, payload, manifest, sessionID)
	if err != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("build chat request body: %w", err)
	}

	staticHeaders, err := manifest.RenderHeaderGroup("chat", map[string]string{
		"modelKey":    modelID,
		"modelSource": "system",
	})
	if err != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("render chat headers: %w", err)
	}

	orgID, _ := cred.extra["organization_id"].(string)
	orgTags := stringList(cred.extra["organization_tags"])
	var agreedPtr *bool
	if v, ok := cred.extra["data_policy_agreed"].(bool); ok {
		agreedPtr = &v
	}

	signed, err := signCosyRequest(SignInferInput{
		Endpoint:      manifest.Endpoints.BaseURL,
		MachineID:     cred.MachineID,
		DeviceToken:   cred.AccessToken(),
		Body:          string(bodyBytes),
		CosyVersion:   manifest.Profile.CosyVersion,
		StaticHeaders: staticHeaders,
		User: &SignInferUser{
			UID:              cred.UserID,
			OrganizationID:   orgID,
			OrganizationTags: orgTags,
			DataPolicyAgreed: agreedPtr,
		},
	})
	if err != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("sign cosy request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, signed.URL, bytes.NewReader([]byte(signed.Body)))
	if err != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("create non-stream chat request: %w", err)
	}
	for k, v := range signed.Headers {
		httpReq.Header.Set(k, v)
	}

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

func aggregateChatStream(raw []byte) ([]byte, error) {
	out := chatCompletionResponse{Object: "chat.completion"}
	var content, reasoning strings.Builder
	calls := make(map[int]*toolCall)
	var order []int
	finish := ""
	seen := false

	for _, line := range strings.Split(string(raw), "\n") {
		payload, ok := ssePayload(strings.TrimSpace(line))
		if !ok {
			continue
		}
		res := unwrapSSEFrame(payload)
		if res.ErrorMsg != "" {
			return nil, errors.New(res.ErrorMsg)
		}
		if res.IsDone || res.Skip || res.RawChunk == "" {
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
		if err := json.Unmarshal([]byte(res.RawChunk), &chunk); err != nil {
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
