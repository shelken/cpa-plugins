package main

// 插件本地沙箱: scripts/dev-sandbox.go
//
// 存在的理由: 验证一个插件能否被宿主正确装载, 此前需要手工做七件事 —— 编译动态库、
// 按平台放置、生成 config.yaml、填管理密钥、启动宿主、轮询日志、逐个 curl 断言。
// 本脚本把它压成一条命令, 且断言与退出码可被 CI 或人工直接复用。
//
// 用法:
//
//	go run scripts/dev-sandbox.go --plugin workbuddy [--host <二进制>] [--host-src <源码目录>]
//	                              [--port 18317] [--profile desktop] [--keep]
//
// 沙箱不需要真实凭据: 装载、注册、模型清单三条断言都是离线的。凭据相关的验证
// (扫码登录、真实对话、真实额度) 不在此脚本范围内。
//
// 宿主二进制解析顺序: --host > $CPA_HOST_BIN > 缓存 > --host-src / $CPA_HOST_SRC 现场构建。
// 缓存路径: ~/.cache/cpa-plugins/host/v<SDK版本>/cliproxyapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

var reGoModSDK = regexp.MustCompile(`github\.com/router-for-me/CLIProxyAPI/v7\s+v([0-9][^\s]*)`)

type sandbox struct {
	pluginID   string
	pluginDir  string
	sandboxDir string
	hostBinary string
	hostSource string
	port       int
	profile    string
	timeout    time.Duration
	keep       bool

	secret     string
	library    string
	configPath string
	logPath    string
	keyPath    string

	process *exec.Cmd
	logFile *os.File
}

func main() {
	s := &sandbox{}
	flag.StringVar(&s.pluginID, "plugin", "", "插件 id (plugins/ 下的目录名)")
	flag.StringVar(&s.hostBinary, "host", "", "宿主二进制路径")
	flag.StringVar(&s.hostSource, "host-src", os.Getenv("CPA_HOST_SRC"), "CLIProxyAPI 源码目录, 用于现场构建宿主")
	flag.StringVar(&s.sandboxDir, "dir", "", "沙箱目录, 默认 ~/.cache/cpa-plugins/sandbox/<id>")
	flag.StringVar(&s.profile, "profile", "", "写入插件的 identity-profile/login-profile, 默认不写")
	flag.IntVar(&s.port, "port", 18317, "宿主监听端口")
	flag.DurationVar(&s.timeout, "timeout", 30*time.Second, "等待宿主就绪与断言的总超时")
	flag.BoolVar(&s.keep, "keep", false, "结束后保留宿主机进程与沙箱目录")
	flag.Parse()

	if strings.TrimSpace(s.pluginID) == "" {
		fmt.Fprintln(os.Stderr, "[-] 必须指定 --plugin")
		flag.Usage()
		os.Exit(2)
	}
	if s.sandboxDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[-] 无法定位用户目录: %v\n", err)
			os.Exit(1)
		}
		s.sandboxDir = filepath.Join(home, ".cache", "cpa-plugins", "sandbox", s.pluginID)
	}
	s.pluginDir = filepath.Join("plugins", s.pluginID)

	if err := s.run(); err != nil {
		fmt.Fprintf(os.Stderr, "\n[-] %v\n", err)
		s.dumpLogTail()
		s.stop()
		os.Exit(1)
	}
	fmt.Printf("\n[+] 沙箱验证通过。日志: %s\n", s.logPath)
	if s.keep {
		fmt.Printf("[+] 宿主仍在运行: http://127.0.0.1:%d (管理密钥: %s)\n", s.port, s.keyPath)
		return
	}
	s.stop()
}

func (s *sandbox) run() error {
	if _, err := os.Stat(filepath.Join(s.pluginDir, "go.mod")); err != nil {
		return fmt.Errorf("找不到插件 %s 的 go.mod, 确认 id 是否正确", s.pluginID)
	}

	sdkVersion, err := s.readSDKVersion()
	if err != nil {
		return err
	}
	fmt.Printf("[*] 插件 %s, 目标宿主 SDK v%s\n", s.pluginID, sdkVersion)

	if err := s.resolveHost(sdkVersion); err != nil {
		return err
	}

	if err := os.RemoveAll(s.sandboxDir); err != nil {
		return err
	}
	if err := s.prepareLayout(); err != nil {
		return err
	}
	if err := s.buildPlugin(); err != nil {
		return err
	}
	if err := s.writeConfig(); err != nil {
		return err
	}
	if err := s.startHost(); err != nil {
		return err
	}
	return s.assert()
}

