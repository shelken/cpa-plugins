# 静态清单维护

适用场景：官方客户端升级后，需要重录流量或更新插件内嵌的静态清单。两个 provider 插件的生成器都不在本仓，本仓只保留产物。

## workbuddy

生成器在 `{pi-codebuddy-provider}` 仓库，唯一入口是 `scripts/staticctl.ts`，产物直写进本仓的 `plugins/workbuddy/data/static-config.json`：

```bash
# 日常同步: 在线拉模型 + 最近抓包提协议 + 导出本仓清单 + 校验, 约 15s
bun scripts/staticctl.ts

# 只更新模型清单
bun scripts/staticctl.ts --models

# 官方客户端升级后重新采集并更新清单
bun scripts/staticctl.ts --chat

# 只读校验清单新鲜度与版本漂移, 不写文件
bun scripts/staticctl.ts --check
```

`--chat` 自建代理并冷启动 WorkBuddy，在 App 内发一条对话后关闭；本次会话同步协议、模型和清单。清单默认写进 `{active-dir}/cpa-plugins`，用 `CPA_PLUGINS_DIR` 指向其他检出

## qwenworkcn

生成器在 `{pi-qwenwork-provider}` 仓库，入口是 `scripts/export-cpa-static.ts`，数据取自该仓 `src/product/headers.json` 的 qwenwork 段与真机 `model/list` 导出的模型缓存，产物直写进本仓的 `plugins/qwenworkcn/data/static-config.json`：

```bash
bun scripts/export-cpa-static.ts
```

默认写进 `~/Code/active/cpa-plugins`，用 `CPA_PLUGINS_DIR` 指向其他检出或用 `--out <path>` 指定单个输出文件

## 审计

workbuddy 改动任何协议字段前，先跑差异审计，确认插件字段与抓包一致：

```bash
bun scripts/audit-traffic-diff.ts
```

默认只分析最新一次会话。不同接口的样本可能落在不同会话里，此时指定会话，参数必须是能唯一命中的文件名片段：

```bash
bun scripts/audit-traffic-diff.ts --session <文件名片段>
```

只有抓包版本与本机客户端版本一致时，差异报告才可作为改字段的依据

## Provenance 字段

`static-config.json` 顶层的 `provenance` 对象标记清单由哪次真实抓包生成：

| 字段 | 含义 |
| :--- | :--- |
| `captureSession` | 生成所用会话文件名 |
| `captureUserAgent` | 抓包时官方客户端的 User-Agent |
| `captureClientVersion` | 官方客户端版本号 |
| `generatedAt` | 生成时间 |
| `generator` | 生成脚本 |
| `modelsSource` | 模型清单数据来源 |

校验方式：

```bash
jq .provenance plugins/workbuddy/data/static-config.json
```

决策背景见 [ADR-0005](../adr/plugins/workbuddy/0005-static-manifest-provenance.md)。CI provenance 门禁尚未实现，进度见 [issue #1](https://github.com/shelken/cpa-plugins/issues/1)。
