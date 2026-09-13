# 打包与安装

用户视角：在宿主里订阅本仓的清单，能看到插件、能装上、装上后能用。

## Sub-features

- `install-invariants` 仓库不变量：清单同步、id 与目录名一致、声明平台在 CI 矩阵内、哈希已回填
- `install-pack` 本地打包并自检包结构符合宿主规则
- `install-verify` 对已发布产物校验 SHA256 与动态库格式
- `install-record` 用真实产物哈希回填清单

## How to get to it (user POV)

- 宿主里把 `registry.json` 的地址配成插件源，然后安装

## Driving it with the repo scripts

Preconditions:

- 在插件仓根目录
- 需要对已发布产物做校验时，网络可达 GitHub

- **不变量（秒级，提交前必跑）。** `go run scripts/check-plugins.go`。输出 `通过: N 个插件, 0 条警告`。发布门禁用 `go run scripts/check-plugins.go -release-ready`，未回填哈希会被判失败
- **包结构自检。** `go run scripts/release.go pack --plugin workbuddy --out dist`。断言包内只有一个动态库条目、位于压缩包根级、名为 `<id><扩展名>` 或 `<id>-v<版本><扩展名>`，扩展名按平台取（linux `.so`，darwin `.dylib`）。任一处不符，宿主安装会失败
- **清单同步。** `go run scripts/build-registry.go -check` 必须报告已同步。改过 `plugin.json` 就要跑一次不带 `-check` 的重新生成
- **安装态校验。** `go run scripts/verify-registry-install.go -plugin workbuddy`（本地清单用 `-local`）。输出每个平台的 `PASSED (SHA256 verified, library format valid)`
- **回填。** 发布后把真实哈希写回：`go run scripts/release.go record --plugin <id>`。先用 `gh release download` 取回真实产物，**不要用本地临时构建的哈希**，Go 构建不可复现，两次 `pack` 的哈希就不同
- **资产名契约。** 资产名必须是 `<id>_<version>_<goos>_<goarch>.zip`，校验文件必须叫 `checksums.txt`。这两条由宿主 `internal/pluginstore` 定死，改动前先核对源码

## Gotchas

- `install.artifacts[].sha256` 是必填。缺失报 `artifact checksum missing`，不匹配报 `artifact checksum mismatch`
- CI 会为**所有**插件构建三个平台，包括 `darwin/arm64`。清单只声明 linux 时，产物已经躺在 release 里却装不上，`check-plugins` 不会报这条（它只检查声明是否都在矩阵内）。发布后顺手核对声明与 release 资产是否一一对应
- 刚推完就验证可能读到旧内容：`raw.githubusercontent` 对压缩与非压缩两个变体分别缓存，压缩变体更新滞后。`verify-registry-install` 已强制走 identity 变体，同类的自建请求也要这么做
- 文件后缀与平台不匹配时宿主**静默跳过**，不报错。装不上先看这一条
- 哈希只能取自真实上传的产物。本地构建的哈希会让安装报 checksum mismatch
- 发布工作流目前只覆盖打包与发布，不会把哈希回填进清单；回填始终是发布流程里的人为一步
