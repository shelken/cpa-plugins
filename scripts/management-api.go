package main

// 管理面统一入口: 封装密钥来源与请求头形式, 避免每次手搓 curl 时在鉴权上翻车。
//
// 用法:
//
//	go run scripts/management-api.go -sandbox workbuddy -path /v0/management/plugins
//	go run scripts/management-api.go -base http://192.168.69.60:8317 \
//	    -key-cmd 'sec-run printenv CPA_TOKEN' -path /v0/management/auth-files
//	go run scripts/management-api.go -base http://192.168.69.60:8317 \
//	    -key-file ~/.cache/cpa-plugins/keys/home-ops -method POST \
//	    -path /v0/management/quota/fetch -body '{"auth_index":"..."}'
//
// 密钥只从 -key-file / -key-name / -key-cmd / -key-env / 沙箱目录读取, 不打印、不落盘、不进 shell 历史。
// 只发一次请求, 不重试: 宿主对连续失败尝试会按 IP 封禁。
// 响应体走 stdout, 诊断行(请求行、密钥来源、宿主版本头、状态码、处置建议)走 stderr, 所以可以直接接 jq。

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultSandboxPort = 18317
	defaultPath        = "/v0/management/plugins"
	maxBodyBytes       = 8 << 20
)

func main() {
	sandboxID := flag.String("sandbox", "", "沙箱 id, 读 ~/.cache/cpa-plugins/sandbox/<id> 的宿主与密钥")
	sandboxDir := flag.String("dir", "", "沙箱目录, 默认 ~/.cache/cpa-plugins/sandbox/<id>")
	base := flag.String("base", "", "目标地址, 默认 http://127.0.0.1:18317")
	port := flag.Int("port", defaultSandboxPort, "宿主端口, 仅在 -sandbox 没给 -base 时用于拼地址")
	path := flag.String("path", defaultPath, "管理面路径, 如 /v0/management/plugins")
	method := flag.String("method", http.MethodGet, "请求方法")
	body := flag.String("body", "", "请求体, POST 用")
	keyFile := flag.String("key-file", "", "从文件读管理密钥")
	keyName := flag.String("key-name", "", "从 ~/.cache/cpa-plugins/keys/<名字> 读管理密钥")
	keyCmd := flag.String("key-cmd", "", "执行命令取管理密钥, 如 sec-run printenv CPA_TOKEN")
	keyEnv := flag.String("key-env", "", "从环境变量取管理密钥")
	authStyle := flag.String("auth", "authorization", "密钥放法: authorization(Authorization: <key>) 或 x-management-key(X-Management-Key: <key>)")
	timeout := flag.Duration("timeout", 30*time.Second, "单次请求超时")
	proxyURL := flag.String("proxy", "", "代理地址, 默认直连: 内网目标被环境里的 HTTP_PROXY/HTTPS_PROXY 吃掉时会连不上, 所以不读环境代理")
	printHeaders := flag.Bool("headers", false, "额外打印完整响应头")
	flag.Parse()

	key, keySource, err := resolveKey(*sandboxID, *sandboxDir, *keyFile, *keyName, *keyCmd, *keyEnv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] %v\n", err)
		os.Exit(2)
	}

	target := *base
	if target == "" {
		target = fmt.Sprintf("http://127.0.0.1:%d", *port)
	}
	endpoint := strings.TrimRight(target, "/") + "/" + strings.TrimLeft(*path, "/")

	var payload io.Reader
	if *body != "" {
		payload = bytes.NewReader([]byte(*body))
	}
	req, err := http.NewRequest(strings.ToUpper(*method), endpoint, payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] 构造请求失败: %v\n", err)
		os.Exit(2)
	}

	headerName := "Authorization"
	switch strings.ToLower(*authStyle) {
	case "authorization", "":
	case "x-management-key":
		headerName = "X-Management-Key"
	default:
		fmt.Fprintf(os.Stderr, "[-] -auth 只支持 authorization 或 x-management-key, 收到 %q\n", *authStyle)
		os.Exit(2)
	}
	if key == "" {
		fmt.Fprintf(os.Stderr, "[key] 没给密钥来源, 本次只发无密钥请求: 目标会 401, 但宿主自报的版本头照常返回。\n")
	} else {
		req.Header.Set(headerName, key)
	}
	if *body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	fmt.Fprintf(os.Stderr, "[req] %s %s\n", req.Method, endpoint)
	if key != "" {
		fmt.Fprintf(os.Stderr, "[key] 来源 %s, 长度 %d, 放法 %s: <不回显>\n", keySource, len(key), headerName)
	}

	transport := &http.Transport{Proxy: nil}
	if *proxyURL != "" {
		parsed, err := url.Parse(*proxyURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[-] 解析代理地址失败: %v\n", err)
			os.Exit(2)
		}
		transport.Proxy = http.ProxyURL(parsed)
	}
	client := &http.Client{Timeout: *timeout, Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] 请求失败: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] 读取响应失败: %v\n", err)
		os.Exit(1)
	}

	if banner := hostBanner(resp.Header); banner != "" {
		fmt.Fprintf(os.Stderr, "[host] %s\n", banner)
	}
	if *printHeaders {
		fmt.Fprintf(os.Stderr, "[header] 原始响应头\n")
		for name, values := range resp.Header {
			for _, value := range values {
				fmt.Fprintf(os.Stderr, "[header] %s: %s\n", name, value)
			}
		}
	}
	fmt.Fprintf(os.Stderr, "[res] HTTP %d\n", resp.StatusCode)
	os.Stdout.Write(raw)
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		fmt.Println()
	}

	if advice := authAdvice(resp.StatusCode, raw); advice != "" {
		fmt.Fprintf(os.Stderr, "[-] %s\n", advice)
	}
	if resp.StatusCode >= 300 {
		os.Exit(1)
	}
}

