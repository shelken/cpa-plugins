# CPA-PLUGINS

使用一个仓库统一聚合、管理与分发所有的 CLIProxyAPI 插件。

## 怎么工作

- **自研维护**：在 `plugins/<plugin-id>/` 编写与维护插件源码，由统一 CI 编译发布。
- **源码引入**：参考外部开源或私有仓库代码重写维护，并在工作流中按需引入构建。
- **声明分发**：在 `external/<plugin-id>.json` 声明外部可信插件，直接同步其官方 Release 产物。

## 工作流分发

- **跨平台目标**：构建 `linux/amd64`、`linux/arm64`、`darwin/arm64` 三个平台。c-shared 是 CGO 构建，无法交叉编译，每个平台必须用对应的原生运行器构建。
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

### 3. 本地沙箱 (`scripts/dev-sandbox.go`)

一条命令完成「编译插件 → 生成沙箱配置 → 启动宿主 → 断言装载与注册」，不需要真实凭据：

```bash
go run scripts/dev-sandbox.go --plugin workbuddy --host /path/to/cliproxyapi

# 不指定 --host 时依次尝试 $CPA_HOST_BIN、~/.cache/cpa-plugins/host/v<SDK版本>/cliproxyapi
# 也可以用 --host-src 指向 CLIProxyAPI 源码目录现场构建（要求签出到插件所需的版本）
# --keep 保留宿主机进程与沙箱目录，便于继续手工调试
```

断言四件事：宿主日志出现 `plugin loaded` 与 `plugin registered`；`/v1/models` 覆盖插件静态清单（`plugins/<id>/data/static-config.json`）声明的全部模型；管理面对声明了 `ConfigFields` 的插件返回非空 `config_fields`；实现额度能力的插件出现在 `quota/providers` 列表中。

### 4. 发布工具 (`scripts/release.go`)

```bash
# 消费变更集, 产出新版本并同步源码里的版本字面量、plugin.json 的版本与三处产物地址
go run scripts/release.go version --plugin workbuddy

# 本地打包当前平台，产物按宿主契约命名并自检，输出 sha256
go run scripts/release.go pack --plugin workbuddy --out dist

# 用真实发布产物回填 plugin.json 的 sha256，并重建 registry.json
go run scripts/release.go record --plugin workbuddy --dist dist
```

`pack` 的产物只有在确实被上传发布时才能用于回填哈希，否则安装时会报 `checksum mismatch`。同样只支持当前平台。

### 5. 仓库不变量检查 (`scripts/check-plugins.go`)

```bash
go run scripts/check-plugins.go                  # 常规检查
go run scripts/check-plugins.go --strict         # 警告也视为失败
go run scripts/check-plugins.go --release-ready  # 发布门禁：未回填哈希即失败
go run scripts/check-plugins.go --changesets-base origin/main  # 变更集门禁：有改动却没有版本意图即失败
```

检查项：`registry.json` 与插件清单同步；`plugin.json` 的 id 与目录名一致；声明的平台都存在于发布工作流的构建矩阵；产物 URL 末段等于宿主期望的资产名；`sha256` 已回填且为 64 位十六进制；每个插件都有 `README.md`；各插件所钉宿主 SDK 版本与 go 指令是否一致。变更集门禁的基准 ref 在 CI 里由 push 的 `github.event.before` 或 PR 的 `base.sha` 自动给出。

## 发布流程

版本、三处产物地址与标签都由脚本产出，人只写变更集。改了 `plugins/<id>/` 却没有留下版本意图，CI 会在差异上失败。

变更集是一个 json 文件，放在 `plugins/<id>/changesets/` 下，`bump` 取 `patch`、`minor`、`major` 之一，多个变更集共存时取最高档：

```json
{"bump": "patch", "note": "会话标识改用宿主的规范会话 id"}
```

```bash
# 1. 写变更集, 文件名自取, 建议带日期与主题
# 2. 产出新版本: 同步源码里的 Version 字面量、plugin.json 的 version 与三处产物地址, 清空旧哈希, 删除已消费的变更集
go run scripts/release.go version --plugin workbuddy
# 3. 本地预检产物命名与包结构 (只支持当前平台)
go run scripts/release.go pack --plugin workbuddy --out dist
# 4. 常规检查, 此时 sha256 待回填属于预期警告, 不要用 --release-ready
go run scripts/check-plugins.go
# 5. 提交并按第 2 步打印的标签推送
git add plugins/workbuddy && git commit
git tag workbuddy/v<X.Y.Z> && git push origin main workbuddy/v<X.Y.Z>
# 6. CI 构建发布完成后, 校验线上产物与哈希
go run scripts/verify-registry-install.go -local workbuddy
```

第 2 步会打印新版本与标签，标签的版本号必须与 `plugin.json` 一致，否则产物地址指向不存在的资产。

哈希不需要人工回填：标签触发构建发布后，工作流的 `record` 作业自己下载产物、跑 `release.go record`，再把 `plugin.json` 与 `registry.json` 的改动提交回 main。哈希只能由真实上传的产物得出，本地 `pack` 的产物不能用来回填。

第 6 步通过后，在宿主侧把插件更新到新版本，再用管理面确认版本与配置字段都到位：

```bash
go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins \
  | jq -c '.plugins[] | select(.id=="workbuddy") | {version: .metadata.version, config_fields}'
```

判据是 `version` 等于新标签的版本号，且声明过 `ConfigFields` 的插件 `config_fields` 非空。两者缺一，说明宿主仍跑旧产物。

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
