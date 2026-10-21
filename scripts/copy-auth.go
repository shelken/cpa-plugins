package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// 把生产宿主的凭据原样搬进本地沙箱, 供真机对话验收使用。
//
//	go run scripts/copy-auth.go -from https://<prod-host> \
//	  -token-file ~/.cache/cpa-plugins/sandbox/<ids>/management-key <file.json> [<file2.json> ...]
//
// 管理面任何回显通道都会把凭据打码, 打码值上线即 401, 所以下载与上传必须在同一进程里完成:
// 凭据只在内存里流转, 不落盘不回显, 只打印文件名与字节数。生产密钥经 sec-run 隐式读取,
// 沙箱密钥读 -token-file; 生产侧只做 GET, 写操作只落沙箱。
func main() {
	from := flag.String("from", "", "生产宿主地址 (只读下载凭据)")
	to := flag.String("to", "http://127.0.0.1:18317", "沙箱宿主地址 (上传凭据)")
	tokenFile := flag.String("token-file", "", "沙箱管理密钥文件")
	flag.Parse()
	if *from == "" || *tokenFile == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "用法: go run scripts/copy-auth.go -from <prod> -token-file <sandbox-key> <file.json>...")
		os.Exit(2)
	}

	prodToken, err := exec.Command("sec-run", "printenv", "CPA_TOKEN").Output()
	if err != nil || len(bytes.TrimSpace(prodToken)) == 0 {
		fmt.Fprintln(os.Stderr, "[-] 未能经 sec-run 读取生产管理密钥, 不发请求")
		os.Exit(3)
	}
	sandboxToken, err := os.ReadFile(*tokenFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] 读取 token-file 失败: %v\n", err)
		os.Exit(3)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	for _, name := range flag.Args() {
		q := url.QueryEscape(name)
		raw, err := call(client, http.MethodGet, strings.TrimRight(*from, "/")+"/v0/management/auth-files/download?name="+q, string(prodToken), nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[-] %s: %v\n", name, err)
			os.Exit(1)
		}
		if _, err := call(client, http.MethodPost, strings.TrimRight(*to, "/")+"/v0/management/auth-files?name="+q, string(sandboxToken), raw); err != nil {
			fmt.Fprintf(os.Stderr, "[-] %s: %v\n", name, err)
			os.Exit(1)
		}
		fmt.Printf("[+] copied %s (%d bytes)\n", name, len(raw))
	}
}

// call 失败时只回显方法、去掉查询串的路径与状态码, 不带任何取值。
func call(client *http.Client, method, target, token string, body []byte) ([]byte, error) {
	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s 请求失败", method, strings.SplitN(target, "?", 2)[0])
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s %s 返回 HTTP %d", method, strings.SplitN(target, "?", 2)[0], resp.StatusCode)
	}
	return out, nil
}
