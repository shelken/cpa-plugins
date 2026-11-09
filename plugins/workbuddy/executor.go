package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"

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
	Model               string        `json:"model"`
	Messages            []chatMessage `json:"messages"`
	Stream              bool          `json:"stream,omitempty"`
	StreamOptions       any           `json:"stream_options,omitempty"`
	Temperature         *float64      `json:"temperature,omitempty"`
	MaxTokens           *int          `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int          `json:"max_completion_tokens,omitempty"`
	Tools               []any         `json:"tools,omitempty"`
	ToolChoice          any           `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool         `json:"parallel_tool_calls,omitempty"`
	ReasoningEffort     string        `json:"reasoning_effort,omitempty"`
	ReasoningSummary    string        `json:"reasoning_summary,omitempty"`
	Verbosity           string        `json:"verbosity,omitempty"`
	Store               *bool         `json:"store,omitempty"`
	ExtraVars           any           `json:"extra_vars,omitempty"`
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

// 上游丢弃 assistant 消息里的 reasoning_content 字段 (沙箱 codeword 实验: content 里的
// 暗号能复述, reasoning_content 里的看不到); 官方客户端回传历史思考用 reasoning 字段
// (magpie #291 同款实测: reasoning_content 里的词到不了模型, reasoning 能到)。
// cleanMessages 末尾把历史 reasoning_content 换名为 reasoning。
func historyReasoningField(messages []chatMessage) {
	for i := range messages {
		if messages[i].Role != "assistant" || messages[i].ReasoningContent == "" {
			continue
		}
		if messages[i].Extra == nil {
			messages[i].Extra = map[string]any{}
		}
		// 已有 reasoning 字段 (宿主或客户端显式给的) 优先
		if _, exists := messages[i].Extra["reasoning"]; !exists {
			messages[i].Extra["reasoning"] = messages[i].ReasoningContent
		}
		messages[i].ReasoningContent = ""
	}
}

// 上游 chat/completions 只认 system/user/assistant/tool: 不认 developer 角色,
// 且要求首条消息为 system (否则 11128, 含 "first message is not system prompt" 形态)。
// 宿主 v8.0.61 起 (00b76771) responses→chat 不再把 developer 降级为 user, 这里补齐映射:
// developer 原位改为 system (magpie 已验证做法), 无 system/developer 开头时注入占位 system。
func normalizeSystemFirst(messages []chatMessage) []chatMessage {
	hasLeadSystem := false
	for i := range messages {
		if messages[i].Role == "developer" {
			messages[i].Role = "system"
		}
		if i == 0 && messages[i].Role == "system" {
			hasLeadSystem = true
		}
	}
	if !hasLeadSystem && len(messages) > 0 && messages[0].Role != "system" {
		messages = append([]chatMessage{{Role: "system", Content: "You are a helpful assistant."}}, messages...)
	}
	return messages

}

func cleanMessages(rawMessages []chatMessage, supportsImages bool, manifest *ManifestV2) []chatMessage {
	rawMessages = normalizeSystemFirst(rawMessages)
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

	historyReasoningField(finalMessages)

	return finalMessages
}

