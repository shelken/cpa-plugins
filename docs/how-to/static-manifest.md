# 静态清单维护

适用场景：workbuddy 官方客户端升级后，需要重录流量、更新插件内嵌的静态清单。

## 清单来源

静态清单不在本仓手工编辑。生成器在 `{pi-codebuddy-provider}` 仓库，产物直写进本仓的 `plugins/workbuddy/data/static-config.json`。

生成步骤：

1. 在本机安装与目标版本一致的 WorkBuddy 客户端
2. 启动抓包并操作客户端，产生一次会话
3. 在 `{pi-codebuddy-provider}` 下导出静态清单：

```bash
bun run scripts/capture-traffic.ts --export
```

## 审计

改动任何协议字段前，先跑差异审计，确认插件字段与抓包一致：

```bash
bun run scripts/audit-traffic-diff.ts
```

默认只分析最新一次会话。不同接口的样本可能落在不同会话里，此时指定会话，参数必须是能唯一命中的文件名片段：

```bash
bun run scripts/audit-traffic-diff.ts --session <文件名片段>
```

只有抓包版本与本机客户端版本一致时，差异报告才可作为改字段的依据。

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
