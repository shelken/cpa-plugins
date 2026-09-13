package main

// 插件发布工具: 本地打包 + 契约自检 + 哈希回填 + 重建 registry.json
//
// 存在的理由: 宿主 internal/pluginstore 对产物命名有硬性要求, 不满足则安装失败,
// 而 CI 只有推到标签后才会执行一次。本脚本把同一套约束前移到本地, 让发布风险在
// 打包阶段暴露, 而不是等用户装不上才发现。
//
// 用法:
//
//	go run scripts/release.go pack   --plugin <id> [--version <v>] [--out dist]
//	go run scripts/release.go record --plugin <id> [--dist dist]
//
// pack   构建当前平台动态库, 打成宿主契约命名的 zip, 打印 sha256 与 size。
// record 读取 dist 下已有 zip, 把 sha256 回填进 plugin.json, 再重建 registry.json。
//
// 推荐发布流程。哈希必须来自**真实上传的产物**, 否则安装时报 checksum mismatch:
//
//	go run scripts/check-plugins.go --release-ready          # 发布前门禁
//	git tag workbuddy/v0.1.0 && git push origin workbuddy/v0.1.0
//	gh release download workbuddy/v0.1.0 --pattern '*.zip' --dir dist
//	go run scripts/release.go record --plugin workbuddy --dist dist
//	git add plugins/workbuddy/plugin.json registry.json && git commit
//
// pack 用于本地预检包结构与命名是否满足宿主契约; 其产物只有在确实被上传发布时,
// 才可以据其回填哈希。
//
// 说明: 只支持为当前平台打包。c-shared 是 CGO 构建, 交叉编译需要目标平台的 C 工具链,
// 因此 CI 用各平台原生运行器分别构建, 本地同理只构建本机平台。

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// ---------------- 宿主契约 ----------------

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

// archiveName 与宿主 internal/pluginstore 的 ArchiveName 保持一致
func archiveName(id, version, goos, goarch string) string {
	return fmt.Sprintf("%s_%s_%s_%s.zip", id, version, goos, goarch)
}

// ---------------- plugin.json ----------------

type artifact struct {
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

func readPluginManifest(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	var manifest map[string]any
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	return manifest, nil
}

func manifestArtifacts(manifest map[string]any) ([]artifact, error) {
	install, ok := manifest["install"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("plugin.json 缺少 install 段")
	}
	rawArtifacts, ok := install["artifacts"].([]any)
	if !ok || len(rawArtifacts) == 0 {
		return nil, fmt.Errorf("plugin.json 的 install.artifacts 为空")
	}
	artifacts := make([]artifact, 0, len(rawArtifacts))
	for _, raw := range rawArtifacts {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("artifacts 条目不是对象")
		}
		item := artifact{}
		item.GOOS, _ = entry["goos"].(string)
		item.GOARCH, _ = entry["goarch"].(string)
		item.URL, _ = entry["url"].(string)
		item.SHA256, _ = entry["sha256"].(string)
		artifacts = append(artifacts, item)
	}
	return artifacts, nil
}

func manifestString(manifest map[string]any, key string) string {
	value, _ := manifest[key].(string)
	return value
}

// ---------------- 任务: pack ----------------

