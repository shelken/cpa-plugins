# CODING_STANDARDS

编码与提交规范，分支见 `AGENTS.md` 的任务表

## 安全

- 凭据（secret、apikey）只经环境变量等隐式渠道读取使用

## 插件

- 设计前核对 `docs/reference/feature-check-list.md` 对应类型插件的功能需求
- provider 类插件注册时带模型前缀与配置开关，与其他同 id 插件区分
- 提交前检查插件 `README.md` 是否需要更新
- provider 类插件提交前检查静态字段与模型 list 是否同步，用既有同步脚本更新，有变更向用户汇报

## 文档

- 每个插件维护独立 `README.md`，编写前阅读 `doc-readme` skill

## 生产宿主

- 连接生产 CPA：读 `~/.omp/agent/models.yml` 的 cpa 条目取 baseurl，用 `scripts/management-api.go` 控制与查询
- home-ops 通常在 active 目录下，CPA 生产经 GitOps 部署，生效配置在 `cli-proxy-api/` 目录，推送前告知用户

## CI 与发布

- 部分插件不适合开源，工作流在必要时引入私有仓库构建