func (s *sandbox) readSDKVersion() (string, error) {
	data, err := os.ReadFile(filepath.Join(s.pluginDir, "go.mod"))
	if err != nil {
		return "", err
	}
	match := reGoModSDK.FindStringSubmatch(string(data))
	if len(match) < 2 {
		return "", fmt.Errorf("插件 go.mod 未声明 CLIProxyAPI SDK 依赖")
	}
	return match[1], nil
}

func (s *sandbox) resolveHost(sdkVersion string) error {
	if s.hostBinary != "" {
		if _, err := os.Stat(s.hostBinary); err != nil {
			return fmt.Errorf("--host 指定的二进制不存在: %s", s.hostBinary)
		}
		return s.verifyHostVersion(sdkVersion)
	}
	if env := strings.TrimSpace(os.Getenv("CPA_HOST_BIN")); env != "" {
		s.hostBinary = env
		return s.verifyHostVersion(sdkVersion)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cached := filepath.Join(home, ".cache", "cpa-plugins", "host", "v"+sdkVersion, hostBinaryName())
	if _, err := os.Stat(cached); err == nil {
		s.hostBinary = cached
		fmt.Printf("[*] 使用缓存的宿主二进制: %s\n", cached)
		return s.verifyHostVersion(sdkVersion)
	}

	if s.hostSource == "" {
		return fmt.Errorf(`未找到可用的宿主二进制。请任选其一:
      --host /path/to/cliproxyapi          直接指定二进制
      --host-src /path/to/CLIProxyAPI      现场构建 (该目录应已 checkout 到 v%s)
      环境变量 CPA_HOST_BIN 或 CPA_HOST_SRC
      或把二进制预置到 %s`, sdkVersion, cached)
	}

	sourceVersion, err := gitDescribe(s.hostSource)
	if err != nil {
		return err
	}
	if sourceVersion != "v"+sdkVersion {
		return fmt.Errorf(`宿主源码版本与插件目标 SDK 不一致, 这样装载成功也不能证明兼容:
      插件 go.mod 要求: v%s
      源码当前版本:     %s
      请先在该目录执行: git checkout v%s`, sdkVersion, sourceVersion, sdkVersion)
	}

	if err := os.MkdirAll(filepath.Dir(cached), 0o755); err != nil {
		return err
	}
	fmt.Printf("[*] 从 %s 构建宿主 (v%s) 到缓存...\n", s.hostSource, sdkVersion)
	build := exec.Command("go", "build", "-o", cached, "./cmd/server")
	build.Dir = s.hostSource
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("构建宿主失败: %w", err)
	}
	s.hostBinary = cached
	return nil
}

// verifyHostVersion 只能核对源码版本, 二进制无法自省; 仅当显式传入 --host-src 时才校验。
func (s *sandbox) verifyHostVersion(sdkVersion string) error {
	if s.hostSource == "" {
		return nil
	}
	sourceVersion, err := gitDescribe(s.hostSource)
	if err != nil {
		return err
	}
	if sourceVersion != "v"+sdkVersion {
		return fmt.Errorf("宿主源码版本 %s 与插件目标 SDK v%s 不一致", sourceVersion, sdkVersion)
	}
	return nil
}

func (s *sandbox) prepareLayout() error {
	extension := platformExtension(runtime.GOOS)
	s.library = filepath.Join(s.sandboxDir, "plugins", runtime.GOOS, runtime.GOARCH, s.pluginID+extension)
	s.configPath = filepath.Join(s.sandboxDir, "config.yaml")
	s.logPath = filepath.Join(s.sandboxDir, "host.log")
	s.keyPath = filepath.Join(s.sandboxDir, "management-key")

	if err := os.MkdirAll(filepath.Dir(s.library), 0o755); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(s.sandboxDir, "auth"), 0o755)
}

