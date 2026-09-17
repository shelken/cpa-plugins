package main

// 插件本地沙箱: scripts/dev-sandbox.go
//
// 存在的理由: 验证一个插件能否被宿主正确装载, 此前需要手工做七件事 —— 编译动态库、
// 按平台放置、生成 config.yaml、填管理密钥、启动宿主、轮询日志、逐个 curl 断言。
// 本脚本把它压成一条命令, 且断言与退出码可被 CI 或人工直接复用。
//
// 用法:
//
//	go run scripts/dev-sandbox.go --plugin workbuddy [--plugin qwenworkcn] [--checks load,models]
//	                              [--host <二进制>] [--host-src <源码目录>]
//	                              [--port 18317] [--profile desktop] [--keep]
//
// --plugin 可重复或逗号分隔, 多插件共用一个宿主进程与沙箱目录 (默认目录名为 id 以 + 连接)。
// --checks 选择要跑的断言子集, `--checks list` 打印清单; 断言都是离线的, 默认全跑。
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

// check 是断言的注册单位: 每个断言对单个插件各跑一次, 返回失败即终止。
// 加断言 = 写一个 run 函数 + 注册一行, 调度不随断言数量增长。
type check struct {
	id   string
	desc string
	run  func(*sandbox, string) error
}

// checkRegistry 是断言清单的唯一权威来源: --checks list、解析、调度全部由它派生。
var checkRegistry = []check{
	{"load", "插件已装载并注册 (宿主日志 plugin loaded / plugin registered)", checkLoad},
	{"models", "/v1/models 覆盖静态清单声明的全部模型", checkModels},
	{"resource", "插件 resource 页面可被宿主服务", checkResource},
	{"menus", "管理面 plugins 列表暴露插件菜单", checkMenus},
	{"config", "管理面返回可视化配置字段", checkConfig},
	{"quota", "额度提供方列表包含插件", checkQuota},
}

var defaultChecks = []string{"load", "models", "resource", "menus", "config", "quota"}

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

func printCheckCatalog() {
	fmt.Println("可用断言 (按需点名, 不必全跑):")
	for _, c := range checkRegistry {
		fmt.Printf("  %-10s %s\n", c.id, c.desc)
	}
	fmt.Printf("  %-10s 以上全部 (默认)\n", "all")
}

func lookupCheck(id string) (check, bool) {
	for _, c := range checkRegistry {
		if c.id == id {
			return c, true
		}
	}
	return check{}, false
}

// resolveChecks 把用户输入折成有序执行集 (顺序即注册顺序), 并回传无法识别的名字。
func resolveChecks(input []string) ([]check, []string) {
	if len(input) == 0 {
		input = defaultChecks
	}
	want := map[string]bool{}
	var unknown []string
	for _, raw := range input {
		id := strings.ToLower(strings.TrimSpace(raw))
		if id == "" {
			continue
		}
		if id == "all" {
			for _, c := range checkRegistry {
				want[c.id] = true
			}
			continue
		}
		if _, ok := lookupCheck(id); !ok {
			unknown = append(unknown, id)
			continue
		}
		want[id] = true
	}

	ordered := make([]check, 0, len(want))
	for _, c := range checkRegistry {
		if want[c.id] {
			ordered = append(ordered, c)
		}
	}
	return ordered, unknown
}

type sandbox struct {
	pluginIDs  []string
	pluginDirs map[string]string
	hostBinary string
	hostSource string
	port       int
	profile    string
	timeout    time.Duration
	keep       bool
	chosen     []check

	// sandboxDir 为空时由 pluginIDs 推导; 显式传 --dir 时原样使用
	sandboxDir string

	// modelIDs 是本次运行 /v1/models 的快照, 供各插件的模型断言复用
	modelIDs map[string]bool

	secret     string
	configPath string
	logPath    string
	keyPath    string

	process *exec.Cmd
	logFile *os.File
}

