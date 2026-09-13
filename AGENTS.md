# CPA-PLUGINS

聚合与分发 CLIProxyAPI 插件的统一 Monorepo 仓库。

## 布局

- `plugins/`: 自研插件源码目录，每个插件拥有独立子目录、`plugin.json` 与 Go 模块
- `external/`: 外部可信插件的声明文件，通过 JSON 直接引用上游 Release 或自建构建配方
- `scripts/`: 核心维护与校验脚本
  - `build-registry.go`: 扫描子目录声明并合并生成根目录 `registry.json`，支持 `--check` 一致性检查
  - `verify-registry-install.go`: 端到端拉取清单、下载 Release 压缩包并校验 SHA256 与 ELF 动态库格式的验证工具，支持按插件 ID 参数化校验
- `registry.json`: CPA 宿主通过 `plugins.store-sources` 直接订阅的单一聚合清单文件
- `.github/workflows/`: 跨平台 CI 工作流，负责清单校验以及按标签自动发布 Release 产物

## 约束

- 严禁直接手动编辑 `registry.json`。修改插件清单后统一运行 `go run scripts/build-registry.go` 生成。
- 发布或更新特定插件后，运行 `go run scripts/verify-registry-install.go <plugin-id>` 进行定向产物校验。
- 仅编译发布 `linux/amd64` 与 `linux/arm64` 两个平台的动态库二进制。
- 因为有些插件不适合开源，工作流在必要时引入私有仓库进行构建。
- 为每个插件编写独立的 `README.md`，编写前阅读相关 SKILL。
- 每次提交前，检查对应插件 `README.md` 是否需要更新。

## 仓库参考

- [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI): 权威主程序，一般在本地仓库 `{kaiyuan-dir}/CLIProxyAPI` 中