func (s *sandbox) buildPlugin() error {
	fmt.Printf("[*] 构建插件动态库 -> %s\n", s.library)
	build := exec.Command("go", "build", "-buildmode=c-shared", "-o", s.library, ".")
	build.Dir = s.pluginDir
	build.Env = append(os.Environ(), "CGO_ENABLED=1")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("构建插件失败: %w", err)
	}
	// c-shared 会顺带生成同名头文件, 留在插件目录里属于构建垃圾
	header := strings.TrimSuffix(s.library, filepath.Ext(s.library)) + ".h"
	_ = os.Remove(header)
	return nil
}

func (s *sandbox) writeConfig() error {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return err
	}
	s.secret = hex.EncodeToString(buffer)

	var builder strings.Builder
	fmt.Fprintf(&builder, "port: %d\n", s.port)
	fmt.Fprintf(&builder, "auth-dir: %q\n", filepath.Join(s.sandboxDir, "auth"))
	builder.WriteString("remote-management:\n")
	fmt.Fprintf(&builder, "  secret-key: %q\n", s.secret)
	builder.WriteString("plugins:\n")
	builder.WriteString("  enabled: true\n")
	fmt.Fprintf(&builder, "  dir: %q\n", filepath.Join(s.sandboxDir, "plugins"))
	builder.WriteString("  configs:\n")
	fmt.Fprintf(&builder, "    %s:\n", s.pluginID)
	builder.WriteString("      enabled: true\n")
	if s.profile != "" {
		fmt.Fprintf(&builder, "      identity-profile: %q\n", s.profile)
		fmt.Fprintf(&builder, "      login-profile: %q\n", s.profile)
	}

	if err := os.WriteFile(s.configPath, []byte(builder.String()), 0o644); err != nil {
		return err
	}
	// 宿主装载后会把明文密钥哈希再写回配置, 明文随即消失; 要驱动管理面只能从这里读。
	if err := os.WriteFile(s.keyPath, []byte(s.secret), 0o600); err != nil {
		return err
	}
	fmt.Printf("[*] 写入沙箱配置: %s\n", s.configPath)
	return nil
}

