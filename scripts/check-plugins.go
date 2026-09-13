package main

// 仓库不变量检查: scripts/check-plugins.go
//
// 存在的理由: 本轮调试中反复踩到「配置与实际不一致」类问题 —— 两个插件钉了不同的
// 宿主 SDK 版本、plugin.json 声明的平台没有对应的构建运行器、产物命名与宿主期望不符。
// 这些都能在秒级离线检查里发现, 而不是等装载失败或用户装不上才暴露。
//
// 用法:
//
//	go run scripts/check-plugins.go [--strict] [--release-ready]
//
//	--strict         把 WARN 也视为失败
//	--release-ready  发布门禁: 未回填 sha256 视为失败
//
// 退出码 0 表示通过, 1 表示存在失败项。

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type report struct {
	failures []string
	warnings []string
	notes    []string
}

func (r *report) fail(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

func (r *report) warn(format string, args ...any) {
	r.warnings = append(r.warnings, fmt.Sprintf(format, args...))
}

func (r *report) note(format string, args ...any) {
	r.notes = append(r.notes, fmt.Sprintf(format, args...))
}

var (
	reSDKVersion  = regexp.MustCompile(`github\.com/router-for-me/CLIProxyAPI/v7\s+v([0-9][^\s]*)`)
	reGoDirective = regexp.MustCompile(`(?m)^go\s+([0-9][^\s]*)`)
	reSHA256      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reMatrixOS    = regexp.MustCompile(`^\s*-\s*goos:\s*"?([A-Za-z0-9_]+)"?\s*$`)
	reMatrixArch  = regexp.MustCompile(`^\s*goarch:\s*"?([A-Za-z0-9_]+)"?\s*$`)
)

func main() {
	strict := flag.Bool("strict", false, "把 WARN 也视为失败")
	releaseReady := flag.Bool("release-ready", false, "发布门禁: 未回填 sha256 视为失败")
	flag.Parse()

	r := &report{}

	pluginDirs := discoverPluginDirs(r)
	matrix := readWorkflowMatrix(r)

	registryInSync(r)

	versionSeen := map[string][]string{}
	goDirectiveSeen := map[string][]string{}

	for _, dir := range pluginDirs {
		id := filepath.Base(dir)
		manifest, err := readManifest(filepath.Join(dir, "plugin.json"))
		if err != nil {
			r.fail("%s: 读取 plugin.json 失败: %v", id, err)
			continue
		}

		checkIdentity(r, id, manifest)
		checkArtifacts(r, id, manifest, matrix, *releaseReady)
		checkDocs(r, dir, id)

		version, goDirective := readGoMod(r, dir, id)
		if version != "" {
			versionSeen[version] = append(versionSeen[version], id)
		}
		if goDirective != "" {
			goDirectiveSeen[goDirective] = append(goDirectiveSeen[goDirective], id)
		}
	}

	checkUniform(r, "宿主 SDK 版本", versionSeen)
	checkUniform(r, "go 指令版本", goDirectiveSeen)

	// 输出
	for _, note := range r.notes {
		fmt.Printf("[i] %s\n", note)
	}
	for _, warning := range r.warnings {
		fmt.Printf("[!] %s\n", warning)
	}
	for _, failure := range r.failures {
		fmt.Printf("[-] %s\n", failure)
	}

	fmt.Println()
	if len(r.failures) == 0 && (len(r.warnings) == 0 || !*strict) {
		fmt.Printf("通过: %d 个插件, %d 条警告\n", len(pluginDirs), len(r.warnings))
		return
	}
	fmt.Printf("失败: %d 条错误, %d 条警告\n", len(r.failures), len(r.warnings))
	os.Exit(1)
}

func discoverPluginDirs(r *report) []string {
	dirs, err := filepath.Glob("plugins/*")
	if err != nil {
		r.fail("扫描 plugins/ 失败: %v", err)
		return nil
	}
	kept := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		kept = append(kept, dir)
	}
	sort.Strings(kept)
	return kept
}

type manifest struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Install struct {
		Type      string `json:"type"`
		Artifacts []struct {
			GOOS   string `json:"goos"`
			GOARCH string `json:"goarch"`
			URL    string `json:"url"`
			SHA256 string `json:"sha256"`
		} `json:"artifacts"`
	} `json:"install"`
}

func readManifest(path string) (manifest, error) {
	var m manifest
	data, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	return m, nil
}

