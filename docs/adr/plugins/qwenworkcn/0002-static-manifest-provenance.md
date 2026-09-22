# 静态清单由上游工具生成并携带来源标记

插件运行时零网络依赖的前提是清单可信，而 Cosy 协议信封、请求头模板与模型清单来自对官方客户端运行时流量与逆向成果的实证沉淀，会随客户端升级失效。因此静态清单不在插件仓手工维护：生成器留在 `{pi-qwenwork-provider}` 仓库（`scripts/export-cpa-static.ts`），产物直写进本仓 `plugins/qwenworkcn/data/static-config.json`，并强制携带顶层 `provenance` 来源标记，记录官方客户端 User-Agent、生成时间、生成脚本与模型缓存获取时间。改动清单的提交必须能回溯到一次版本匹配的客户端抓包与模型清单导出。

## Status

accepted（决策已定；Consequences 第 1 条的 CI provenance 门禁尚未实现，进度见 [issue #1](https://github.com/shelken/cpa-plugins/issues/1)）

## Considered Options

- 上游产出文件、人工搬运：流程清楚，但容易搬错版本或遗漏关键字段，且搬运动作不留版本痕迹。
- 不做来源标记：CI 只能校验 JSON 语法合法性，拦不住过期的协议版本与模型键再次进入。
- 上游工具直写本仓并内嵌 provenance（选定）：由 `export-cpa-static.ts` 基于真实客户端抓包头部模板与网关 `model/list` 缓存统一导出，确保数据保真度与版本可追溯性。

## Consequences

- 清单生成边界清晰：生成脚本留在 `{pi-qwenwork-provider}`，本仓仅作为产物接收方，更新操作通过 `bun scripts/export-cpa-static.ts` 执行。
- `static-config.json` 顶层包含完整 `provenance` 对象（含 `captureUserAgent`、`generatedAt`、`generator`、`modelsSource`、`modelCacheFetchedAt`），改动必须能追溯到对应版本抓包。
- 门禁与人工重录并存：CI provenance 校验防止过期或手工篡改数据入仓；官方客户端升级后通过上游工具链重新拉取模型缓存并刷新清单。
- 清单里的客户端版本号、Cosy 协议版本与模型数量等会漂移的值不硬编码进文档，只保留获取方式。
