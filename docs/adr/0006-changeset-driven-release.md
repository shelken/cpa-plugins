# 插件版本与发布全链路由脚本生成

`plugin.json` 的 `version` 与 `install.artifacts[].url` 是手写字段，`release.go record` 只回填 `sha256` 与 `size`，`check-plugins --release-ready` 只能事后比对这两处自不自洽。于是升一次版本要人工改版本号、改三处产物地址、打标签、再回填哈希，四处人工动作里只有哈希有脚本兜底，地址漏改要到安装时才暴露。因此这条链路整体改由脚本生成：每次影响插件的改动提交一个变更集声明意图，发布环节消费变更集，由脚本产出 `version`、`install.artifacts[].url` 与发布标签，人只写变更集

## Status

accepted

## Considered Options

- 维持现状，靠人同步版本号与三处地址：流程最短，但没有任何机制强制「改了插件就得留下版本意图」，改动可以一路合入而不体现在版本上
- 自建 Go 变更集工具：与 `scripts/` 现有脚本同一套风格，发布聚合、打标签与地址生成都按本仓约定写，代价是这部分逻辑要自己维护
- 引入 changie：Go 原生，但发布聚合与打标签仍要按本仓约定裁剪，等于多一层外部依赖再加一层胶水
- 引入 `@changesets/cli`：生态成熟，代价是给纯 Go 仓库引入 Node 工具链

## Consequences

- `plugin.json` 的 `version` 与 `install.artifacts[].url` 变成产物而非源头，不再手工编辑，变更集是唯一入口
- `release.go record` 仍然必要：哈希只能由真实上传的产物得出，本地构建不可复现
- 发布工作流的触发方式从「推送标签」改成「合并发布提交后打标签」，`release-plugin.yml` 与 `README.md` 的「发布流程」两节需要重写
- CI 增加一条门禁：改动了 `plugins/<id>/` 却没有对应变更集时失败
- 标签命名定为 `<plugin-id>/v<version>`，每个插件独立版本、独立发布