// 上游 (kimi/deepseek 等) 的工具校验会拒绝常见 schema 形态, 400 11133/11152
// (magpie workbuddy #122/#378 实测记录): 无类型 enum、全 object 的无类型 oneOf、
// bool 当 schema、required 非列表、空枚举、未知类型、非法工具名。shapeTools
// 在透传前给每个形态换成上游接受的等价形态, 不收窄不放宽调用面。
// 改名是其中唯一的不可逆变换: 上游不收非法字符, 客户端却只认原名, 所以返回
// safe→原名映射, 请求侧改写历史与 tool_choice, 响应侧由 renameBackChunk 换回。
func shapeTools(tools []any) map[string]string {
	renames := map[string]string{}
	if len(tools) == 0 {
		return renames
	}
	// 预登记全部合法名 (magpie fitTools): 合法名永不改名, 去重空间含它们
	taken := make(map[string]bool, len(tools))
	for _, raw := range tools {
		if fn := toolFunction(raw); fn != nil {
			if name, _ := fn["name"].(string); name != "" && toolNameValid(name) {
				taken[name] = true
			}
		}
	}
	for _, raw := range tools {
		fn := toolFunction(raw)
		if fn == nil {
			continue
		}
		name, _ := fn["name"].(string)
		if name != "" && !toolNameValid(name) {
			safe := safeToolName(name, taken)
			renames[safe] = name
			fn["name"] = safe
		}
		params, has := fn["parameters"]
		if !has {
			continue
		}
		if pm, ok := params.(map[string]any); ok {
			shapeSchema(pm, pm, 0)
		} else {
			// null/数组等非对象形态校验器必拒, 换成等价的空 object
			fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
	}
	return renames
}

// toolFunction 取工具条目的 function 对象; 形态不对返回 nil。
func toolFunction(raw any) map[string]any {
	tool, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	fn, ok := tool["function"].(map[string]any)
	if !ok {
		return nil
	}
	return fn
}

// toolNameValid: 上游只收字母/数字/下划线/连字符 (magpie NAME_OK 实测)。
func toolNameValid(name string) bool {
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

const toolSchemaMaxDepth = 64

var toolSchemaKnownTypes = map[string]bool{
	"string": true, "number": true, "integer": true, "boolean": true,
	"object": true, "array": true, "null": true,
}

// 这些关键字上的 bool 是合法语义, 不是 bool 当 schema, 不替换。
var toolSchemaBoolOK = map[string]bool{"additionalProperties": true, "unevaluatedProperties": true}

// schema 位置关键字 (magpie fitShape 的 SUB_ONE/SUB_LIST/SUB_MAP): 只在这些
// 位置上做 bool 替换与递归, 不然 uniqueItems 这类布尔关键字会被误改。
var toolSchemaSubOne = [...]string{"items", "additionalItems", "unevaluatedItems", "contains", "not", "if", "then", "else",
	"propertyNames", "additionalProperties", "unevaluatedProperties"}
var toolSchemaSubList = [...]string{"anyOf", "oneOf", "allOf", "prefixItems"}
var toolSchemaSubMap = [...]string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"}

// safeToolName 把工具名里上游不收的字符 (点/冒号/空格/CJK 等, magpie 实测 11152)
// 换成 _, 与已有名字去重; 不裁首尾下划线 (合法字符)。
func safeToolName(name string, taken map[string]bool) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if toolNameValid(string(r)) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	base := b.String()
	if base == "" {
		base = "tool"
	}
	if !taken[base] {
		taken[base] = true
		return base
	}
	for i := 2; ; i++ {
		n := fmt.Sprintf("%s_%d", base, i)
		if !taken[n] {
			taken[n] = true
			return n
		}
	}
}

// shapeSchema 就地修整 schema 节点及其全部子 schema 位置 (magpie fitShape +
// fitSchema + fitEnum + typeUnions)。root 供本地 $ref 解析。子 schema 只在
// 关键字位置上递归, description/examples 等非 schema 值不误入。
func shapeSchema(s any, root map[string]any, depth int) {
	if depth > toolSchemaMaxDepth {
		return
	}
	m, ok := s.(map[string]any)
	if !ok {
		if list, isList := s.([]any); isList {
			for _, item := range list {
				shapeSchema(item, root, depth+1)
			}
		}
		return
	}
	replaceBoolSchemas(m)
	shapeSchemaNode(m, root)
	for _, key := range toolSchemaSubOne {
		if sub, has := m[key]; has {
			shapeSchema(sub, root, depth+1)
		}
	}
	for _, key := range toolSchemaSubList {
		if list, has := m[key].([]any); has {
			for _, item := range list {
				shapeSchema(item, root, depth+1)
			}
		}
	}
	for _, key := range toolSchemaSubMap {
		if sm, has := m[key].(map[string]any); has {
			for _, v := range sm {
				shapeSchema(v, root, depth+1)
			}
		}
	}
}

// replaceBoolSchemas 把 schema 位置上的 bool 换成等价对象 schema (magpie
// subSchema; kimi 实测 11133): true → {} (什么都行), false → {"not": {}} (什么都不行)。
func replaceBoolSchemas(m map[string]any) {
	for _, key := range toolSchemaSubOne {
		if sub, has := m[key]; has {
			if b, isBool := sub.(bool); isBool && !toolSchemaBoolOK[key] {
				if b {
					m[key] = map[string]any{}
				} else {
					m[key] = map[string]any{"not": map[string]any{}}
				}
			}
		}
	}
	for _, key := range toolSchemaSubList {
		if list, has := m[key].([]any); has {
			for i, item := range list {
				if b, isBool := item.(bool); isBool {
					if b {
						list[i] = map[string]any{}
					} else {
						list[i] = map[string]any{"not": map[string]any{}}
					}
				}
			}
		}
	}
	for _, key := range toolSchemaSubMap {
		sm, ok := m[key].(map[string]any)
		if !ok {
			continue
		}
		for name, v := range sm {
			if b, isBool := v.(bool); isBool {
				if b {
					sm[name] = map[string]any{}
				} else {
					sm[name] = map[string]any{"not": map[string]any{}}
				}
			}
		}
	}
}

// shapeSchemaNode 修整单个 schema 节点本体 (不改子 schema)。
func shapeSchemaNode(m map[string]any, root map[string]any) {
	// type: 小写化保留已知类型 (kimi 不认 "String"), 未知类型丢弃; 列表去重,
	// 单个收敛为标量 (magpie fitShape)
	if t, has := m["type"]; has {
		switch tv := t.(type) {
		case string:
			lower := strings.ToLower(tv)
			if toolSchemaKnownTypes[lower] {
				m["type"] = lower
			} else {
				delete(m, "type")
			}
		case []any:
			var uniq []any
			seen := map[string]bool{}
			for _, one := range tv {
				ts, isStr := one.(string)
				if !isStr {
					continue
				}
				lower := strings.ToLower(ts)
				if !toolSchemaKnownTypes[lower] || seen[lower] {
					continue
				}
				seen[lower] = true
				uniq = append(uniq, lower)
			}
			switch len(uniq) {
			case 0:
				delete(m, "type")
			case 1:
				m["type"] = uniq[0]
			default:
				m["type"] = uniq
			}
		}
	}

	// required: 非列表丢弃; 列表内非字符串过滤 (校验器拒绝)
	if req, has := m["required"]; has {
		list, isList := req.([]any)
		if !isList {
			delete(m, "required")
		} else {
			filtered := make([]any, 0, len(list))
			for _, one := range list {
				if str, isStr := one.(string); isStr {
					filtered = append(filtered, str)
				}
			}
			m["required"] = filtered
		}
	}

	shapeEnum(m)

	// 元组 items → anyOf 或单 schema, additionalItems 删除 (magpie fitSchema)
	if items, ok := m["items"].([]any); ok {
		list := make([]any, 0, len(items))
		for _, v := range items {
			if _, isMap := v.(map[string]any); isMap {
				list = append(list, v)
			}
		}
		switch len(list) {
		case 0:
			m["items"] = map[string]any{}
		case 1:
			m["items"] = list[0]
		default:
			m["items"] = map[string]any{"anyOf": list}
		}
		delete(m, "additionalItems")
	}

	// oneOf 且无类型: 全部分支 (含本地 $ref 链解析) 都是 object → 补 type object
	// (magpie #378: kimi 对无类型 oneOf 拒 11133)
	if _, hasType := m["type"]; !hasType {
		if branches, ok := m["oneOf"].([]any); ok && len(branches) > 0 {
			allObj := true
			for _, branch := range branches {
				if !schemaBranchIsObject(root, branch, 0) {
					allObj = false
					break
				}
			}
			if allObj {
				m["type"] = "object"
			}
		}
	}

	shapePatternNul(m)
}

// jsonValueType 给 enum 值定 JSON Schema 类型; 整数值归 integer (magpie valueType)。
func jsonValueType(v any) string {
	switch val := v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		if val == math.Trunc(val) && !math.IsInf(val, 0) {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return ""
	}
}

// shapeEnum 给 enum 补 type (magpie fitEnum; kimi 实测 11133): 单类型 → type
// 标量, enum 保留; 含 null → [T, "null"] (kimi 只拒两个非 null 类型); 多类型
// → anyOf 分支各带自己值域的 enum, 原 enum/type 删除; 空或非列表 → 删字段
// (任何值都套不进去, 删掉即等价)。
func shapeEnum(m map[string]any) {
	e, has := m["enum"]
	if !has {
		return
	}
	values, isList := e.([]any)
	if !isList || len(values) == 0 {
		delete(m, "enum")
		return
	}
	// 已声明单一非 null type 的不动
	if ts, hasType := m["type"].(string); hasType {
		if ts != "null" {
			return
		}
	} else if tl, isList := m["type"].([]any); isList {
		nonNull := 0
		for _, one := range tl {
			if s, ok := one.(string); ok && s != "null" {
				nonNull++
			}
		}
		if nonNull == 1 {
			return
		}
	}
	// 按首见顺序分桶; integer 有 number 桶时并入 (magpie fitEnum)
	var order []string
	buckets := map[string][]any{}
	hasNull := false
	for _, v := range values {
		k := jsonValueType(v)
		if k == "" {
			return // 未知值类型: 保持原样, 不碰
		}
		if k == "null" {
			hasNull = true
			continue
		}
		if k == "integer" {
			if _, ok := buckets["number"]; ok {
				buckets["number"] = append(buckets["number"], v)
				continue
			}
		}
		if _, seen := buckets[k]; !seen {
			order = append(order, k)
		}
		buckets[k] = append(buckets[k], v)
	}
	if _, ok := buckets["integer"]; ok {
		if _, ok := buckets["number"]; ok {
			buckets["number"] = append(buckets["number"], buckets["integer"]...)
			delete(buckets, "integer")
			for i, k := range order {
				if k == "integer" {
					order = append(order[:i], order[i+1:]...)
					break
				}
			}
		}
	}
	if len(order) <= 1 {
		var want any
		if len(order) == 0 {
			want = "null"
		} else if hasNull {
			want = []any{order[0], "null"}
		} else {
			want = order[0]
		}
		m["type"] = want
		return
	}
	split := make([]any, 0, len(order)+1)
	for _, k := range order {
		split = append(split, map[string]any{"type": k, "enum": buckets[k]})
	}
	if hasNull {
		split = append(split, map[string]any{"type": "null"})
	}
	delete(m, "enum")
	delete(m, "type")
	if _, hasAnyOf := m["anyOf"]; hasAnyOf {
		allOf, _ := m["allOf"].([]any)
		m["allOf"] = append(allOf, map[string]any{"anyOf": split})
	} else {
		m["anyOf"] = split
	}
}

// schemaBranchIsObject: 分支是 object — 直接声明, 或经本地 $ref 链指到的定义
// (magpie objectBranch); 深度限 16 兼作环保护。
func schemaBranchIsObject(root map[string]any, branch any, depth int) bool {
	bm, ok := branch.(map[string]any)
	if !ok {
		return false
	}
	if t, has := bm["type"]; has {
		ts, isStr := t.(string)
		return isStr && ts == "object"
	}
	ref, has := bm["$ref"].(string)
	if !has || !strings.HasPrefix(ref, "#/") || depth > 16 {
		return false
	}
	target := resolveLocalRef(root, ref)
	if target == nil {
		return false
	}
	return schemaBranchIsObject(root, target, depth+1)
}

// resolveLocalRef 按 JSON Pointer 解析本地 $ref ("#/$defs/x", "#/definitions/x")。
func resolveLocalRef(root map[string]any, ref string) map[string]any {
	at := any(root)
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part = strings.ReplaceAll(part, "~1", "/")
		part = strings.ReplaceAll(part, "~0", "~")
		m, ok := at.(map[string]any)
		if !ok {
			return nil
		}
		next, ok := m[part]
		if !ok {
			return nil
		}
		at = next
	}
	if m, ok := at.(map[string]any); ok {
		return m
	}
	return nil
}

