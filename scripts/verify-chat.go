package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 对话链路的验收入口。职责边界: 只驱动真实宿主的 /v1/chat/completions, 断言用户可见行为。
// 装载与注册归 dev-sandbox.go, 仓库不变量归 check-plugins.go, 发布产物归 verify-registry-install.go。
// 场景以注册表声明 (scenarioRegistry), 加场景 = 写一个 run 函数 + 注册一行, main 不随场景增长。
//
// 密钥纪律: 管理密钥只经 sec-run printenv CPA_TOKEN 读取, 客户端密钥由管理面就地取得,
// 二者都不得出现在终端输出里, 出错信息也只回显打码后的形态。

const doneFrame = "[DONE]"

// scenario 是验收集的注册单位: 一段可独立执行的请求序列, 以及它产出的判据。
// requests 是成本披露 (真实请求次数), 供调用方决定要不要跑。
type scenario struct {
	id       string
	requests int
	desc     string
	run      func(*session)
}

// scenarioRegistry 是场景的唯一权威来源: -list、成本合计、范围判定、调度全部由它派生。
var scenarioRegistry = []scenario{
	{"session", 3, "多轮会话: 流式帧合规 / 正文输出 / 上下文记忆 / 多轮前缀缓存", runSession},
	{"probe", 2, "重复请求缓存: 同一请求连发两次, 第二次应命中前缀缓存", runProbe},
	{"nonstream", 1, "非流式链路: 插件聚合上游流式后返回完整 chat.completion", runNonStream},
	{"tools", 3, "工具调用: 定义透传 / 非流式聚合 / 工具结果消费", runTools},
	{"effort", 2, "思考深度与思维链: 最低档与最高档对比", runEffort},
	{"guard", 1, "模型守卫: 未知模型 id 在路由阶段被拒 (不进入凭据与上游)", runGuard},
}

// defaultScenarios 是「没有指定场景」时的执行集, 刻意不含 tools/effort:
// 那两项判据成本高且只对请求构造改动敏感, 应在改动相关时显式点名。
var defaultScenarios = []string{"session", "nonstream"}

// session 持有一次运行的全部状态: 目标实例、共享夹具、已产出的判据。
// 判据不落全局变量, 同一进程内跑多个场景不会有残留。
type session struct {
	*runner
	prefix   string
	codeword string
	results  []verdict
}

func (s *session) record(name, state, detail string) {
	s.results = append(s.results, verdict{name, state, detail})
	fmt.Printf("  %-28s %-6s %s\n", name, state, detail)
}

func (s *session) summary() (passed, warned, failed int) {
	for _, v := range s.results {
		switch v.state {
		case "FAIL":
			failed++
		case "WARN":
			warned++
		}
	}
	return len(s.results) - failed - warned, warned, failed
}

// listFlag 支持 `-x a,b` 与 `-x a -x b` 两种写法, 多项参数是验收子集选择的载体。
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		if s := strings.TrimSpace(part); s != "" {
			*l = append(*l, s)
		}
	}
	return nil
}

func printScenarioCatalog() {
	fmt.Println("可用场景 (按需点名, 不必全跑):")
	for _, s := range scenarioRegistry {
		fmt.Printf("  %-10s %d 次请求   %s\n", s.id, s.requests, s.desc)
	}
	fmt.Printf("  %-10s %d 次请求   以上全部\n", "all", requestsOf(allScenarioIDs()))
	fmt.Printf("\n默认 (不写 -scenarios): %s  共 %d 次请求\n",
		strings.Join(defaultScenarios, ","), requestsOf(defaultScenarios))
	fmt.Println("每个场景覆盖哪些判据见 .agents/skills/verify-cpa-plugin/features/chat.md")
}

func allScenarioIDs() []string {
	ids := make([]string, 0, len(scenarioRegistry))
	for _, s := range scenarioRegistry {
		ids = append(ids, s.id)
	}
	return ids
}

func requestsOf(ids []string) int {
	total := 0
	for _, id := range ids {
		for _, s := range scenarioRegistry {
			if s.id == id {
				total += s.requests
			}
		}
	}
	return total
}

func lookupScenario(id string) (scenario, bool) {
	for _, s := range scenarioRegistry {
		if s.id == id {
			return s, true
		}
	}
	return scenario{}, false
}

