package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

func main() {
	base := flag.String("base", "http://127.0.0.1:18317", "目标地址")
	path := flag.String("path", "/v0/management/plugins", "管理面路径")
	method := flag.String("method", http.MethodGet, "请求方法")
	body := flag.String("body", "", "请求体")
	flag.Parse()

	out, err := exec.Command("sec-run", "printenv", "CPA_TOKEN").Output()
	key := strings.TrimSpace(string(out))
	if err != nil || key == "" {
		fmt.Fprintln(os.Stderr, "[-] 未能通过 sec-run 读取到 CPA_TOKEN，不发请求")
		os.Exit(3)
	}

	endpoint := strings.TrimRight(*base, "/") + "/" + strings.TrimLeft(*path, "/")
	var payload io.Reader
	if *body != "" {
		payload = bytes.NewReader([]byte(*body))
	}

	req, err := http.NewRequest(strings.ToUpper(*method), endpoint, payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] 构造请求失败: %v\n", err)
		os.Exit(2)
	}
	req.Header.Set("Authorization", key)
	if *body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] 请求失败: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if v := resp.Header.Get("X-CPA-VERSION"); v != "" {
		fmt.Fprintf(os.Stderr, "[host] X-CPA-VERSION=%s\n", v)
	}
	fmt.Fprintf(os.Stderr, "[res] HTTP %d\n", resp.StatusCode)
	os.Stdout.Write(redactBody(raw, resp.Header.Get("Content-Type")))
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		fmt.Println()
	}
	if resp.StatusCode >= 300 {
		os.Exit(1)
	}
}

// 管理面会原样回吐各类密钥与凭据, 终端输出是它们唯一会外泄的出口, 因此默认全部打码。
// 打码保留长度与前 4 字节哈希, 足以判断「是哪一条、换没换」, 又不足以还原。
var sensitiveKey = regexp.MustCompile(`(?i)^(api[_-]?keys?|keys?|id[_-]?tokens?|access[_-]?tokens?|refresh[_-]?tokens?|tokens?|secrets?|passwords?|credentials?|authorization|cookies?|private[_-]?key)$`)

func redactBody(raw []byte, contentType string) []byte {
	if len(bytes.TrimSpace(raw)) == 0 {
		return raw
	}
	if strings.Contains(contentType, "json") {
		if out, ok := redactJSON(raw); ok {
			return out
		}
	}
	return redactText(raw)
}

func redactJSON(raw []byte) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var doc any
	if err := decoder.Decode(&doc); err != nil {
		return nil, false
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(redactValue(doc)); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

func redactValue(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		for k, val := range typed {
			if sensitiveKey.MatchString(k) {
				typed[k] = maskValue(val)
			} else {
				typed[k] = redactValue(val)
			}
		}
		return typed
	case []any:
		for i := range typed {
			typed[i] = redactValue(typed[i])
		}
		return typed
	default:
		return v
	}
}

func maskValue(v any) any {
	switch typed := v.(type) {
	case string:
		return maskString(typed)
	case []any:
		for i := range typed {
			typed[i] = maskValue(typed[i])
		}
		return typed
	case map[string]any:
		for k := range typed {
			typed[k] = maskValue(typed[k])
		}
		return typed
	default:
		return v
	}
}

func maskString(s string) string {
	if s == "" {
		// 空值不是秘密, 保留原样是为了不掩盖「这个字段没配」这一事实。
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("<redacted len=%d sha256=%s>", len(s), hex.EncodeToString(sum[:4]))
}

var yamlLine = regexp.MustCompile(`^(\s*(?:- )?)([A-Za-z0-9_.-]+)(\s*:\s*)(.+)$`)
var yamlItem = regexp.MustCompile(`^(\s*-\s*)(\S.*)$`)

func redactText(raw []byte) []byte {
	lines := strings.Split(string(raw), "\n")
	sensitiveBlockIndent := -1
	for i, line := range lines {
		if m := yamlLine.FindStringSubmatch(line); m != nil {
			if sensitiveKey.MatchString(m[2]) {
				if strings.TrimSpace(m[4]) == "" {
					sensitiveBlockIndent = len(m[1])
				} else {
					lines[i] = m[1] + m[2] + m[3] + maskString(strings.TrimSpace(m[4]))
				}
			}
			continue
		}
		if m := yamlItem.FindStringSubmatch(line); m != nil && sensitiveBlockIndent >= 0 {
			if len(m[1]) > sensitiveBlockIndent {
				lines[i] = m[1] + maskString(strings.TrimSpace(m[2]))
			} else {
				sensitiveBlockIndent = -1
			}
		}
	}
	return []byte(strings.Join(lines, "\n"))
}