// shapePatternNul 把 pattern 里的 \0 改写 \u0000 (deepseek 校验器不认, 匹配不变;
// magpie NUL_ESCAPE): \\ 是字面反斜杠不动, \0 后跟数字是八进制转义不动。
func shapePatternNul(m map[string]any) {
	p, ok := m["pattern"].(string)
	if !ok || !strings.Contains(p, "\\0") {
		return
	}
	var b strings.Builder
	b.Grow(len(p))
	for i := 0; i < len(p); {
		if p[i] == '\\' && i+1 < len(p) {
			if p[i+1] == '\\' {
				b.WriteString("\\\\")
				i += 2
				continue
			}
			if p[i+1] == '0' && !(i+2 < len(p) && p[i+2] >= '0' && p[i+2] <= '9') {
				b.WriteString("\\u0000")
				i += 2
				continue
			}
		}
		b.WriteByte(p[i])
		i++
	}
	m["pattern"] = b.String()
}

// applyHistoryRenames 按 orig→safe 映射改写历史 tool_calls 与 tool 消息 name
// (magpie fitTools): 不改的话第二轮对话带原名再吃 11152。
func applyHistoryRenames(messages []chatMessage, orig2safe map[string]string) {
	if len(orig2safe) == 0 {
		return
	}
	for i := range messages {
		for j := range messages[i].ToolCalls {
			if safe, ok := orig2safe[messages[i].ToolCalls[j].Function.Name]; ok {
				messages[i].ToolCalls[j].Function.Name = safe
			}
		}
		if messages[i].Role == "tool" && messages[i].Name != "" {
			if safe, ok := orig2safe[messages[i].Name]; ok {
				messages[i].Name = safe
			}
		}
	}
}