// resolveScenarios 把用户输入折成有序执行集, 并回传无法识别的名字。
// 顺序即注册顺序, 与用户书写顺序无关: 多轮会话先跑, 后续场景才能复用前缀缓存。
func resolveScenarios(input []string) ([]scenario, []string) {
	if len(input) == 0 {
		input = defaultScenarios
	}
	want := map[string]bool{}
	var unknown []string
	for _, raw := range input {
		id := strings.ToLower(strings.TrimSpace(raw))
		if id == "" {
			continue
		}
		if id == "all" {
			for _, s := range scenarioRegistry {
				want[s.id] = true
			}
			continue
		}
		if _, ok := lookupScenario(id); !ok {
			unknown = append(unknown, id)
			continue
		}
		want[id] = true
	}

	ordered := make([]scenario, 0, len(want))
	for _, s := range scenarioRegistry {
		if want[s.id] {
			ordered = append(ordered, s)
		}
	}
	return ordered, unknown
}

// manifestPathFor 把 `<plugin>/<model>` 的模型 id 映射到插件自带清单。
// 清单存裸 id, 故此处的 plugin 段不能写死, 否则换插件后清单查不到, 能力判据会静默退化。
func manifestPathFor(modelID, override string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	idx := strings.LastIndex(modelID, "/")
	if idx <= 0 {
		return ""
	}
	return filepath.Join("plugins", modelID[:idx], "data", "static-config.json")
}

func loadModelDef(manifestPath, modelID string) *manifestModel {
	if manifestPath == "" {
		return nil
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil
	}
	var doc manifestFile
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	bareID := modelID
	if idx := strings.LastIndex(bareID, "/"); idx >= 0 {
		bareID = bareID[idx+1:]
	}
	for i := range doc.Models {
		if strings.EqualFold(doc.Models[i].ID, bareID) {
			return &doc.Models[i]
		}
	}
	return nil
}

// 工具结果的取值刻意取专用数字: 模型若真读了工具返回, 复述里必然出现该数字。
const toolResult = "北京 晴 26 摄氏度 湿度 38%"