func checkIdentity(r *report, dirID string, m manifest) {
	if strings.TrimSpace(m.ID) == "" {
		r.fail("%s: plugin.json 缺少 id", dirID)
		return
	}
	if m.ID != dirID {
		r.fail("%s: plugin.json 的 id (%s) 与目录名不一致", dirID, m.ID)
	}
	if strings.TrimSpace(m.Version) == "" {
		r.fail("%s: plugin.json 缺少 version", dirID)
	}
	if m.Install.Type != "direct" {
		r.fail("%s: install.type 为 %q, 应为 direct", dirID, m.Install.Type)
	}
}

func checkArtifacts(r *report, id string, m manifest, matrix map[string]bool, releaseReady bool) {
	if len(m.Install.Artifacts) == 0 {
		r.fail("%s: install.artifacts 为空", id)
		return
	}

	seen := map[string]bool{}
	for _, item := range m.Install.Artifacts {
		key := item.GOOS + "/" + item.GOARCH
		if seen[key] {
			r.fail("%s: 重复声明平台 %s", id, key)
		}
		seen[key] = true

		if !matrix[key] {
			r.fail("%s: 声明了 %s, 但发布工作流的构建矩阵里没有该平台, 该产物永远不会存在", id, key)
		}

		expectedName := fmt.Sprintf("%s_%s_%s_%s.zip", id, m.Version, item.GOOS, item.GOARCH)
		if !strings.HasSuffix(item.URL, "/"+expectedName) {
			r.fail("%s: %s 的 url 末段应为 %s, 实际 %s", id, key, expectedName, item.URL)
		}
		tag := fmt.Sprintf("%s%%2Fv%s", id, m.Version)
		if !strings.Contains(item.URL, tag) {
			r.warn("%s: %s 的 url 未使用 %%2F 编码的标签路径 (期望包含 %s)", id, key, tag)
		}

		switch {
		case strings.TrimSpace(item.SHA256) == "":
			if releaseReady {
				r.fail("%s: %s 未回填 sha256, 宿主安装时会报 artifact checksum missing", id, key)
			} else {
				r.warn("%s: %s 未回填 sha256 (发布前必须回填)", id, key)
			}
		case !reSHA256.MatchString(strings.ToLower(item.SHA256)):
			r.fail("%s: %s 的 sha256 不是 64 位十六进制", id, key)
		}
	}
}

func checkDocs(r *report, dir string, id string) {
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		r.fail("%s: 缺少 README.md", id)
	}
}

func readGoMod(r *report, dir string, id string) (string, string) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		r.fail("%s: 缺少 go.mod", id)
		return "", ""
	}
	text := string(data)

	version := ""
	if match := reSDKVersion.FindStringSubmatch(text); len(match) > 1 {
		version = match[1]
	} else {
		r.fail("%s: go.mod 未声明 CLIProxyAPI SDK 依赖", id)
	}

	goDirective := ""
	if match := reGoDirective.FindStringSubmatch(text); len(match) > 1 {
		goDirective = match[1]
	}
	return version, goDirective
}

func checkUniform(r *report, label string, seen map[string][]string) {
	if len(seen) <= 1 {
		return
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		sort.Strings(seen[key])
		parts = append(parts, fmt.Sprintf("%s (%s)", key, strings.Join(seen[key], ", ")))
	}
	r.warn("%s在各插件间不一致: %s", label, strings.Join(parts, " | "))
}

// readWorkflowMatrix 解析发布工作流构建矩阵里的平台集合。
// 工作流是本仓自己的文件, 按行扫描即可, 不必引入 YAML 依赖。
func readWorkflowMatrix(r *report) map[string]bool {
	const workflowPath = ".github/workflows/release-plugin.yml"
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		r.fail("读取 %s 失败: %v", workflowPath, err)
		return map[string]bool{}
	}

	matrix := map[string]bool{}
	pendingOS := ""
	for _, line := range strings.Split(string(data), "\n") {
		if match := reMatrixOS.FindStringSubmatch(line); len(match) > 1 {
			pendingOS = match[1]
			continue
		}
		if match := reMatrixArch.FindStringSubmatch(line); len(match) > 1 && pendingOS != "" {
			matrix[pendingOS+"/"+match[1]] = true
			pendingOS = ""
		}
	}
	if len(matrix) == 0 {
		r.fail("%s 中未解析到任何构建平台", workflowPath)
	}
	return matrix
}

func registryInSync(r *report) {
	cmd := exec.Command("go", "run", "scripts/build-registry.go", "--check")
	output, err := cmd.CombinedOutput()
	if err != nil {
		r.fail("registry.json 与插件清单不同步: %s", strings.TrimSpace(string(output)))
	}
}