// normalizeToolChoice 把客户端的 tool_choice 整形为上游认真执行且接受的形态
// (magpie toolChoiceOn 实测): 上游只认真字符串形态 (对象形态虽不报错但约束
// 不生效); none 上游不执行 (实测照样调工具) → 删约束连同 tools 一起不发;
// auto/required 转字符串; 点名函数转裸函数名字符串, 名字用整形后的 safe 名。
// 返回整形后的 choice 与是否应丢弃全部工具。
func normalizeToolChoice(choice any, orig2safe map[string]string) (any, bool) {
	switch cv := choice.(type) {
	case string:
		if strings.EqualFold(strings.TrimSpace(cv), "none") {
			return nil, true
		}
		return cv, false
	case map[string]any:
		t, _ := cv["type"].(string)
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "none":
			return nil, true
		case "auto", "required":
			return strings.ToLower(strings.TrimSpace(t)), false
		case "function":
			fn, ok := cv["function"].(map[string]any)
			if !ok {
				return nil, false
			}
			name, _ := fn["name"].(string)
			name = strings.TrimSpace(name)
			if name == "" {
				return "auto", false
			}
			if safe, ok := orig2safe[name]; ok {
				name = safe
			}
			return name, false
		default:
			return nil, false
		}
	default:
		return nil, false
	}
}