func runPack(args []string) error {
	fs := flag.NewFlagSet("pack", flag.ExitOnError)
	pluginID := fs.String("plugin", "", "插件 id (plugins/ 下的目录名)")
	version := fs.String("version", "", "覆盖版本号, 默认取 plugin.json")
	outDir := fs.String("out", "dist", "产物输出目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*pluginID) == "" {
		fs.Usage()
		return fmt.Errorf("必须指定 --plugin")
	}

	pluginDir := filepath.Join("plugins", *pluginID)
	manifestPath := filepath.Join(pluginDir, "plugin.json")
	manifest, err := readPluginManifest(manifestPath)
	if err != nil {
		return err
	}
	resolvedVersion := strings.TrimSpace(*version)
	if resolvedVersion == "" {
		resolvedVersion = manifestString(manifest, "version")
	}
	if resolvedVersion == "" {
		return fmt.Errorf("无法确定版本号, 请用 --version 指定")
	}

	goos, goarch := runtime.GOOS, runtime.GOARCH
	extension := platformExtension(goos)

	// 1. 构建动态库到临时目录, 文件名就是宿主期望的 <id><扩展名>
	buildDir, err := os.MkdirTemp("", "cpa-pack-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(buildDir)

	libraryPath := filepath.Join(buildDir, *pluginID+extension)
	buildCmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", libraryPath, ".")
	buildCmd.Dir = pluginDir
	buildCmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	buildCmd.Stdout, buildCmd.Stderr = os.Stdout, os.Stderr
	fmt.Printf("[*] 构建 %s/%s 动态库...\n", goos, goarch)
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("构建失败: %w", err)
	}
	if _, err := os.Stat(libraryPath); err != nil {
		return fmt.Errorf("构建未产出 %s", libraryPath)
	}

	// 2. 打包: 包内只放一个根级动态库
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	assetName := archiveName(*pluginID, resolvedVersion, goos, goarch)
	assetPath := filepath.Join(*outDir, assetName)

	if err := writeZip(assetPath, libraryPath, *pluginID+extension); err != nil {
		return err
	}

	// 3. 读回自检, 用宿主的规则校验
	if err := verifyArchive(assetPath, *pluginID, resolvedVersion, goos); err != nil {
		return fmt.Errorf("产物自检失败: %w", err)
	}

	digest, size, err := fileDigest(assetPath)
	if err != nil {
		return err
	}
	fmt.Printf("[+] 产物: %s\n", assetPath)
	fmt.Printf("[+] 包内条目: %s%s\n", *pluginID, extension)
	fmt.Printf("[+] 平台: %s/%s\n", goos, goarch)
	fmt.Printf("[+] 大小: %d 字节\n", size)
	fmt.Printf("[+] sha256: %s\n", digest)
	fmt.Printf("\n[!] 提示: 其余平台的产物必须在对应平台上分别执行 pack, 无法交叉产出。\n")
	return nil
}

func writeZip(assetPath, libraryPath, entryName string) error {
	source, err := os.Open(libraryPath)
	if err != nil {
		return err
	}
	defer source.Close()

	info, err := source.Stat()
	if err != nil {
		return err
	}

	target, err := os.Create(assetPath)
	if err != nil {
		return err
	}
	defer target.Close()

	writer := zip.NewWriter(target)
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = entryName
	header.Method = zip.Deflate
	header.SetMode(0o755)

	entry, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	if _, err := io.Copy(entry, source); err != nil {
		return err
	}
	return writer.Close()
}

