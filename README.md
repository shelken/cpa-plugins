# CPA-PLUGINS

使用一个仓库统一聚合、管理与分发所有的 CLIProxyAPI 插件。

## 功能

- **自研维护**：在 `plugins/<plugin-id>/` 编写与维护插件源码，由统一 CI 编译发布
- **源码引入**：参考外部开源或私有仓库代码重写维护，并在工作流中按需引入构建
- **声明分发**：在 `external/<plugin-id>.json` 声明外部可信插件，直接同步其官方 Release 产物

## 快速上手

1. 在宿主配置的 `plugins.store-sources` 中添加本仓库聚合清单：

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/shelken/cpa-plugins/main/registry.json
  configs:
    workbuddy:
      enabled: true
```

2. 重启宿主，插件装载完成后在管理面触发扫码登录
3. 用管理面确认版本与配置字段都到位：

```bash
go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins \
  | jq -c '.plugins[] | select(.id=="workbuddy") | {version: .metadata.version, config_fields}'
```

判据是 `version` 等于目标版本号，且声明过 `ConfigFields` 的插件 `config_fields` 非空。

## 核心脚本

| 脚本 | 作用 | 示例 |
| :--- | :--- | :--- |
| `build-registry.go` | 扫描 `plugins/` 与 `external/` 生成 `registry.json`，`--check` 校验是否最新 | `go run scripts/build-registry.go --check` |
| `verify-registry-install.go` | 端到端校验清单、下载 Release 产物、核对 SHA256 与动态库格式，必须传插件 id | `go run scripts/verify-registry-install.go workbuddy` |
| `dev-sandbox.go` | 编译插件、生成沙箱配置、启动宿主并断言装载与注册；`--plugin` 可重复传参一次起多个插件，`--checks` 选断言子集 | `go run scripts/dev-sandbox.go --plugin workbuddy --plugin qwenworkcn --checks load,models` |
| `release.go` | 消费变更集产出版本、按宿主契约打包、用真实产物回填哈希，无参数打印子命令用法 | `go run scripts/release.go version --plugin workbuddy` |
| `check-plugins.go` | 仓库不变量检查与发布门禁 | `go run scripts/check-plugins.go --strict` |
| `verify-chat.go` | 对真实上游发对话请求验证对话链路；`-scenarios` 按场景选判据（`-list` 打印清单），不传则跑默认子集 | `go run scripts/verify-chat.go -base http://<host>:8317 -model workbuddy/hy3 -scenarios session,nonstream` |
| `management-api.go` | 调用宿主管理面接口 | `go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins` |

各脚本的完整参数与行为以脚本自身的 usage 输出为准：

```bash
go run scripts/release.go
```

## 文档

- [docs/README.md](docs/README.md)：文档索引
- [docs/adr/](docs/adr/)：架构决策记录

## 许可证

MIT License