func (s *sandbox) startHost() error {
	logFile, err := os.Create(s.logPath)
	if err != nil {
		return err
	}
	s.logFile = logFile

	command := exec.Command(s.hostBinary, "-config", s.configPath)
	command.Dir = s.sandboxDir
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		return fmt.Errorf("启动宿主失败: %w", err)
	}
	s.process = command
	fmt.Printf("[*] 启动宿主 pid=%d, 等待就绪...\n", command.Process.Pid)

	deadline := time.Now().Add(s.timeout)
	for time.Now().Before(deadline) {
		log := s.readLog()
		if strings.Contains(log, "API server started successfully") {
			return nil
		}
		if command.ProcessState != nil && command.ProcessState.Exited() {
			return fmt.Errorf("宿主在就绪前退出, 退出码 %d", command.ProcessState.ExitCode())
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("等待宿主就绪超时 (%s)", s.timeout)
}

func (s *sandbox) assert() error {
	log := s.readLog()

	if !strings.Contains(log, "plugin loaded") {
		return fmt.Errorf("宿主日志中没有出现 plugin loaded, 插件未被装载")
	}
	if !strings.Contains(log, "plugin registered") {
		return fmt.Errorf("宿主日志中出现了 plugin loaded 但没有 plugin registered, 插件注册失败")
	}
	fmt.Println("[+] 断言通过: 插件已装载并注册")

	if strings.Contains(log, "pluginhost: model registrar") && strings.Contains(log, "context deadline exceeded") {
		fmt.Println("[!] 警告: 日志中出现 model registrar 超时, 模型可能未完成注册")
	}

	if err := s.assertModels(); err != nil {
		return err
	}
	s.reportQuotaProvider()
	return nil
}

func (s *sandbox) assertModels() error {
	body, status, err := s.httpGet("/v1/models", false)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("GET /v1/models 返回 %d: %s", status, truncate(body, 200))
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return fmt.Errorf("解析 /v1/models 响应失败: %w", err)
	}

	served := map[string]bool{}
	for _, item := range payload.Data {
		served[item.ID] = true
	}

	declared, err := s.declaredModels()
	if err != nil {
		return err
	}

	// 无静态清单的插件可能不提供模型 (例如只做用量或管理的插件), 因此空列表不算失败;
	// 有静态清单时, 声明的模型必须全部在列。
	if len(declared) == 0 {
		fmt.Printf("[+] 断言通过: /v1/models 返回 %d 个模型, 插件无静态清单, 不比对\n", len(served))
		return nil
	}

	var missing []string
	for _, id := range declared {
		if !served[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("静态清单声明了 %d 个模型, 但 /v1/models 少了 %d 个: %s",
			len(declared), len(missing), strings.Join(missing, ", "))
	}
	fmt.Printf("[+] 断言通过: /v1/models 返回 %d 个模型, 清单声明的 %d 个全部在列\n",
		len(served), len(declared))
	return nil
}

// declaredModels 读取插件内嵌静态清单里声明的模型 id。
// 插件可能按自身规则做过滤, 因此断言方向是「声明的必须都在」而不是「完全相等」。
func (s *sandbox) declaredModels() ([]string, error) {
	path := filepath.Join(s.pluginDir, "data", "static-config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var manifest struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}

	ids := make([]string, 0, len(manifest.Models))
	for _, item := range manifest.Models {
		for _, key := range []string{"id", "ID"} {
			if value, ok := item[key].(string); ok && value != "" {
				ids = append(ids, value)
				break
			}
		}
	}
	return ids, nil
}

func (s *sandbox) reportQuotaProvider() {
	body, status, err := s.httpGet("/v0/management/quota/providers", true)
	if err != nil || status != http.StatusOK {
		return
	}
	if strings.Contains(body, `"`+s.pluginID+`"`) {
		fmt.Println("[+] 断言通过: 额度提供方列表包含本插件")
		return
	}
	fmt.Println("[i] 额度提供方列表不含本插件 (未实现 QuotaProvider 时属正常)")
}

func (s *sandbox) httpGet(path string, management bool) (string, int, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d%s", s.port, path)
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	if management {
		request.Header.Set("Authorization", "Bearer "+s.secret)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return "", 0, fmt.Errorf("请求 %s 失败: %w", path, err)
	}
	defer response.Body.Close()

	data, err := io.ReadAll(response.Body)
	if err != nil {
		return "", response.StatusCode, err
	}
	return string(data), response.StatusCode, nil
}

func (s *sandbox) readLog() string {
	if s.logPath == "" {
		return ""
	}
	data, err := os.ReadFile(s.logPath)
	if err != nil {
		return ""
	}
	return string(data)
}

func (s *sandbox) dumpLogTail() {
	log := s.readLog()
	if log == "" {
		return
	}
	lines := strings.Split(strings.TrimRight(log, "\n"), "\n")
	if len(lines) > 25 {
		lines = lines[len(lines)-25:]
	}
	fmt.Fprintf(os.Stderr, "\n--- 宿主日志尾部 (%s) ---\n%s\n", s.logPath, strings.Join(lines, "\n"))
}

func (s *sandbox) stop() {
	if s.process == nil || s.process.Process == nil {
		return
	}
	_ = s.process.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() {
		_, _ = s.process.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = s.process.Process.Kill()
	}
	if s.logFile != nil {
		_ = s.logFile.Close()
	}
	s.process = nil
}

// platformExtension 与宿主 internal/pluginstore 的 pluginExtension 保持一致
func platformExtension(goos string) string {
	switch strings.ToLower(strings.TrimSpace(goos)) {
	case "darwin", "mac", "macos", "osx":
		return ".dylib"
	case "windows":
		return ".dll"
	default:
		return ".so"
	}
}

func hostBinaryName() string {
	if runtime.GOOS == "windows" {
		return "cliproxyapi.exe"
	}
	return "cliproxyapi"
}

func gitDescribe(dir string) (string, error) {
	command := exec.Command("git", "-C", dir, "describe", "--tags")
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("读取 %s 的 git 版本失败: %w", dir, err)
	}
	return strings.TrimSpace(string(output)), nil
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