// hostBanner 取宿主自报的版本头。这些头在鉴权之前写入, 所以密钥不对时也能读到, 是取版本最省事的入口。
func hostBanner(header http.Header) string {
	names := []string{"X-CPA-VERSION", "X-CPA-COMMIT", "X-CPA-BUILD-DATE", "X-CPA-SUPPORT-PLUGIN", "X-CPA-HOME-VERSION"}
	parts := make([]string, 0, len(names))
	for _, name := range names {
		if value := header.Get(name); value != "" {
			parts = append(parts, name+"="+value)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

func authAdvice(status int, body []byte) string {
	text := strings.ToLower(string(body))
	switch {
	case status == http.StatusUnauthorized && strings.Contains(text, "invalid management key"):
		return "密钥不被目标接受。换一个来源再试, 别在同一个来源上重复请求: 连续 5 次失败会按 IP 封 30 分钟。"
	case status == http.StatusUnauthorized && strings.Contains(text, "missing management key"):
		return "请求没带上密钥。检查 -key-file / -key-name / -key-cmd / -key-env 是否为空。"
	case status == http.StatusForbidden && strings.Contains(text, "ip banned"):
		return "目标已按 IP 封禁。等到提示的时间再动手, 期间不要发任何请求, 重试只会延长等待。"
	case status == http.StatusForbidden:
		return "被拒绝。检查目标宿主的 remote-management.allow-remote, 以及请求来源 IP 是否在允许范围。"
	case status == http.StatusNotFound:
		return "404。路径不存在, 或宿主的 remote-management 关闭时所有 /v0/management 路由都返回 404。"
	}
	return ""
}

func resolveKey(sandboxID, sandboxDirFlag, keyFileFlag, keyName, keyCmd, keyEnv string) (string, string, error) {
	if keyFileFlag != "" {
		return readKeyFile(keyFileFlag)
	}
	if keyName != "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", fmt.Errorf("取用户目录失败: %w", err)
		}
		return readKeyFile(filepath.Join(home, ".cache", "cpa-plugins", "keys", keyName))
	}
	if keyCmd != "" {
		out, err := exec.Command("sh", "-c", keyCmd).Output()
		if err != nil {
			return "", "", fmt.Errorf("执行取密钥命令失败: %w", err)
		}
		key := strings.TrimSpace(string(out))
		if key == "" {
			return "", "", fmt.Errorf("取密钥命令没有输出: %s", keyCmd)
		}
		return key, "命令 `" + keyCmd + "`", nil
	}
	if keyEnv != "" {
		key := strings.TrimSpace(os.Getenv(keyEnv))
		if key == "" {
			return "", "", fmt.Errorf("环境变量 %s 为空", keyEnv)
		}
		return key, "环境变量 " + keyEnv, nil
	}
	if sandboxID != "" {
		dir := sandboxDirFlag
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", "", fmt.Errorf("取用户目录失败: %w", err)
			}
			dir = filepath.Join(home, ".cache", "cpa-plugins", "sandbox", sandboxID)
		}
		return readKeyFile(filepath.Join(dir, "management-key"))
	}
	return "", "未提供密钥来源", nil
}

func readKeyFile(path string) (string, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", "", fmt.Errorf("读密钥文件失败: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(os.Stderr, "[!] 密钥文件权限过宽(%o), 建议 chmod 600: %s\n", info.Mode().Perm(), path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("读密钥文件失败: %w", err)
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		return "", "", fmt.Errorf("密钥文件为空: %s", path)
	}
	return key, "文件 " + path, nil
}
