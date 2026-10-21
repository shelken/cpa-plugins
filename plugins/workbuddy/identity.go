package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// 伪装配置渲染引擎: 读取 manifest.values 注册表, 按声明派生动态值, 再统一渲染
// 请求头与 body 指令里的 {{占位符}}。头的具体名字与取值都在配置里, 本文件只实现机制。
//
// 两阶段渲染保证同值一致性: 先按 scope 求值一次(每个 name 只算一次), 再填充全部引用,
// 因此 X-Request-ID 与 X-Conversation-Message-ID 引用同一 {{messageId}} 时天然同值。

// ValueDecl 是 values 注册表的单条声明, 形如:
//
//	{"from": "credential"}                                  → 凭据 userId
//	{"from": "credentialToken"}                             → 凭据 access token
//	{"from": "manifest", "manifestKey": "appVersion"}       → manifest provenance 派生的模板变量
//	{"derive": "uuid"|"hex32", "salt": "...", "scope": "session"|"request"}
type ValueDecl struct {
	From        string `json:"from,omitempty"`
	ManifestKey string `json:"manifestKey,omitempty"`
	Derive      string `json:"derive,omitempty"`
	Salt        string `json:"salt,omitempty"`
	Scope       string `json:"scope,omitempty"`
}

// valueResolver 为一次请求求值全部动态值。
type valueResolver struct {
	decls   map[string]ValueDecl
	seed    string
	cred    *Credential
	vars    map[string]string // manifest 模板变量(appVersion/cliVersion 等)
	session map[string]string // scope=session 的缓存, 按 seed 命中
	mu      sync.Mutex
}

// sessionCache 按 seed 缓存会话级值, 会话内多次请求复用同一批 ID。
var sessionCache = struct {
	sync.Mutex
	bySeed map[string]map[string]string
}{bySeed: map[string]map[string]string{}}

func newSessionValues(seed string) map[string]string {
	sessionCache.Lock()
	defer sessionCache.Unlock()
	if v, ok := sessionCache.bySeed[seed]; ok {
		return v
	}
	v := map[string]string{}
	sessionCache.bySeed[seed] = v
	// 上限保护: 宿主长驻时 seed 数量受凭据与会话数约束, 仍防御性截断最旧条目
	if len(sessionCache.bySeed) > 512 {
		for k := range sessionCache.bySeed {
			delete(sessionCache.bySeed, k)
			break
		}
	}
	return v
}