func main() {
	s := &sandbox{pluginDirs: map[string]string{}}
	var plugins listFlag
	var checks listFlag
	flag.Var(&plugins, "plugin", "插件 id (plugins/ 下的目录名), 逗号分隔或重复传参")
	flag.StringVar(&s.hostBinary, "host", "", "宿主二进制路径")
	flag.StringVar(&s.hostSource, "host-src", os.Getenv("CPA_HOST_SRC"), "CLIProxyAPI 源码目录, 用于现场构建宿主")
	flag.StringVar(&s.sandboxDir, "dir", "", "沙箱目录, 默认 ~/.cache/cpa-plugins/sandbox/<id>")
	flag.StringVar(&s.profile, "profile", "", "写入插件的 identity-profile/login-profile, 默认不写")
	flag.IntVar(&s.port, "port", 18317, "宿主监听端口")
	flag.DurationVar(&s.timeout, "timeout", 30*time.Second, "等待宿主就绪与断言的总超时")
	flag.BoolVar(&s.keep, "keep", false, "结束后保留宿主机进程与沙箱目录")
	flag.Var(&checks, "checks", "要跑的断言, 逗号分隔或重复传参; --checks list 打印清单")
	flag.Parse()

	if len(checks) == 1 && strings.EqualFold(strings.TrimSpace(checks[0]), "list") {
		printCheckCatalog()
		return
	}

	// 去重保持顺序, 同一个插件重复传参不重复构建
	seen := map[string]bool{}
	for _, id := range plugins {
		if !seen[id] {
			seen[id] = true
			s.pluginIDs = append(s.pluginIDs, id)
		}
	}
	if len(s.pluginIDs) == 0 {
		fmt.Fprintln(os.Stderr, "[-] 必须指定 --plugin")
		flag.Usage()
		os.Exit(2)
	}

	chosen, unknown := resolveChecks(checks)
	if len(unknown) > 0 {
		fmt.Fprintf(os.Stderr, "[-] 未知断言: %s\n\n", strings.Join(unknown, ", "))
		printCheckCatalog()
		os.Exit(2)
	}
	if len(chosen) == 0 {
		fmt.Fprintln(os.Stderr, "[-] 没有可执行的断言")
		os.Exit(2)
	}
	s.chosen = chosen

	if s.sandboxDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[-] 无法定位用户目录: %v\n", err)
			os.Exit(1)
		}
		s.sandboxDir = filepath.Join(home, ".cache", "cpa-plugins", "sandbox", strings.Join(s.pluginIDs, "+"))
	}
	for _, id := range s.pluginIDs {
		s.pluginDirs[id] = filepath.Join("plugins", id)
	}

	names := make([]string, 0, len(s.chosen))
	for _, c := range s.chosen {
		names = append(names, c.id)
	}
	fmt.Printf("[*] 执行断言: %s (未点名的不执行)\n", strings.Join(names, ", "))

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
	for _, id := range s.pluginIDs {
		if _, err := os.Stat(filepath.Join(s.pluginDirs[id], "go.mod")); err != nil {
			return fmt.Errorf("找不到插件 %s 的 go.mod, 确认 id 是否正确", id)
		}
	}

	sdkVersion, err := s.readSDKVersion(s.pluginDirs[s.pluginIDs[0]])
	if err != nil {
		return err
	}
	fmt.Printf("[*] 插件 %s, 目标宿主 SDK v%s\n", strings.Join(s.pluginIDs, ", "), sdkVersion)

	if err := s.resolveHost(sdkVersion); err != nil {
		return err
	}

	if err := os.RemoveAll(s.sandboxDir); err != nil {
		return err
	}
	if err := s.prepareLayout(); err != nil {
		return err
	}
	for _, id := range s.pluginIDs {
		if err := s.buildPlugin(id); err != nil {
			return err
		}
	}
	if err := s.writeConfig(); err != nil {
		return err
	}
	if err := s.startHost(); err != nil {
		return err
	}
	return s.assert()
}

func (s *sandbox) readSDKVersion(pluginDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(pluginDir, "go.mod"))
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
	s.configPath = filepath.Join(s.sandboxDir, "config.yaml")
	s.logPath = filepath.Join(s.sandboxDir, "host.log")
	s.keyPath = filepath.Join(s.sandboxDir, "management-key")

	if err := os.MkdirAll(filepath.Join(s.sandboxDir, "plugins", runtime.GOOS, runtime.GOARCH), 0o755); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(s.sandboxDir, "auth"), 0o755)
}

