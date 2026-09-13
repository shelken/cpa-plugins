# 静态清单由上游工具生成并携带来源标记

插件运行时零网络依赖的前提是清单可信，而清单字段来自对官方客户端的流量观测，会随客户端升级失效。因此清单不在插件仓手工维护：生成器留在 `{pi-codebuddy-provider}`，产物直写进插件仓，并强制携带 provenance，记录生成所用的会话文件名、抓包 User-Agent 与生成时间。改动清单的提交必须能回溯到一次版本匹配的抓包会话。

## Status

accepted（决策已定；Consequences 第 1 条的 CI provenance 门禁尚未实现，进度见 [issue #1](https://github.com/shelken/cpa-plugins/issues/1)）

## Considered Options

- 上游产出文件、人工搬运：流程清楚，但容易搬错版本，且搬运动作不留痕迹。
- 不做来源标记：CI 只能校验 JSON 合法性，拦不住过期数据再次进入。

## Consequences

- `{cpa-plugins}` 的 CI 在清单聚合之外增加一步 provenance 校验，标记缺失或格式不符即失败。
- 门禁与人工重录并存：门禁拦住过期数据，重录动作仍由人触发，因为只有本机安装的客户端才能产生匹配版本的会话。
- 静态清单需要一次 schema 改版，以容纳 provenance 与两套身份档的头部模板。
- 清单里的版本号、模型数量这类会漂移的值不写进文档，只保留获取方式。
