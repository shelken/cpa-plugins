package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

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
	Name             string     `json:"name,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	StopReason       string     `json:"stop_reason,omitempty"`
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

func findModelConfig(manifest *ManifestV2, modelID string) *ManifestModel {
	for i := range manifest.Models {
		if strings.EqualFold(manifest.Models[i].ID, modelID) {
			return &manifest.Models[i]
		}
	}
	return nil
}

func prepareChatRequestBody(reqPayload []byte, manifest *ManifestV2, profile *ProfileConfig, isStream bool) ([]byte, error) {
	var inReq chatCompletionRequest
	if err := json.Unmarshal(reqPayload, &inReq); err != nil {
		return nil, fmt.Errorf("unmarshal chat completions request: %w", err)
	}

	modelDef := findModelConfig(manifest, inReq.Model)
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

	outMap := make(map[string]any)
	outMap["model"] = inReq.Model
	outMap["messages"] = inReq.Messages
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

func traceID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}

func buildChatHeaders(profile *ProfileConfig, cred *Credential) http.Header {
	headers := make(http.Header)

	for k, v := range profile.Headers["chat"] {
		headers.Set(k, v)
	}

	// 取值形态照抄桌面客户端: 消息 id 与请求 id 相同, 会话请求 id 与根请求 id 相同,
	// 这三类 id 为 32 位无横线十六进制; 会话 id 与连接 id 为带横线的 UUID。
	// 客户端在对话接口上不发送 X-Session-ID, 故不设置。
	messageID := traceID()
	conversationRequestID := traceID()

	headers.Set("Authorization", "Bearer "+cred.AccessToken())
	headers.Set("X-User-Id", cred.UserID)
	headers.Set("X-Conversation-ID", uuid.NewString())
	headers.Set("X-Conversation-Request-ID", conversationRequestID)
	headers.Set("X-Root-Request-ID", conversationRequestID)
	headers.Set("X-Conversation-Message-ID", messageID)
	headers.Set("X-Request-ID", messageID)
	headers.Set("X-Trace-Id", traceID())
	headers.Set("Acp-Connection-Id", uuid.NewString())
	headers.Set("X-Codebuddy-Request", "1")
	headers.Set("X-Requested-With", "XMLHttpRequest")
	headers.Set("Accept", "application/json")
	if headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", "application/json")
	}

	return headers
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

	bodyBytes, err := prepareChatRequestBody(payload, manifest, profile, true)
	if err != nil {
		return nil, fmt.Errorf("prepare stream chat body: %w", err)
	}

	chatURL := manifest.BuildURL(manifest.Endpoints.ChatCompletions)
	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, chatURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create stream chat request: %w", err)
	}

	headers := buildChatHeaders(profile, cred)
	httpReq.Header = headers

	go func() {
		defer func() {
			_ = callHostStreamClose(streamID, "")
		}()

		resp, errDo := httpClient.Do(httpReq)
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
				lineStr := string(line)
				trimmed := strings.TrimSpace(lineStr)

				if strings.HasPrefix(trimmed, ":") {
					continue
				}

				if trimmed == "data: [DONE]" {
					break
				}

				if strings.HasPrefix(trimmed, "data:") {
					emitErr := callHostStreamEmit(streamID, line)
					if emitErr != nil {
						break
					}
				}
			}

			if errRead != nil {
				if errRead != io.EOF {
					_ = callHostStreamClose(streamID, errRead.Error())
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

	bodyBytes, err := prepareChatRequestBody(payload, manifest, profile, false)
	if err != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("prepare non-stream chat body: %w", err)
	}

	chatURL := manifest.BuildURL(manifest.Endpoints.ChatCompletions)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("create non-stream chat request: %w", err)
	}

	httpReq.Header = buildChatHeaders(profile, cred)

	resp, err := httpClient.Do(httpReq)
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

	return pluginapi.ExecutorResponse{
		Payload: respBytes,
	}, nil
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

var hostCallMu sync.Mutex