// renameBackChunk 在响应帧 (单条 chat.completion 或流式 chunk 的 JSON) 里把
// safe 工具名换回客户端原名 (magpie namedBack); 未命中原样返回入参切片, 零拷贝。
func renameBackChunk(frame []byte, safe2orig map[string]string) []byte {
	if len(safe2orig) == 0 {
		return frame
	}
	hit := false
	for safe := range safe2orig {
		if bytes.Contains(frame, []byte("\""+safe+"\"")) {
			hit = true
			break
		}
	}
	if !hit {
		return frame
	}
	var out bytes.Buffer
	dec := json.NewDecoder(bytes.NewReader(frame))
	dec.UseNumber()
	var raw any
	if dec.Decode(&raw) != nil {
		return frame
	}
	root, ok := raw.(map[string]any)
	if !ok {
		return frame
	}
	choices, _ := root["choices"].([]any)
	for _, c := range choices {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range [...]string{"message", "delta"} {
			section, ok := cm[key].(map[string]any)
			if !ok {
				continue
			}
			tcs, ok := section["tool_calls"].([]any)
			if !ok {
				continue
			}
			for _, tc := range tcs {
				tcm, ok := tc.(map[string]any)
				if !ok {
					continue
				}
				fn, ok := tcm["function"].(map[string]any)
				if !ok {
					continue
				}
				if name, _ := fn["name"].(string); name != "" {
					if orig, ok := safe2orig[name]; ok {
						fn["name"] = orig
					}
				}
			}
		}
	}
	if json.NewEncoder(&out).Encode(raw) != nil {
		return frame
	}
	outBytes := out.Bytes()
	// Encoder 追加换行, 帧语义不需要
	return bytes.TrimSuffix(outBytes, []byte("\n"))
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

// prepareChatRequestBody 把 chat completions 请求整形为上游可接受的请求体,
// 返回上游 model id 与工具改名映射 (safe→原名)。
// prepareChatRequestBody 把 chat completions 请求整形为上游可接受的请求体,
// 返回工具改名映射 (safe→原名) 供请求侧/响应侧改名链路使用。
func prepareChatRequestBody(cfg *PluginConfig, reqPayload []byte, manifest *ManifestV2, profile *ProfileConfig, isStream bool, values ...map[string]string) ([]byte, map[string]string, error) {
	var inReq chatCompletionRequest
	if err := json.Unmarshal(reqPayload, &inReq); err != nil {
		return nil, nil, fmt.Errorf("unmarshal chat completions request: %w", err)
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
		var resolvedValues map[string]string
		if len(values) > 0 {
			resolvedValues = values[0]
		}
		if resolvedValues == nil {
			var err error
			resolvedValues, err = resolveValues(manifest.Values, "", &Credential{}, manifestTemplateVars(manifest))
			if err != nil {
				return nil, nil, fmt.Errorf("resolve user field values: %w", err)
			}
		}
		if err := injectUserMessageFields(inReq.Messages, profile.Body.UserMessageFields, resolvedValues); err != nil {
			return nil, nil, err
		}
	}

	// 工具整形可能改名非法工具名 (上游 11152), 改名映射同时用于: 请求侧改写历史
	// tool_calls/tool 消息名/tool_choice, 响应侧把 safe 名换回客户端原名。
	renames := map[string]string{}
	dropTools := false
	if len(inReq.Tools) > 0 {
		renames = shapeTools(inReq.Tools)
		applyHistoryRenames(inReq.Messages, renames)
		if inReq.ToolChoice != nil {
			var normalized any
			normalized, dropTools = normalizeToolChoice(inReq.ToolChoice, renames)
			inReq.ToolChoice = normalized
		}
		if dropTools {
			// 上游不执行 none 约束 (实测照样调工具), 唯一可靠的禁止方式是不发工具
			inReq.Tools = nil
			inReq.ToolChoice = nil
			inReq.ParallelToolCalls = nil
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
	} else if inReq.MaxCompletionTokens != nil {
		outMap["max_tokens"] = *inReq.MaxCompletionTokens
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
		// 小预算请求 (标题生成/分类这类短 ask) 不注入默认 effort: 默认档会把整个预算
		// 烧在思考上 (magpie #643/#644 实测; SMALL_BUDGET 取 Anthropic thinking 的
		// 最低预算, 超过它才可能是有意的思考请求)。客户端显式给的 effort 不受影响。
		const smallBudget = 1024
		limit := 0
		if inReq.MaxTokens != nil {
			limit = *inReq.MaxTokens
		} else if inReq.MaxCompletionTokens != nil {
			limit = *inReq.MaxCompletionTokens
		}
		if effort == defaultEffort && inReq.ReasoningEffort == "" && limit > 0 && limit <= smallBudget {
			effort = ""
		}
		if effort != "" {
			outMap["reasoning_effort"] = effort
			outMap["reasoning_summary"] = "auto"
		}
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

	body, err := json.Marshal(outMap)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal chat request body: %w", err)
	}
	return body, renames, nil
}

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

func handleExecuteStream(manifest *ManifestV2, cfg *PluginConfig, req pluginapi.ExecutorRequest, streamID, hostCallbackID string) (map[string]any, error) {
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

	seedPayload := req.OriginalRequest
	if len(seedPayload) == 0 {
		seedPayload = payload
	}
	seed := sessionSeed(req, seedPayload)
	vars := manifestTemplateVars(manifest)
	values, err := resolveValues(manifest.Values, seed, cred, vars)
	if err != nil {
		return nil, fmt.Errorf("resolve values: %w", err)
	}

	bodyBytes, renames, err := prepareChatRequestBody(cfg, payload, manifest, profile, true, values)
	if err != nil {
		return nil, fmt.Errorf("prepare stream chat body: %w", err)
	}

	chatURL := manifest.BuildURL(manifest.Endpoints.ChatCompletions)

	headers, err := renderHeaders(profile.Headers["chat"], values)
	if err != nil {
		return nil, fmt.Errorf("build stream chat headers: %w", err)
	}

	go func() {
		// 下游 close 必须是本 goroutine 的最后一个宿主回调: 上游 close 是后注册的 defer,
		// 先于它执行; 错误原因经 closeErr 带出, 一条流只关一次。
		var closeErr string
		defer func() { _ = callHostStreamClose(streamID, closeErr) }()

		// 上游连接挂到宿主的 operation 上: 客户端 499 或插件卸载时, 宿主级联取消,
		// stream_read 立即报错, 这里不会无限期阻塞在响应体读取上。
		upstreamStatus, upstreamStreamID, errDo := hostHTTPDoStream(
			hostCallbackID, http.MethodPost, chatURL, map[string][]string(headers), bodyBytes)
		if errDo != nil {
			closeErr = errDo.Error()
			return
		}
		defer hostHTTPStreamClose(upstreamStreamID)

		if upstreamStatus != http.StatusOK {
			closeErr = upstreamErrorMessage(upstreamStatus, upstreamStreamID)
			return
		}
		closeErr = pumpChatSSE(upstreamStreamID, streamID, renames)
	}()

	return map[string]any{
		"headers": map[string][]string{
			"content-type": {"text/event-stream; charset=utf-8"},
		},
	}, nil
}

func handleExecute(manifest *ManifestV2, cfg *PluginConfig, req pluginapi.ExecutorRequest, hostCallbackID string) (pluginapi.ExecutorResponse, error) {
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
	seedPayload := req.OriginalRequest
	if len(seedPayload) == 0 {
		seedPayload = payload
	}
	seed := sessionSeed(req, seedPayload)
	vars := manifestTemplateVars(manifest)
	values, err := resolveValues(manifest.Values, seed, cred, vars)
	if err != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("resolve values: %w", err)
	}

	// 上游只接受流式对话, 发 stream:false 会被拒 (400/11101) 并把凭据打成不可用,
	// 因此这里始终按流式请求上游, 在插件内聚合成单条完整响应再交给宿主。
	bodyBytes, renames, err := prepareChatRequestBody(cfg, payload, manifest, profile, true, values)
	if err != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("prepare non-stream chat body: %w", err)
	}

	chatURL := manifest.BuildURL(manifest.Endpoints.ChatCompletions)

	headers, err := renderHeaders(profile.Headers["chat"], values)
	if err != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("build chat headers: %w", err)
	}

	upstreamStatus, upstreamStreamID, errDo := hostHTTPDoStream(
		hostCallbackID, http.MethodPost, chatURL, map[string][]string(headers), bodyBytes)
	if errDo != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("execute non-stream chat request: %w", errDo)
	}
	defer hostHTTPStreamClose(upstreamStreamID)

	if upstreamStatus != http.StatusOK {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("%s", upstreamErrorMessage(upstreamStatus, upstreamStreamID))
	}

	sseLines, err := hostHTTPStreamReadAll(upstreamStreamID)
	if err != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("read non-stream chat response: %w", err)
	}

	aggregated, err := aggregateChatStream(sseLines)
	if err != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("aggregate upstream stream: %w", err)
	}
	if len(renames) > 0 {
		aggregated = renameBackChunk(aggregated, renames)
	}

	return pluginapi.ExecutorResponse{
		Payload: aggregated,
	}, nil
}