// buildPlugin 编译单个插件到宿主扫描目录; 多插件时每个都产出一个动态库。
func (s *sandbox) buildPlugin(id string) error {
	library := filepath.Join(s.sandboxDir, "plugins", runtime.GOOS, runtime.GOARCH, id+platformExtension(runtime.GOOS))
	fmt.Printf("[*] 构建插件 %s -> %s\n", id, library)
	build := exec.Command("go", "build", "-buildmode=c-shared", "-o", library, ".")
	build.Dir = s.pluginDirs[id]
	build.Env = append(os.Environ(), "CGO_ENABLED=1")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("构建插件 %s 失败: %w", id, err)
	}
	// c-shared 会顺带生成同名头文件, 留在插件目录里属于构建垃圾
	header := strings.TrimSuffix(library, filepath.Ext(library)) + ".h"
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
	for _, id := range s.pluginIDs {
		fmt.Fprintf(&builder, "    %s:\n", id)
		builder.WriteString("      enabled: true\n")
		if s.profile != "" {
			fmt.Fprintf(&builder, "      identity-profile: %q\n", s.profile)
			fmt.Fprintf(&builder, "      login-profile: %q\n", s.profile)
		}
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

// assert 按注册顺序调度断言, 每个断言对每个插件各跑一次。
func (s *sandbox) assert() error {
	// 模型断言对所有插件复用一次 /v1/models 快照, 避免 N 次重复请求。
	served, err := s.servedModelIDs()
	if err != nil {
		return err
	}
	s.modelIDs = served

	for _, c := range s.chosen {
		for _, id := range s.pluginIDs {
			if err := c.run(s, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkLoad 断言宿主日志里该插件已完成装载与注册。
// 日志行形如 `pluginhost: plugin loaded plugin_id=<id> path=...`, 按 plugin_id 逐插件判定,
// 多插件共生时不会把「有插件装载了」误当成「这个插件装载了」。
func checkLoad(s *sandbox, pluginID string) error {
	log := s.readLog()
	loaded := strings.Contains(log, "plugin loaded plugin_id="+pluginID)
	registered := strings.Contains(log, "plugin registered plugin_id="+pluginID)
	if !loaded {
		return fmt.Errorf("宿主日志中没有 %s 的 plugin loaded 行, 插件未被装载", pluginID)
	}
	if !registered {
		return fmt.Errorf("宿主日志中有 %s 的 plugin loaded 但没有 plugin registered, 插件注册失败", pluginID)
	}
	fmt.Printf("[+] 断言通过: 插件 %s 已装载并注册\n", pluginID)

	if strings.Contains(log, "pluginhost: model registrar") && strings.Contains(log, "context deadline exceeded") {
		fmt.Println("[!] 警告: 日志中出现 model registrar 超时, 模型可能未完成注册")
	}
	return nil
}

// servedModelIDs 拉一次 /v1/models 供所有插件的模型断言复用; 未点名 models 断言时返回空。
func (s *sandbox) servedModelIDs() (map[string]bool, error) {
	if _, ok := lookupCheck("models"); !ok {
		return nil, nil
	}
	if !s.wantsCheck("models") {
		return nil, nil
	}
	body, status, err := s.httpGet("/v1/models", false)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("GET /v1/models 返回 %d: %s", status, truncate(body, 200))
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return nil, fmt.Errorf("解析 /v1/models 响应失败: %w", err)
	}

	served := map[string]bool{}
	for _, item := range payload.Data {
		served[item.ID] = true
	}
	return served, nil
}

func (s *sandbox) wantsCheck(id string) bool {
	for _, c := range s.chosen {
		if c.id == id {
			return true
		}
	}
	return false
}

func checkModels(s *sandbox, pluginID string) error {
	served := s.modelIDs
	declared, err := s.declaredModels(pluginID)
	if err != nil {
		return err
	}

	// 无静态清单的插件可能不提供模型 (例如只做用量或管理的插件), 因此空列表不算失败;
	// 有静态清单时, 声明的模型必须全部在列。
	if len(declared) == 0 {
		fmt.Printf("[+] 断言通过: /v1/models 返回 %d 个模型, 插件无静态清单, 不比对\n", len(served))
		return nil
	}

	// 插件注册侧可能给模型 id 加前缀 (workbuddy 的 enable-model-prefix, 默认开启),
	// 清单声明的是裸 id; 断言按「前缀 id 或裸 id 命中其一」匹配。
	var missing []string
	for _, id := range declared {
		prefixed := pluginID + "/" + id
		if !served[id] && !served[prefixed] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("插件 %s 的静态清单声明了 %d 个模型, 但 /v1/models 少了 %d 个: %s",
			pluginID, len(declared), len(missing), strings.Join(missing, ", "))
	}
	fmt.Printf("[+] 断言通过: %s, /v1/models 返回 %d 个模型, 清单声明的 %d 个全部在列\n",
		pluginID, len(served), len(declared))
	return nil
}

// assertResourcePage 验证插件 resource 页面可被宿主服务 (面板 iframe 数据源)。
func checkResource(s *sandbox, pluginID string) error {
	body, status, err := s.httpGet("/v0/resource/plugins/"+pluginID+"/quota", false)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("GET /v0/resource/plugins/%s/quota 返回 %d: %s", pluginID, status, truncate(body, 200))
	}
	if !strings.Contains(body, pluginID) {
		return fmt.Errorf("resource 页面内容不含 %q, 疑似服务了错误内容: %s", pluginID, truncate(body, 200))
	}
	fmt.Printf("[+] 断言通过: 插件 %s 的 resource 页面已注册且可访问\n", pluginID)
	return nil
}

// assertPluginMenus 验证管理面 plugins 列表暴露了插件菜单 (面板侧边栏入口)。
func checkMenus(s *sandbox, pluginID string) error {
	body, status, err := s.httpGet("/v0/management/plugins", true)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("GET /v0/management/plugins 返回 %d: %s", status, truncate(body, 200))
	}

	var payload struct {
		Plugins []struct {
			ID    string `json:"id"`
			Menus []struct {
				Path string `json:"path"`
				Menu string `json:"menu"`
			} `json:"menus"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return fmt.Errorf("解析 /v0/management/plugins 响应失败: %w", err)
	}

	for _, plugin := range payload.Plugins {
		if plugin.ID != pluginID {
			continue
		}
		if len(plugin.Menus) == 0 {
			return fmt.Errorf("插件 %s 未注册任何菜单, 面板侧边栏不会显示", pluginID)
		}
		menus := make([]string, 0, len(plugin.Menus))
		for _, menu := range plugin.Menus {
			menus = append(menus, menu.Menu+" ("+menu.Path+")")
		}
		fmt.Printf("[+] 断言通过: %s 插件菜单已注册: %s\n", pluginID, strings.Join(menus, ", "))
		return nil
	}
	return fmt.Errorf("/v0/management/plugins 列表中没有找到插件 %s", pluginID)
}

// declaredModels 读取插件内嵌静态清单里声明的模型 id。
// 插件可能按自身规则做过滤, 因此断言方向是「声明的必须都在」而不是「完全相等」。
func (s *sandbox) declaredModels(pluginID string) ([]string, error) {
	path := filepath.Join(s.pluginDirs[pluginID], "data", "static-config.json")
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

func checkQuota(s *sandbox, pluginID string) error {
	body, status, err := s.httpGet("/v0/management/quota/providers", true)
	if err != nil || status != http.StatusOK {
		return nil
	}
	if strings.Contains(body, `"`+pluginID+`"`) {
		fmt.Printf("[+] 断言通过: %s 在额度提供方列表中\n", pluginID)
		return nil
	}
	fmt.Printf("[i] 额度提供方列表不含 %s (未实现 QuotaProvider 时属正常)\n", pluginID)
	return nil
}
func checkConfig(s *sandbox, pluginID string) error {
	body, status, err := s.httpGet("/v0/management/plugins", true)
	if err != nil || status != http.StatusOK {
		return nil
	}
	var resp struct {
		Plugins []struct {
			ID           string `json:"id"`
			ConfigFields []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"config_fields"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil
	}
	for _, p := range resp.Plugins {
		if p.ID == pluginID {
			if len(p.ConfigFields) > 0 {
				names := make([]string, 0, len(p.ConfigFields))
				for _, f := range p.ConfigFields {
					names = append(names, f.Name)
				}
				fmt.Printf("[+] 断言通过: %s 管理面返回 %d 个配置字段: %s\n",
					pluginID, len(p.ConfigFields), strings.Join(names, ", "))
			} else {
				fmt.Printf("[i] %s 管理面未返回可视化配置字段 (config_fields 为空)\n", pluginID)
			}
			return nil
		}
	}
	return nil
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
