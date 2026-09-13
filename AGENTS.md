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

- 因为有些插件不适合开源，工作流在必要时引入私有仓库进行构建
- 为每个插件编写独立的 `README.md`，编写前阅读相关 SKILL
- 每次提交前，检查对应插件 `README.md` 是否需要更新
- 严禁在任何 调试/测试/bash 中直接显式读取和使用secret/apikey; 只能隐式读取和使用(例如环境变量).
- 阅读 docs/adr 了解插件决策

## 注意

- 关于provider类的插件, 统一在注册时带上模型前缀, 且带上配置开关, 避免和其他同id混用

## 快速指路

- 如何连接生产cpa: 阅读~/.omp/agent/models.yml中的cpa找到baseurl, 然后通过 scripts/management-api.go 控制和查询
- home-ops: 通常在我的active目录下, cpa生产通过gitops部署在那里, 配置也在里面; 当需要修改配置生效时去找`cli-proxy-api`目录, 推送前告知用户

## 仓库参考

- [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI): 权威主程序，一般源码会在本地仓库 `{kaiyuan-dir}/CLIProxyAPI` 中