type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// toolCall 请求与响应两侧同形: 客户端把模型产出的调用原样带回下一轮, 一个类型管两个方向。
type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type toolSpec struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type usageInfo struct {
	PromptTokens          int `json:"prompt_tokens"`
	CompletionTokens      int `json:"completion_tokens"`
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
	CompletionDetails     struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	PromptDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func (u usageInfo) cachedTokens() int {
	if u.PromptCacheHitTokens > 0 {
		return u.PromptCacheHitTokens
	}
	return u.PromptDetails.CachedTokens
}

func (u usageInfo) cacheRatio() float64 {
	denom := u.PromptCacheHitTokens + u.PromptCacheMissTokens
	if denom == 0 {
		denom = u.PromptTokens
	}
	if denom == 0 {
		return 0
	}
	return float64(u.cachedTokens()) / float64(denom)
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			// 工具调用按 index 分片下发: 名字只在首片给, arguments 是跨帧拼接的 JSON 文本。
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usageInfo `json:"usage"`
}

type turn struct {
	status       int
	frames       int
	stream       bool
	done         bool
	readErr      string
	doubleFrame  int
	invalidPay   string
	content      string
	reasoning    string
	object       string
	finishReason string
	toolCalls    []toolCall
	usage        *usageInfo
	rawHead      string
}

type manifestModel struct {
	ID                     string   `json:"id"`
	OnlyReasoning          bool     `json:"onlyReasoning"`
	SupportsReasoning      bool     `json:"supportsReasoning"`
	SupportedEfforts       []string `json:"supportedEfforts"`
	DefaultReasoningEffort string   `json:"defaultReasoningEffort"`
}

type manifestFile struct {
	Models []manifestModel `json:"models"`
}

type runner struct {
	base     string
	apiKey   string
	model    string
	modelDef *manifestModel
	client   *http.Client
}

type verdict struct {
	name   string
	state  string
	detail string
}

func secRun(name string) string {
	out, err := exec.Command("sec-run", "printenv", name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// readManagementToken 优先读沙箱 per-run 密钥文件, 未指定时回落 sec-run 的生产密钥。
func readManagementToken(path string) string {
	if strings.TrimSpace(path) == "" {
		return secRun("CPA_TOKEN")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func (r *runner) managementGet(token, path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(r.base, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", token)
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("管理面 %s 返回 HTTP %d", path, resp.StatusCode)
	}
	return body, nil
}

func (r *runner) firstAPIKey(token string) (string, error) {
	body, err := r.managementGet(token, "/v0/management/api-keys")
	if err != nil {
		return "", err
	}
	var doc struct {
		Keys []string `json:"api-keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("解析 api-keys 响应失败: %w", err)
	}
	for _, k := range doc.Keys {
		if strings.TrimSpace(k) != "" {
			return k, nil
		}
	}
	// 沙箱默认不配 api-keys, 此时 /v1 不鉴权。
	return "", nil
}

type chatRequest struct {
	messages []chatMessage
	effort   string
	stream   bool
	tools    []toolSpec
	// model 覆盖本次请求的模型 id (缺省用 runner 的被测模型), 供未知模型守卫场景使用。
	model string
}

func (r *runner) chat(req chatRequest) (turn, error) {
	model := r.model
	if strings.TrimSpace(req.model) != "" {
		model = req.model
	}
	payload := map[string]any{
		"model":    model,
		"messages": req.messages,
		"stream":   req.stream,
	}
	if req.stream {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	if req.effort != "" {
		payload["reasoning_effort"] = req.effort
	}
	if len(req.tools) > 0 {
		payload["tools"] = req.tools
		payload["tool_choice"] = "auto"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return turn{}, err
	}

	httpReq, err := http.NewRequest(http.MethodPost, strings.TrimRight(r.base, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return turn{}, err
	}
	// 客户端密钥只进请求头, 不进日志与错误信息。
	if r.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+r.apiKey)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(httpReq)
	if err != nil {
		return turn{}, err
	}
	defer resp.Body.Close()
	// 读取中断比"帧少了"更隐蔽: 不接住错误的话, 尾部帧(含 usage)凭空消失,
	// 上层只会看到缓存与推理字数莫名变少。
	raw, readErr := io.ReadAll(resp.Body)

	out := turn{status: resp.StatusCode, stream: req.stream}
	if readErr != nil {
		out.readErr = readErr.Error()
	}
	if len(raw) > 0 {
		head := raw
		if len(head) > 160 {
			head = head[:160]
		}
		out.rawHead = strings.TrimSpace(string(head))
	}
	if resp.StatusCode >= 300 {
		return out, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	if !req.stream {
		var single struct {
			Object  string `json:"object"`
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Message      struct {
					Content          string     `json:"content"`
					ReasoningContent string     `json:"reasoning_content"`
					ToolCalls        []toolCall `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
			Usage *usageInfo `json:"usage"`
		}
		if err := json.Unmarshal(raw, &single); err != nil {
			return out, fmt.Errorf("解析非流式响应失败")
		}
		out.object = single.Object
		if len(single.Choices) > 0 {
			out.content = single.Choices[0].Message.Content
			out.reasoning = single.Choices[0].Message.ReasoningContent
			out.finishReason = single.Choices[0].FinishReason
			out.toolCalls = single.Choices[0].Message.ToolCalls
		}
		out.usage = single.Usage
		if readErr != nil {
			return out, fmt.Errorf("响应体读取中断: %s", readErr)
		}
		return out, nil
	}

	out = parseStream(raw, out)
	if readErr != nil {
		return out, fmt.Errorf("响应体读取中断(已收到 %d 帧, 终止帧 %v): %s", out.frames, out.done, readErr)
	}
	return out, nil
}

func parseStream(raw []byte, out turn) turn {
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		out.frames++
		layers := 1
		for strings.HasPrefix(payload, "data:") {
			layers++
			payload = strings.TrimSpace(strings.TrimPrefix(payload, "data:"))
		}
		if layers > 1 {
			out.doubleFrame++
			if out.invalidPay == "" {
				out.invalidPay = truncate(payload, 60)
			}
		}
		if payload == "" {
			continue
		}
		if payload == doneFrame {
			out.done = true
			continue
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			if out.invalidPay == "" {
				out.invalidPay = truncate(payload, 60)
			}
			continue
		}
		for _, c := range chunk.Choices {
			out.content += c.Delta.Content
			out.reasoning += c.Delta.ReasoningContent
			if c.FinishReason != "" {
				out.finishReason = c.FinishReason
			}
			for _, call := range c.Delta.ToolCalls {
				// 名字只在首片给, arguments 跨帧拼; 丢任何一片都会得到半截 JSON。
				for len(out.toolCalls) <= call.Index {
					out.toolCalls = append(out.toolCalls, toolCall{Type: "function"})
				}
				slot := &out.toolCalls[call.Index]
				if call.ID != "" {
					slot.ID = call.ID
				}
				if call.Function.Name != "" {
					slot.Function.Name = call.Function.Name
				}
				slot.Function.Arguments += call.Function.Arguments
			}
		}
		if chunk.Usage != nil {
			out.usage = chunk.Usage
		}
	}
	return out
}

func truncate(s string, n int) string {
	runes := []rune(strings.ReplaceAll(s, "\n", " "))
	if len(runes) <= n {
		return string(runes)
	}
	return string(runes[:n]) + "…"
}

func effortRank(e string) int {
	switch strings.ToLower(e) {
	case "minimal", "none":
		return 0
	case "low":
		return 1
	case "medium":
		return 2
	case "high":
		return 3
	case "xhigh", "max":
		return 4
	default:
		return 2
	}
}

func buildPrefix(repeats int) string {
	paragraph := "以下为会话背景资料, 请把它视为固定上下文的一部分, 不要在回答中复述。" +
		"资料编号 %d: 季度复核的结论是流程本身没有缺陷, 记录格式的偏差来自模板版本不一致, " +
		"下一轮先把模板收敛到单一来源, 再评估是否需要调整字段口径。"
	var sb strings.Builder
	for i := range repeats {
		fmt.Fprintf(&sb, paragraph+"\n", i+1)
	}
	return sb.String()
}

func main() {
	base := flag.String("base", "http://127.0.0.1:18317", "目标实例地址")
	model := flag.String("model", "hy3", "被测模型 id, 形如 <plugin>/<model>")
	manifestPath := flag.String("manifest", "", "静态模型清单路径, 默认按模型 id 的插件段推导")
	prefixRepeats := flag.Int("prefix", 120, "长前缀段落重复次数, 用于制造可命中缓存的前缀")
	timeout := flag.Duration("timeout", 180*time.Second, "单个请求超时")
	tokenFile := flag.String("token-file", "", "管理密钥文件路径 (本地沙箱 per-run key), 设置时优先于 sec-run")
	listScenarios := flag.Bool("list", false, "只打印可用场景与其请求成本, 不发请求")
	var requested listFlag
	flag.Var(&requested, "scenarios", "要执行的场景, 逗号分隔或重复传参; 默认 "+strings.Join(defaultScenarios, ","))
	flag.Parse()

	if *listScenarios {
		printScenarioCatalog()
		return
	}

	chosen, unknown := resolveScenarios(requested)
	if len(unknown) > 0 {
		fmt.Fprintf(os.Stderr, "[-] 未知场景: %s\n\n", strings.Join(unknown, ", "))
		printScenarioCatalog()
		os.Exit(2)
	}
	if len(chosen) == 0 {
		fmt.Fprintln(os.Stderr, "[-] 没有任何可执行场景")
		os.Exit(2)
	}

	token := readManagementToken(*tokenFile)
	if token == "" {
		if *tokenFile != "" {
			fmt.Fprintf(os.Stderr, "[-] 读取 token-file 失败: %s\n", *tokenFile)
		} else {
			fmt.Fprintln(os.Stderr, "[-] 未能通过 sec-run 读取到 CPA_TOKEN, 不发任何请求")
		}
		os.Exit(3)
	}

	run := &runner{base: *base, model: *model, client: &http.Client{Timeout: *timeout}}
	run.modelDef = loadModelDef(manifestPathFor(*model, *manifestPath), *model)

	key, err := run.firstAPIKey(token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] %v\n", err)
		os.Exit(3)
	}
	run.apiKey = key

	ids := make([]string, 0, len(chosen))
	for _, s := range chosen {
		ids = append(ids, s.id)
	}

	fmt.Printf("目标 %s 模型 %s\n", *base, *model)
	fmt.Printf("  执行场景: %s (共 %d 次请求, 未点名的判据不执行)\n", strings.Join(ids, ", "), requestsOf(ids))
	if run.modelDef == nil {
		fmt.Println("  静态清单里没有该模型, 能力判据将退化为默认假设")
	} else {
		fmt.Printf("  清单声明: 思考=%v 仅思考=%v 档位=%v 默认=%s\n",
			run.modelDef.SupportsReasoning, run.modelDef.OnlyReasoning,
			run.modelDef.SupportedEfforts, run.modelDef.DefaultReasoningEffort)
	}
	fmt.Println()

	// 多轮会话需先跑: probe 与 effort 复用同一条长前缀, 顺序颠倒会丢掉缓存命中。
	sess := &session{runner: run, prefix: buildPrefix(*prefixRepeats), codeword: "ZQ-7741"}
	for _, s := range chosen {
		s.run(sess)
		fmt.Println()
	}

	passed, warned, failed := sess.summary()
	fmt.Printf("结论: %d 项通过, %d 项警告, %d 项失败 (场景: %s)\n",
		passed, warned, failed, strings.Join(ids, ","))
	if failed > 0 {
		os.Exit(1)
	}
}

// runSession 用一次多轮会话同时观察帧合规、正文、上下文记忆与多轮前缀缓存。
func runSession(s *session) {
	fmt.Println("[多轮会话] 长前缀 + 暗号")
	turn1, err1 := s.chat(chatRequest{messages: []chatMessage{
		{Role: "system", Content: s.prefix},
		{Role: "user", Content: "记住暗号 " + s.codeword + "，稍后我会问你。只回复 OK"},
	}, stream: true})
	checkTurn(s, "轮次1 流式", turn1, err1)

	history := []chatMessage{
		{Role: "system", Content: s.prefix},
		{Role: "user", Content: "记住暗号 " + s.codeword + "，稍后我会问你。只回复 OK"},
		{Role: "assistant", Content: turn1.content},
		{Role: "user", Content: "暗号是什么？只回复暗号本身"},
	}
	turn2, err2 := s.chat(chatRequest{messages: history, stream: true})
	checkTurn(s, "轮次2 流式", turn2, err2)
	checkFraming(s, turn2)
	// 思考判据不挂这一轮: 提示词是复述型, 上游本就不一定思考, 拿它判思考输出是错靶子。
	checkText(s, turn2)

	if err2 == nil {
		if strings.Contains(turn2.content, s.codeword) {
			s.record("上下文记忆", "PASS", "轮次2 正确复现暗号")
		} else {
			s.record("上下文记忆", "FAIL", "轮次2 未复现暗号, 实际回复 "+truncate(turn2.content, 60))
		}
	}

	turn3, err3 := s.chat(chatRequest{messages: append(history,
		chatMessage{Role: "assistant", Content: turn2.content},
		chatMessage{Role: "user", Content: "把刚才那段背景资料的第一条编号复述出来，只回复编号数字"},
	), stream: true})
	checkTurn(s, "轮次3 流式", turn3, err3)

	reportCache(s, "多轮缓存(轮次2)", turn2)
	reportCache(s, "多轮缓存(轮次3)", turn3)

	for _, t := range []struct {
		name string
		turn turn
		err  error
	}{{"轮次2", turn2, err2}, {"轮次3", turn3, err3}} {
		if t.err != nil || t.turn.usage != nil {
			continue
		}
		s.record("用量上报 "+t.name, "FAIL", "流式响应没有 usage, 无法计算缓存率")
	}
}

// runProbe 同一请求连发两次: 第二次必须命中前缀缓存, 否则说明请求头每轮变动导致上游认新会话。
func runProbe(s *session) {
	fmt.Println("[缓存] 同一请求连发两次, 第二次应命中前缀缓存")
	probe := []chatMessage{
		{Role: "system", Content: s.prefix},
		{Role: "user", Content: "只回复 OK"},
	}
	probeFirst, errP1 := s.chat(chatRequest{messages: probe, stream: true})
	checkTurn(s, "探针 首次", probeFirst, errP1)
	probeSecond, errP2 := s.chat(chatRequest{messages: probe, stream: true})
	checkTurn(s, "探针 二次", probeSecond, errP2)

	reportCache(s, "重复请求缓存(二次)", probeSecond)
	if errP2 == nil && probeSecond.usage == nil {
		s.record("用量上报 探针二次", "FAIL", "流式响应没有 usage, 无法计算缓存率")
	}
}

// runNonStream 覆盖上游只支持流式的场景: 这条必须由插件聚合后返回完整响应。
func runNonStream(s *session) {
	fmt.Println("[非流式] 单次请求应返回完整 chat.completion")
	nonStream, err := s.chat(chatRequest{messages: []chatMessage{
		{Role: "user", Content: "只回复四个字: 链路正常"},
	}})
	checkNonStream(s, nonStream, err)
}

// runTools 覆盖工具定义透传与工具结果消费, 两轮之间要把调用原样带回。
func runTools(s *session) {
	fmt.Println("[工具调用] 工具定义透传 + 工具结果消费")
	checkTools(s)
}

// runGuard 验证宿主只接受自己报送过的模型 id: 未知 id 必须在路由阶段被拒
// (400 model_not_found), 不进入凭据与上游阶段。对照: 同一沙箱里合法模型会走到
// auth_not_found (无凭据时 503), 说明二者在宿主里的处理阶段不同。
func runGuard(s *session) {
	fmt.Println("[模型守卫] 未知模型 id 应由宿主拒绝")
	unknown := s.model + "-not-a-real-model"
	t, err := s.chat(chatRequest{
		messages: []chatMessage{{Role: "user", Content: "只回复 OK"}},
		model:    unknown,
	})
	if err == nil {
		s.record("未知模型拒绝", "FAIL",
			fmt.Sprintf("未知模型 %s 未被拒绝 (HTTP %d), 未在路由阶段拦下", unknown, t.status))
		return
	}
	if t.status != http.StatusBadRequest {
		s.record("未知模型拒绝", "FAIL",
			fmt.Sprintf("未知模型 %s 返回 HTTP %d, 期望 400: %s", unknown, t.status, truncate(t.rawHead, 80)))
		return
	}
	if !strings.Contains(t.rawHead, "model_not_found") {
		s.record("未知模型拒绝", "FAIL", "拒绝理由不是 model_not_found: "+truncate(t.rawHead, 80))
		return
	}
	s.record("未知模型拒绝", "PASS", "未知模型在路由阶段被拒 (400 model_not_found), 未进入凭据与上游阶段")
}

// runEffort 对比最低档与最高档: 思考深度有没有真的传到上游。
func runEffort(s *session) {
	if s.modelDef == nil || len(s.modelDef.SupportedEfforts) == 0 {
		fmt.Println("[思考深度] 跳过: 清单未声明该模型的档位")
		return
	}

	efforts := append([]string(nil), s.modelDef.SupportedEfforts...)
	sort.Slice(efforts, func(i, j int) bool { return effortRank(efforts[i]) < effortRank(efforts[j]) })
	low, high := efforts[0], efforts[len(efforts)-1]
	fmt.Printf("[思考深度] 对比 reasoning_effort=%s 与 %s\n", low, high)

	prompt := []chatMessage{{Role: "user", Content: "在 1 到 300 之间, 有多少个整数的十进制写法里出现过字符 7? 先推演再给结论, 结论单独一行写 答案=N"}}
	lowTurn, lowErr := s.chat(chatRequest{messages: prompt, effort: low, stream: true})
	checkTurn(s, "低档 "+low, lowTurn, lowErr)
	highTurn, highErr := s.chat(chatRequest{messages: prompt, effort: high, stream: true})
	checkTurn(s, "高档 "+high, highTurn, highErr)
	if highErr == nil {
		checkCoT(s, highTurn)
	}

	lowReasoning, highReasoning := -1, -1
	if lowTurn.usage != nil {
		lowReasoning = lowTurn.usage.CompletionDetails.ReasoningTokens
	}
	if highTurn.usage != nil {
		highReasoning = highTurn.usage.CompletionDetails.ReasoningTokens
	}
	lowChars := len([]rune(lowTurn.reasoning))
	highChars := len([]rune(highTurn.reasoning))

	switch {
	case lowErr != nil || highErr != nil:
		s.record("思考深度传递", "FAIL", "请求未成功, 无法比较")
	case lowReasoning >= 0 && highReasoning >= 0:
		switch {
		case highReasoning > lowReasoning:
			s.record("思考深度传递", "PASS",
				fmt.Sprintf("reasoning_tokens %s=%d < %s=%d", low, lowReasoning, high, highReasoning))
		case highReasoning == lowReasoning:
			s.record("思考深度传递", "WARN",
				fmt.Sprintf("两档 reasoning_tokens 相同(%d), 可能是采样波动, 也可能档位没传到上游", highReasoning))
		default:
			s.record("思考深度传递", "FAIL",
				fmt.Sprintf("高档推理反而更少: %s=%d > %s=%d", low, lowReasoning, high, highReasoning))
		}
	default:
		// 上游没回报 usage 时退到推理正文长度: 档位有没有传到上游, 从推理量仍看得出来。
		switch {
		case lowChars == 0 || highChars == 0:
			s.record("思考深度传递", "FAIL",
				fmt.Sprintf("两档都没有 reasoning_tokens, 推理正文也是空的 (low=%d 字, high=%d 字)", lowChars, highChars))
		case highChars > lowChars:
			s.record("思考深度传递", "PASS",
				fmt.Sprintf("usage 缺失, 按推理字数比较: %s=%d 字 < %s=%d 字", low, lowChars, high, highChars))
		case highChars == lowChars:
			s.record("思考深度传递", "WARN",
				fmt.Sprintf("usage 缺失, 两档推理字数相同(%d), 分不出档位", highChars))
		default:
			s.record("思考深度传递", "FAIL",
				fmt.Sprintf("usage 缺失, 高档推理反而更少: %s=%d 字 > %s=%d 字", low, lowChars, high, highChars))
		}
	}
}

func checkTurn(s *session, label string, t turn, err error) {
	if err != nil {
		s.record(label, "FAIL", "请求失败: "+err.Error()+" 响应头: "+truncate(t.rawHead, 80))
		return
	}
	if t.stream && !t.done {
		s.record(label, "FAIL", fmt.Sprintf("流式响应没有终止帧, 只收到 %d 帧", t.frames))
		return
	}
	s.record(label, "OK", fmt.Sprintf("HTTP %d, %d 帧, 正文 %d 字, 推理 %d 字",
		t.status, t.frames, len([]rune(t.content)), len([]rune(t.reasoning))))
}

func checkFraming(s *session, t turn) {
	if t.doubleFrame > 0 {
		s.record("流式帧合规", "FAIL",
			fmt.Sprintf("%d 帧带双重 data 前缀, 标准客户端无法解析, 首个载荷: %s", t.doubleFrame, t.invalidPay))
		return
	}
	if t.invalidPay != "" {
		s.record("流式帧合规", "FAIL", "存在非 JSON 载荷: "+t.invalidPay)
		return
	}
	if !t.done {
		s.record("流式帧合规", "FAIL", "缺少终止帧 [DONE]")
		return
	}
	s.record("流式帧合规", "PASS", fmt.Sprintf("%d 帧全部为合法 JSON, 终止帧存在", t.frames))
}

// checkTools 跑两轮: 首轮看模型是否真的发起工具调用, 次轮把调用与工具结果带回, 看模型是否消费了结果。
// 工具定义与工具结果都要经宿主与插件原样往返, 任一层丢帧或拼错 arguments 都在这里现形。
func checkTools(s *session) {
	tools := []toolSpec{weatherTool()}
	first := []chatMessage{{Role: "user", Content: "北京现在天气怎么样? 必须调用 get_weather 工具查, 不要凭记忆回答"}}

	streamed, streamErr := s.chat(chatRequest{messages: first, stream: true, tools: tools})
	reportToolCall(s, "工具调用 流式", streamed, streamErr, true)

	aggregated, aggregatedErr := s.chat(chatRequest{messages: first, tools: tools})
	reportToolCall(s, "工具调用 非流式", aggregated, aggregatedErr, false)

	if len(streamed.toolCalls) == 0 {
		s.record("工具结果消费", "FAIL", "首轮没有拿到可回填的 tool_calls, 次轮无从发起")
		return
	}
	call := streamed.toolCalls[0]
	second := append(append([]chatMessage(nil), first...),
		chatMessage{Role: "assistant", ToolCalls: []toolCall{call}},
		chatMessage{Role: "tool", ToolCallID: call.ID, Content: toolResult},
	)
	consumed, consumedErr := s.chat(chatRequest{messages: second, stream: true, tools: tools})
	if consumedErr != nil {
		s.record("工具结果消费", "FAIL", "请求失败: "+consumedErr.Error()+" 响应头: "+truncate(consumed.rawHead, 80))
		return
	}
	if strings.Contains(consumed.content, "26") || strings.Contains(consumed.content, "晴") {
		s.record("工具结果消费", "PASS", "模型引用了工具返回的天气 "+truncate(consumed.content, 40))
		return
	}
	s.record("工具结果消费", "FAIL", "模型没有引用工具结果, 实际回复 "+truncate(consumed.content, 60))
}

func weatherTool() toolSpec {
	var spec toolSpec
	spec.Type = "function"
	spec.Function.Name = "get_weather"
	spec.Function.Description = "查询指定城市的实时天气"
	spec.Function.Parameters = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"city": map[string]any{"type": "string", "description": "城市名"},
		},
		"required": []string{"city"},
	}
	return spec
}