func (r *valueResolver) value(name string) (string, error) {
	decl, ok := r.decls[name]
	if !ok {
		return "", fmt.Errorf("values 注册表缺少声明: %s", name)
	}
	switch decl.From {
	case "credential":
		return r.cred.UserID, nil
	case "credentialToken":
		return r.cred.AccessToken(), nil
	case "manifest":
		v, ok := r.vars[decl.ManifestKey]
		if !ok || v == "" {
			return "", fmt.Errorf("manifest 模板变量缺失: %s", decl.ManifestKey)
		}
		return v, nil
	}
	if decl.Derive == "" {
		return "", fmt.Errorf("values 声明 %s 缺少 from/derive", name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if decl.Scope == "session" {
		if v, ok := r.session[name]; ok {
			return v, nil
		}
	}
	// session 级: 确定性派生(材料=salt+name+seed), 同会话恒定;
	// request 级: 官方语义是每次请求新值, 材料掺入随机熵, 不能用会话种子(否则同会话两次请求同值)。
	material := decl.Salt + name + "\x00" + r.seed
	if decl.Scope != "session" {
		material += "\x00" + uuid.NewString()
	}
	v := derive(decl.Derive, material)
	if decl.Scope == "session" {
		r.session[name] = v
	}
	return v, nil
}

// derive 复刻原 executor.go 的两种派生形态: uuid(带横线)与 hex32(无横线)。
// salt 里已包含用途前缀与种子, 再拼 name 避免同一 salt 声明被多个 name 复用时同值。
func derive(kind, material string) string {
	switch kind {
	case "uuid":
		sum := sha256.Sum256([]byte(material))
		raw := [16]byte{}
		copy(raw[:], sum[:16])
		raw[6] = (raw[6] & 0x0f) | 0x40
		raw[8] = (raw[8] & 0x3f) | 0x80
		return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
	case "hex32":
		sum := sha256.Sum256([]byte(material))
		return hex.EncodeToString(sum[:16])
	}
	return ""
}

// resolveValues 求值注册表全部声明; 任一失败即报错, 不发残缺请求。
func resolveValues(decls map[string]ValueDecl, seed string, cred *Credential, vars map[string]string) (map[string]string, error) {
	r := &valueResolver{decls: decls, seed: seed, cred: cred, vars: vars, session: newSessionValues(seed)}
	out := make(map[string]string, len(decls))
	for name := range decls {
		v, err := r.value(name)
		if err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, nil
}

// renderValue 渲染字符串里的 {{占位符}}。整串恰为一个占位符时保留值类型(注入 body 用)。
// 未知占位符报错: 配置写错头名静默上线比失败更糟。
func renderValue(tpl string, values map[string]string, raw map[string]any) (any, error) {
	if exact := strings.TrimSpace(tpl); strings.HasPrefix(exact, "{{") && strings.HasSuffix(exact, "}}") && !strings.Contains(exact[2:len(exact)-2], "{{") {
		name := strings.TrimSpace(exact[2 : len(exact)-2])
		if v, ok := raw[name]; ok {
			return v, nil
		}
		if v, ok := values[name]; ok {
			return v, nil
		}
		return nil, fmt.Errorf("未知占位符: %s", name)
	}
	var b strings.Builder
	for {
		start := strings.Index(tpl, "{{")
		if start < 0 {
			b.WriteString(tpl)
			return b.String(), nil
		}
		end := strings.Index(tpl[start:], "}}")
		if end < 0 {
			b.WriteString(tpl)
			return b.String(), nil
		}
		b.WriteString(tpl[:start])
		name := strings.TrimSpace(tpl[start+2 : start+end])
		if v, ok := raw[name]; ok {
			b.WriteString(fmt.Sprint(v))
		} else if v, ok := values[name]; ok {
			b.WriteString(v)
		} else {
			return nil, fmt.Errorf("未知占位符: %s", name)
		}
		tpl = tpl[start+end+2:]
	}
}

// renderHeaders 按配置头组渲染全部请求头。值非字符串的声明(布尔/数字)先转字符串。
func renderHeaders(group map[string]string, values map[string]string) (http.Header, error) {
	h := make(http.Header, len(group))
	raw := make(map[string]any, len(values))
	for k, v := range values {
		raw[k] = v
	}
	for key, tpl := range group {
		v, err := renderValue(tpl, values, raw)
		if err != nil {
			return nil, fmt.Errorf("渲染头 %s: %w", key, err)
		}
		h.Set(key, fmt.Sprint(v))
	}
	return h, nil
}

// injectUserMessageFields 把 body.userMessageFields 声明的字段合并进全部 user 消息。
// 官方桌面端每条 user 消息都带 agent/startsNewUserRequest/conversationRequestId 簿记。
// 已存在的字段不覆盖: 宿主或客户端显式给过的值优先。
func injectUserMessageFields(messages []chatMessage, fields map[string]any, values map[string]string) error {
	if len(fields) == 0 {
		return nil
	}
	raw := make(map[string]any, len(values))
	for k, v := range values {
		raw[k] = v
	}
	for i := range messages {
		if messages[i].Role != "user" {
			continue
		}
		if messages[i].Extra == nil {
			messages[i].Extra = map[string]any{}
		}
		for key, tpl := range fields {
			if _, exists := messages[i].Extra[key]; exists {
				continue
			}
			// 非字符串标量(布尔/数字)直接原样注入, 保住 JSON 类型;
			// 字符串才走占位符渲染。
			if s, isStr := tpl.(string); isStr {
				v, err := renderValue(s, values, raw)
				if err != nil {
					return fmt.Errorf("渲染 user 字段 %s: %w", key, err)
				}
				messages[i].Extra[key] = v
				continue
			}
			messages[i].Extra[key] = tpl
		}
	}
	return nil
}