// pumpChatSSE 把上游 SSE 逐行转发给下游, 返回下游关闭原因 (空串为正常结束)。
// renames 非空时, 流式帧里的 safe 工具名换回客户端原名 (renameBackChunk 未命中
// 时零拷贝原样返回)。
func pumpChatSSE(upstreamStreamID, streamID string, renames map[string]string) string {
	// handle 返回 stop=true 时立即停读上游: 遇到 [DONE], 或 emit 失败说明下游已不可达。
	handle := func(line []byte) (stop bool, closeErr string) {
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "data: [DONE]" {
			return true, ""
		}
		payload, ok := ssePayload(trimmed)
		if !ok {
			return false, ""
		}
		if len(renames) > 0 {
			payload = string(renameBackChunk([]byte(payload), renames))
		}
		if err := callHostStreamEmit(streamID, []byte(payload)); err != nil {
			return true, err.Error()
		}
		return false, ""
	}

	var lines sseLineReader
	for {
		chunk, done, errRead := hostHTTPStreamRead(upstreamStreamID)
		lines.write(chunk)
		for line, ok := lines.next(); ok; line, ok = lines.next() {
			if stop, closeErr := handle(line); stop {
				return closeErr
			}
		}
		if errRead != nil {
			return errRead.Error()
		}
		if done {
			// 上游末行可以不带换行, EOF 时按完整行处理。
			_, closeErr := handle(lines.rest())
			return closeErr
		}
	}
}

// sseLineReader 把宿主按固定大小 (32KB) 读出的分块重组为完整行。
// scanned 记录已确认无换行的前缀, 每次只扫新到的字节, 长行不会退化成平方扫描。
type sseLineReader struct {
	buf     []byte
	scanned int
}

func (r *sseLineReader) write(p []byte) { r.buf = append(r.buf, p...) }

func (r *sseLineReader) next() ([]byte, bool) {
	i := bytes.IndexByte(r.buf[r.scanned:], '\n')
	if i < 0 {
		r.scanned = len(r.buf)
		return nil, false
	}
	end := r.scanned + i + 1
	line := r.buf[:end]
	r.buf, r.scanned = r.buf[end:], 0
	return line, true
}

func (r *sseLineReader) rest() []byte {
	tail := r.buf
	r.buf, r.scanned = nil, 0
	return tail
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