func reportToolCall(s *session, label string, t turn, err error, streaming bool) {
	if err != nil {
		s.record(label, "FAIL", "请求失败: "+err.Error()+" 响应头: "+truncate(t.rawHead, 80))
		return
	}
	if streaming && !t.done {
		s.record(label, "FAIL", fmt.Sprintf("流式响应没有终止帧, 只收到 %d 帧", t.frames))
		return
	}
	if len(t.toolCalls) == 0 {
		s.record(label, "FAIL", "响应没有 tool_calls, 工具定义没透传到上游; 实际正文 "+truncate(t.content, 40))
		return
	}
	call := t.toolCalls[0]
	if call.ID == "" {
		s.record(label, "FAIL", "tool_calls 缺 id, 次轮无法把工具结果对回调用")
		return
	}
	if call.Function.Name == "" {
		s.record(label, "FAIL", "tool_calls 缺函数名, 名字所在的帧被丢了")
		return
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		s.record(label, "FAIL", fmt.Sprintf("%s 的 arguments 不是合法 JSON, 分片拼接有误: %s",
			call.Function.Name, truncate(call.Function.Arguments, 60)))
		return
	}
	if t.finishReason != "tool_calls" {
		s.record(label, "FAIL", fmt.Sprintf("finish_reason 应为 tool_calls, 实际 %q", t.finishReason))
		return
	}
	s.record(label, "PASS", fmt.Sprintf("finish=%s, 调用 %s, arguments %s",
		t.finishReason, call.Function.Name, truncate(call.Function.Arguments, 50)))
}

