# Monorepo 布局与分发策略

我们需要一个统一枢纽：既开发自研 CLIProxyAPI 插件，也把可信的外部插件通过单一 store 来源分发出去。决策采用独立清单的 monorepo 结构：自研插件在 `plugins/<plugin-id>/`，外部插件在 `external/<plugin-id>.json` 声明，本地工具把它们编译成根 `registry.json`。各插件用独立 git 标签（`<plugin-id>/v<version>`）触发各自的 CI 构建。外部插件支持直接引用上游 Release，需要自建编译时可回退到仓内构建。

## Status

accepted

## Considered Options

- 全仓统一版本，所有插件共用一个发布标签
- 直接手工维护单一 `registry.json`，不做子清单
- 用 Git Submodules vendoring 外部插件源码

## Consequences

- 消费端只需在 `plugins.store-sources` 配置一个来源 URL
- 每个插件独立发布生命周期，依赖边界清晰
- 增删插件只需改对应目录的文件，不会产生合并冲突
