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
	"sort"
	"strings"
	"time"
)

// 对话链路的验收入口。四项指标来自真实使用需求: 多轮缓存、上下文记忆、
// 思考深度传递、思维链输出; 再加一项流式帧合规, 因为标准客户端读不懂就会直接报错。
//
// 密钥纪律: 管理密钥只经 sec-run printenv CPA_TOKEN 读取, 客户端密钥由管理面就地取得,
// 二者都不得出现在终端输出里, 出错信息也只回显打码后的形态。

const doneFrame = "[DONE]"

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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
		} `json:"delta"`
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

var results []verdict

func record(name, state, detail string) {
	results = append(results, verdict{name, state, detail})
	fmt.Printf("  %-28s %-6s %s\n", name, state, detail)
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

func (r *runner) chat(messages []chatMessage, effort string, stream bool) (turn, error) {
	payload := map[string]any{
		"model":    r.model,
		"messages": messages,
		"stream":   stream,
	}
	if stream {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	if effort != "" {
		payload["reasoning_effort"] = effort
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return turn{}, err
	}

	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(r.base, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return turn{}, err
	}
	// 客户端密钥只进请求头, 不进日志与错误信息。
	if r.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.apiKey)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return turn{}, err
	}
	defer resp.Body.Close()
	// 读取中断比"帧少了"更隐蔽: 不接住错误的话, 尾部帧(含 usage)凭空消失,
	// 上层只会看到缓存与推理字数莫名变少。
	raw, readErr := io.ReadAll(resp.Body)

	out := turn{status: resp.StatusCode, stream: stream}
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

	if !stream {
		var single struct {
			Object  string `json:"object"`
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Message      struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
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
	model := flag.String("model", "hy3", "被测模型 id")
	manifestPath := flag.String("manifest", "plugins/workbuddy/data/static-config.json", "静态模型清单路径")
	prefixRepeats := flag.Int("prefix", 120, "长前缀段落重复次数, 用于制造可命中缓存的公共前缀")
	timeout := flag.Duration("timeout", 180*time.Second, "单个请求超时")
	tokenFile := flag.String("token-file", "", "管理密钥文件路径 (本地沙箱 per-run key), 设置时优先于 sec-run")
	flag.Parse()

	client := &http.Client{Timeout: *timeout}
	run := &runner{base: *base, model: *model, client: client}

	token := readManagementToken(*tokenFile)
	if token == "" {
		if *tokenFile != "" {
			fmt.Fprintf(os.Stderr, "[-] 读取 token-file 失败: %s\n", *tokenFile)
		} else {
			fmt.Fprintln(os.Stderr, "[-] 未能通过 sec-run 读取到 CPA_TOKEN, 不发任何请求")
		}
		os.Exit(3)
	}

	if raw, err := os.ReadFile(*manifestPath); err == nil {
		var doc manifestFile
		if json.Unmarshal(raw, &doc) == nil {
			// 宿主注册 id 形如 <plugin>/<model>, 清单存裸 id。剥掉最后一个 "/" 之前的部分,
			// 不能写死某个插件的前缀, 否则换插件时清单整个查不到, 思考判据会被静默跳过。
			bareID := *model
			if idx := strings.LastIndex(bareID, "/"); idx >= 0 {
				bareID = bareID[idx+1:]
			}
			for i := range doc.Models {
				if strings.EqualFold(doc.Models[i].ID, bareID) {
					run.modelDef = &doc.Models[i]
					break
				}
			}
		}
	}

	key, err := run.firstAPIKey(token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] %v\n", err)
		os.Exit(3)
	}
	run.apiKey = key

	fmt.Printf("目标 %s 模型 %s\n", *base, *model)
	if run.modelDef == nil {
		fmt.Println("  静态清单里没有该模型, 能力判据将退化为默认假设")
	} else {
		fmt.Printf("  清单声明: 思考=%v 仅思考=%v 档位=%v 默认=%s\n",
			run.modelDef.SupportsReasoning, run.modelDef.OnlyReasoning,
			run.modelDef.SupportedEfforts, run.modelDef.DefaultReasoningEffort)
	}
	fmt.Println()

	prefix := buildPrefix(*prefixRepeats)
	codeword := "ZQ-7741"

	// ---- 指标 1 + 2 + 4: 一次多轮会话同时观察缓存、记忆与思维链 ----
	fmt.Println("[多轮会话] 长前缀 + 暗号")
	turn1, err1 := run.chat([]chatMessage{
		{Role: "system", Content: prefix},
		{Role: "user", Content: "记住暗号 " + codeword + "，稍后我会问你。只回复 OK"},
	}, "", true)
	checkTurn("轮次1 流式", turn1, err1)

	history := []chatMessage{
		{Role: "system", Content: prefix},
		{Role: "user", Content: "记住暗号 " + codeword + "，稍后我会问你。只回复 OK"},
		{Role: "assistant", Content: turn1.content},
		{Role: "user", Content: "暗号是什么？只回复暗号本身"},
	}
	turn2, err2 := run.chat(history, "", true)
	checkTurn("轮次2 流式", turn2, err2)
	checkFraming(turn2)
	// 思考判据不挂这一轮: 提示词是复述型, 上游本就不一定思考, 拿它判思考输出是错靶子。
	checkText(turn2)

	if err2 == nil {
		if strings.Contains(turn2.content, codeword) {
			record("上下文记忆", "PASS", "轮次2 正确复现暗号")
		} else {
			record("上下文记忆", "FAIL", "轮次2 未复现暗号, 实际回复 "+truncate(turn2.content, 60))
		}
	}

	turn3, err3 := run.chat(append(history,
		chatMessage{Role: "assistant", Content: turn2.content},
		chatMessage{Role: "user", Content: "把刚才那段背景资料的第一条编号复述出来，只回复编号数字"},
	), "", true)
	checkTurn("轮次3 流式", turn3, err3)

	fmt.Println()
	fmt.Println("[缓存] 同一请求连发两次, 第二次应命中前缀缓存")
	probe := []chatMessage{
		{Role: "system", Content: prefix},
		{Role: "user", Content: "只回复 OK"},
	}
	probeFirst, errP1 := run.chat(probe, "", true)
	checkTurn("探针 首次", probeFirst, errP1)
	probeSecond, errP2 := run.chat(probe, "", true)
	checkTurn("探针 二次", probeSecond, errP2)

	reportCache("多轮缓存(轮次2)", turn2)
	reportCache("多轮缓存(轮次3)", turn3)
	reportCache("重复请求缓存(二次)", probeSecond)

	for _, t := range []struct {
		name string
		turn turn
		err  error
	}{{"轮次2", turn2, err2}, {"轮次3", turn3, err3}, {"探针二次", probeSecond, errP2}} {
		if t.err != nil {
			continue
		}
		if t.turn.usage == nil {
			record("用量上报 "+t.name, "FAIL", "流式响应没有 usage, 无法计算缓存率")
		}
	}
	fmt.Println()

	// ---- 非流式: 上游只支持流式, 这条必须由插件聚合后返回完整响应 ----
	fmt.Println("[非流式] 单次请求应返回完整 chat.completion")
	nonStream, nonStreamErr := run.chat([]chatMessage{
		{Role: "user", Content: "只回复四个字: 链路正常"},
	}, "", false)
	checkNonStream(nonStream, nonStreamErr)

	// ---- 指标 3: 思考深度是否真的传到上游 ----
	if run.modelDef != nil && len(run.modelDef.SupportedEfforts) > 0 {
		efforts := append([]string(nil), run.modelDef.SupportedEfforts...)
		sort.Slice(efforts, func(i, j int) bool { return effortRank(efforts[i]) < effortRank(efforts[j]) })
		low, high := efforts[0], efforts[len(efforts)-1]
		fmt.Printf("[思考深度] 对比 reasoning_effort=%s 与 %s\n", low, high)

		prompt := []chatMessage{{Role: "user", Content: "在 1 到 300 之间, 有多少个整数的十进制写法里出现过字符 7? 先推演再给结论, 结论单独一行写 答案=N"}}
		lowTurn, lowErr := run.chat(prompt, low, true)
		checkTurn("低档 "+low, lowTurn, lowErr)
		highTurn, highErr := run.chat(prompt, high, true)
		checkTurn("高档 "+high, highTurn, highErr)
		if highErr == nil {
			checkCoT(highTurn)
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
			record("思考深度传递", "FAIL", "请求未成功, 无法比较")
		case lowReasoning >= 0 && highReasoning >= 0:
			switch {
			case highReasoning > lowReasoning:
				record("思考深度传递", "PASS",
					fmt.Sprintf("reasoning_tokens %s=%d < %s=%d", low, lowReasoning, high, highReasoning))
			case highReasoning == lowReasoning:
				record("思考深度传递", "WARN",
					fmt.Sprintf("两档 reasoning_tokens 相同(%d), 可能是采样波动, 也可能档位没传到上游", highReasoning))
			default:
				record("思考深度传递", "FAIL",
					fmt.Sprintf("高档推理反而更少: %s=%d > %s=%d", low, lowReasoning, high, highReasoning))
			}
		default:
			// 上游没回报 usage 时退到推理正文长度: 档位有没有传到上游, 从推理量仍看得出来。
			switch {
			case lowChars == 0 || highChars == 0:
				record("思考深度传递", "FAIL",
					fmt.Sprintf("两档都没有 reasoning_tokens, 推理正文也是空的 (low=%d 字, high=%d 字)", lowChars, highChars))
			case highChars > lowChars:
				record("思考深度传递", "PASS",
					fmt.Sprintf("usage 缺失, 按推理字数比较: %s=%d 字 < %s=%d 字", low, lowChars, high, highChars))
			case highChars == lowChars:
				record("思考深度传递", "WARN",
					fmt.Sprintf("usage 缺失, 两档推理字数相同(%d), 分不出档位", highChars))
			default:
				record("思考深度传递", "FAIL",
					fmt.Sprintf("usage 缺失, 高档推理反而更少: %s=%d 字 > %s=%d 字", low, lowChars, high, highChars))
			}
		}
	} else {
		fmt.Println("[思考深度] 跳过: 清单未声明该模型的档位")
	}

	fmt.Println()
	failed := 0
	warned := 0
	for _, v := range results {
		switch v.state {
		case "FAIL":
			failed++
		case "WARN":
			warned++
		}
	}
	fmt.Printf("结论: %d 项通过, %d 项警告, %d 项失败\n", len(results)-failed-warned, warned, failed)
	if failed > 0 {
		os.Exit(1)
	}
}

func checkTurn(label string, t turn, err error) {
	if err != nil {
		record(label, "FAIL", "请求失败: "+err.Error()+" 响应头: "+truncate(t.rawHead, 80))
		return
	}
	if t.stream && !t.done {
		record(label, "FAIL", fmt.Sprintf("流式响应没有终止帧, 只收到 %d 帧", t.frames))
		return
	}
	record(label, "OK", fmt.Sprintf("HTTP %d, %d 帧, 正文 %d 字, 推理 %d 字",
		t.status, t.frames, len([]rune(t.content)), len([]rune(t.reasoning))))
}

func checkFraming(t turn) {
	if t.doubleFrame > 0 {
		record("流式帧合规", "FAIL",
			fmt.Sprintf("%d 帧带双重 data 前缀, 标准客户端无法解析, 首个载荷: %s", t.doubleFrame, t.invalidPay))
		return
	}
	if t.invalidPay != "" {
		record("流式帧合规", "FAIL", "存在非 JSON 载荷: "+t.invalidPay)
		return
	}
	if !t.done {
		record("流式帧合规", "FAIL", "缺少终止帧 [DONE]")
		return
	}
	record("流式帧合规", "PASS", fmt.Sprintf("%d 帧全部为合法 JSON, 终止帧存在", t.frames))
}

func checkNonStream(t turn, err error) {
	if err != nil {
		record("非流式链路", "FAIL", "请求失败: "+err.Error()+" 响应头: "+truncate(t.rawHead, 80))
		return
	}
	if t.object != "chat.completion" {
		record("非流式链路", "FAIL", "object 应为 chat.completion, 实际 "+truncate(t.object, 40))
		return
	}
	if strings.TrimSpace(t.content) == "" && strings.TrimSpace(t.reasoning) == "" {
		record("非流式链路", "FAIL", "聚合结果既无正文也无推理内容")
		return
	}
	if t.finishReason == "" {
		record("非流式链路", "FAIL", "缺 finish_reason")
		return
	}
	usage := "无 usage"
	if t.usage != nil {
		usage = fmt.Sprintf("prompt=%d completion=%d", t.usage.PromptTokens, t.usage.CompletionTokens)
	}
	record("非流式链路", "PASS",
		fmt.Sprintf("HTTP %d, finish=%s, 正文 %d 字, %s", t.status, t.finishReason, len([]rune(t.content)), usage))
}

// checkCoT 只在推演型提示词 + 该模型最高档那一轮调用: 那里上游必出思考, 判据才只反映插件转发是否忠实。
// 复述型提示词 (暗号、只回复两个字) 上游本就不一定思考, 拿它判思考输出是错靶子。
func checkCoT(t turn) {
	if strings.TrimSpace(t.reasoning) != "" {
		record("思维链输出", "PASS",
			fmt.Sprintf("推理增量 %d 字, 首段: %s", len([]rune(t.reasoning)), truncate(t.reasoning, 40)))
		return
	}
	if t.usage != nil && t.usage.CompletionDetails.ReasoningTokens > 0 {
		record("思维链输出", "FAIL",
			fmt.Sprintf("上游上报 reasoning_tokens=%d 但流里没有 reasoning_content, 插件丢了思考帧",
				t.usage.CompletionDetails.ReasoningTokens))
		return
	}
	record("思维链输出", "FAIL", "推演型提示词在上限档未出思考: 先查 effort 有没有传到上游")
}

func checkText(t turn) {
	if strings.TrimSpace(t.content) == "" {
		record("正文输出", "FAIL", "content 为空")
		return
	}
	record("正文输出", "PASS", truncate(t.content, 60))
}

func reportCache(name string, t turn) {
	if t.usage == nil {
		record(name, "FAIL", "没有 usage, 无法判断缓存")
		return
	}
	hit := t.usage.cachedTokens()
	if hit > 0 {
		record(name, "PASS", fmt.Sprintf("命中 %d, 未命中 %d, 命中率 %.1f%%",
			hit, t.usage.PromptCacheMissTokens, t.usage.cacheRatio()*100))
		return
	}
	record(name, "FAIL", fmt.Sprintf("命中 0, prompt=%d 全部未命中, 前缀缓存没有生效",
		t.usage.PromptTokens))
}
