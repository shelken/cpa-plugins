# 发布清单里的伪占位哈希

**日期**: 2026-09-13
**影响**: 发布清单处于不可安装状态；一次给子代理的修正指令本身是错的，白跑一轮
**发现人**: 主代理（核对宿主源码时发现）

## 问题

`plugins/workbuddy/plugin.json` 的 `install.artifacts[]` 里，`sha256` 是全 0、`size` 是 0。宿主对这个字段是必填，缺失或不匹配都会让安装失败。全 0 既不是真实值，也不像缺失，于是问题被推迟到用户装不上时才暴露。

## 现象

宿主源码 `internal/pluginstore/direct.go:45`：

```go
expected := strings.ToLower(strings.TrimSpace(artifact.SHA256))
if expected == "" {
    return fmt.Errorf("artifact checksum missing")
}
```

全 0 不走 missing 分支，会在下载后落到 `artifact checksum mismatch`。

## 根因

错误假设：把「占位」当成可接受的中间状态，用看起来有效的值代替明确缺失。

实际约束：哈希只能由真实构建产物得出，而 Go 构建默认不可复现，实测同一份代码连续两次打包得到两个不同的哈希，所以本地构建的哈希不能用于回填。

叠加错误：我给子代理下过「参照 `plugins/echo-probe/plugin.json`，只保留 `goos`/`goarch`/`url`」的指令，但我没读那个文件。它本来就带着真实哈希。指令错了，子代理就照错执行，把哈希整个删掉了。

## 修复

- 清单只声明真实会构建的平台。曾经声明过 `darwin/amd64`，而发布工作流只构建 linux 两平台加 darwin/arm64，那个产物永远不会存在。
- 新增 `scripts/release.go record`：只回填 `dist/` 里确实存在的 zip，缺哪个平台就明确报哪个平台，不猜值。
- 发布流程固定为「打标签 → CI 构建 → `gh release download` 取回真实产物 → 回填哈希 → 重建 registry.json」。

## 预防

- 必填字段禁止用零值、全 F 或 `"TODO"` 伪装：要么是真实值，要么是明确缺失，让报错替你把话说清楚。
- 凡是要「参照某文件」的指令，先读那个文件再下指令。指令本身也是需要验证的产物。
- 发布路径的改动，用宿主源码里的校验函数反查一遍必填项，不靠记忆。