// verifyArchive 按 host/internal/pluginstore 的 readTargetLibrary 规则校验
func verifyArchive(assetPath, pluginID, version, goos string) error {
	extension := platformExtension(goos)
	wanted := map[string]bool{
		pluginID + extension:                  true,
		pluginID + "-v" + version + extension: true,
	}

	reader, err := zip.OpenReader(assetPath)
	if err != nil {
		return err
	}
	defer reader.Close()

	var found string
	for _, file := range reader.File {
		name := strings.TrimSpace(file.Name)
		if strings.Contains(name, `\`) {
			return fmt.Errorf("条目 %s 使用了反斜杠分隔符", name)
		}
		if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") {
			return fmt.Errorf("条目 %s 逃逸出压缩包根目录", name)
		}
		if file.FileInfo().IsDir() {
			continue
		}
		if !wanted[name] {
			if filepath.Base(name) == pluginID+extension {
				return fmt.Errorf("动态库必须在压缩包根级, 实际为 %s", name)
			}
			return fmt.Errorf("动态库命名必须是 %s%s 或 %s-v%s%s, 实际为 %s",
				pluginID, extension, pluginID, version, extension, name)
		}
		if found != "" {
			return fmt.Errorf("压缩包含多个目标动态库: %s 与 %s", found, name)
		}
		found = name
	}
	if found == "" {
		return fmt.Errorf("压缩包内没有 %s%s", pluginID, extension)
	}
	return nil
}

// ---------------- 任务: record ----------------

func runRecord(args []string) error {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	pluginID := fs.String("plugin", "", "插件 id")
	distDir := fs.String("dist", "dist", "产物目录")
	skipRegistry := fs.Bool("skip-registry", false, "只回填 plugin.json, 不重建 registry.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*pluginID) == "" {
		fs.Usage()
		return fmt.Errorf("必须指定 --plugin")
	}

	manifestPath := filepath.Join("plugins", *pluginID, "plugin.json")
	manifest, err := readPluginManifest(manifestPath)
	if err != nil {
		return err
	}
	version := manifestString(manifest, "version")
	if version == "" {
		return fmt.Errorf("plugin.json 缺少 version")
	}
	declared, err := manifestArtifacts(manifest)
	if err != nil {
		return err
	}
	seenPlatform := map[string]bool{}
	for _, item := range declared {
		key := item.GOOS + "/" + item.GOARCH
		if seenPlatform[key] {
			return fmt.Errorf("plugin.json 重复声明平台 %s", key)
		}
		seenPlatform[key] = true
	}

	// 仅回填 dist 中实际存在的产物, 缺哪个平台就保留原值并显式报告, 不猜测。
	install := manifest["install"].(map[string]any)
	rawArtifacts := install["artifacts"].([]any)

	filled, missing := 0, []string{}
	for _, raw := range rawArtifacts {
		entry, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("artifacts 条目不是对象")
		}
		goos, _ := entry["goos"].(string)
		goarch, _ := entry["goarch"].(string)
		assetPath := filepath.Join(*distDir, archiveName(*pluginID, version, goos, goarch))
		if _, err := os.Stat(assetPath); err != nil {
			missing = append(missing, fmt.Sprintf("%s/%s", goos, goarch))
			continue
		}
		digest, size, err := fileDigest(assetPath)
		if err != nil {
			return err
		}
		entry["sha256"] = digest
		entry["size"] = json.Number(fmt.Sprintf("%d", size))
		filled++
		fmt.Printf("[+] 回填 %s/%s: %s\n", goos, goarch, digest)
	}

	if filled == 0 {
		return fmt.Errorf("在 %s 中没有找到任何名为 %s 的产物", *distDir, archiveName(*pluginID, version, "goos", "goarch"))
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		fmt.Fprintf(os.Stderr, "[!] 以下平台缺少产物, 对应条目未回填: %s\n", strings.Join(missing, ", "))
	}

	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(manifestPath, append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("[+] 已更新 %s (字段顺序按字典序规范化)\n", manifestPath)

	if *skipRegistry {
		return nil
	}

	fmt.Println("[*] 重建 registry.json...")
	registryCmd := exec.Command("go", "run", "scripts/build-registry.go")
	registryCmd.Stdout, registryCmd.Stderr = os.Stdout, os.Stderr
	if err := registryCmd.Run(); err != nil {
		return fmt.Errorf("重建 registry.json 失败: %w", err)
	}
	return nil
}

// ---------------- 工具 ----------------

func fileDigest(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "用法: go run scripts/release.go <pack|record> [选项]\n\n")
		fmt.Fprintf(os.Stderr, "  pack   --plugin <id> [--version <v>] [--out dist]\n")
		fmt.Fprintf(os.Stderr, "  record --plugin <id> [--dist dist] [--skip-registry]\n")
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "pack":
		err = runPack(os.Args[2:])
	case "record":
		err = runRecord(os.Args[2:])
	default:
		err = fmt.Errorf("未知子命令 %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n[-] %v\n", err)
		os.Exit(1)
	}
}
