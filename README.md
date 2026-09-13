# CPA-PLUGINS

使用一个仓库统一聚合、管理与分发所有的 CLIProxyAPI 插件。

## 怎么工作

- **自研维护**：在 `plugins/<plugin-id>/` 编写与维护插件源码，由统一 CI 编译发布。
- **源码引入**：参考外部开源或私有仓库代码重写维护，并在工作流中按需引入构建。
- **声明分发**：在 `external/<plugin-id>.json` 声明外部可信插件，直接同步其官方 Release 产物。

## 工作流分发

- **跨平台目标**：仅编译 `linux/amd64` 与 `linux/arm64` 架构动态库，降低构建与维护成本。
- **独立生命周期**：推送 `<plugin-id>/v<version>` 标签（如 `echo-probe/v0.1.0`）仅构建对应插件并发布 Release。

## 核心脚本

### 1. 清单聚合工具 (`scripts/build-registry.go`)

扫描 `plugins/` 与 `external/` 下的所有清单文件，自动生成根目录 `registry.json`：

```bash
# 生成/更新 registry.json
go run scripts/build-registry.go

# 校验当前 registry.json 是否为最新（用于 CI）
go run scripts/build-registry.go --check
```

### 2. 线上产物校验工具 (`scripts/verify-registry-install.go`)

端到端校验清单格式、下载 Release 压缩包、匹配 SHA256 哈希并验证 ELF 动态库文件头。**支持传参定向检验**：

```bash
# 校验指定插件（推荐，发布某个插件后针对性测试）
go run scripts/verify-registry-install.go echo-probe

# 支持同时校验多个插件
go run scripts/verify-registry-install.go echo-probe codexcomp

# 校验本地尚未提交的 registry.json
go run scripts/verify-registry-install.go -local echo-probe

# 严禁不带参数运行（脚本会直接拒绝并报错退出，防止全量无谓下载）
```

## 在 CLIProxyAPI (home-ops) 中订阅

在宿主配置文件的 `plugins.store-sources` 中添加本仓库聚合清单：

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/shelken/cpa-plugins/main/registry.json
  configs:
    codexcomp:
      enabled: true
    echo-probe:
      enabled: true
      priority: 1
```

## 许可证

MIT License
