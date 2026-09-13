package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
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
	os.Stdout.Write(raw)
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		fmt.Println()
	}
	if resp.StatusCode >= 300 {
		os.Exit(1)
	}
}