func checkNonStream(s *session, t turn, err error) {
	if err != nil {
		s.record("非流式链路", "FAIL", "请求失败: "+err.Error()+" 响应头: "+truncate(t.rawHead, 80))
		return
	}
	if t.object != "chat.completion" {
		s.record("非流式链路", "FAIL", "object 应为 chat.completion, 实际 "+truncate(t.object, 40))
		return
	}
	if strings.TrimSpace(t.content) == "" && strings.TrimSpace(t.reasoning) == "" {
		s.record("非流式链路", "FAIL", "聚合结果既无正文也无推理内容")
		return
	}
	if t.finishReason == "" {
		s.record("非流式链路", "FAIL", "缺 finish_reason")
		return
	}
	usage := "无 usage"
	if t.usage != nil {
		usage = fmt.Sprintf("prompt=%d completion=%d", t.usage.PromptTokens, t.usage.CompletionTokens)
	}
	s.record("非流式链路", "PASS",
		fmt.Sprintf("HTTP %d, finish=%s, 正文 %d 字, %s", t.status, t.finishReason, len([]rune(t.content)), usage))
}

// checkCoT 只在推演型提示词 + 该模型最高档那一轮调用: 那里上游必出思考, 判据才只反映插件转发是否忠实。
// 复述型提示词 (暗号、只回复两个字) 上游本就不一定思考, 拿它判思考输出是错靶子。
func checkCoT(s *session, t turn) {
	if strings.TrimSpace(t.reasoning) != "" {
		s.record("思维链输出", "PASS",
			fmt.Sprintf("推理增量 %d 字, 首段: %s", len([]rune(t.reasoning)), truncate(t.reasoning, 40)))
		return
	}
	if t.usage != nil && t.usage.CompletionDetails.ReasoningTokens > 0 {
		s.record("思维链输出", "FAIL",
			fmt.Sprintf("上游上报 reasoning_tokens=%d 但流里没有 reasoning_content, 插件丢了思考帧",
				t.usage.CompletionDetails.ReasoningTokens))
		return
	}
	s.record("思维链输出", "FAIL", "推演型提示词在上限档未出思考: 先查 effort 有没有传到上游")
}

func checkText(s *session, t turn) {
	if strings.TrimSpace(t.content) == "" {
		s.record("正文输出", "FAIL", "content 为空")
		return
	}
	s.record("正文输出", "PASS", truncate(t.content, 60))
}

func reportCache(s *session, name string, t turn) {
	if t.usage == nil {
		s.record(name, "FAIL", "没有 usage, 无法判断缓存")
		return
	}
	hit := t.usage.cachedTokens()
	if hit > 0 {
		s.record(name, "PASS", fmt.Sprintf("命中 %d, 未命中 %d, 命中率 %.1f%%",
			hit, t.usage.PromptCacheMissTokens, t.usage.cacheRatio()*100))
		return
	}
	s.record(name, "FAIL", fmt.Sprintf("命中 0, prompt=%d 全部未命中, 前缀缓存没有生效",
		t.usage.PromptTokens))
}
