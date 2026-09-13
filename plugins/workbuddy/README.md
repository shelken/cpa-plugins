# WorkBuddy

CLIProxyAPI 的腾讯 WorkBuddy 原生渠道插件。

## 功能

- **原生渠道**：把 WorkBuddy 注册为 CPA 的一级上游渠道，同时提供鉴权、模型清单、对话执行与额度查询四项能力
- **扫码登录**：走官方桌面端的 OAuth 握手，凭据由宿主托管，插件本身不落盘
- **零运行时依赖**：官方模型清单与计费倍率内嵌在静态清单里，启动不请求远端配置
- **协议保真**：请求头与请求体以真实客户端流量为准，任何字段改动都要有版本匹配的抓包会话作为证据
- **额度可见**：把套餐余量换算成剩余比例上报宿主

## 快速上手

在 CPA 的 `config.yaml` 中启用插件：

```yaml
plugins:
  enabled: true
  configs:
    workbuddy:
      enabled: true
      priority: 1
```

启用后在管理面板触发一次登录，插件会返回官方登录地址，用手机扫码确认即可。凭据写入宿主 `auth-dir`，后续对话与额度查询都由宿主按凭据调度。

模型以 `workbuddy` 渠道下的官方模型 id 直接可选，无需额外配置。

## 配置项

| 配置字段 | 类型 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `enabled` | boolean | `false` | 是否装载并启用本插件 |
| `priority` | integer | `0` | 插件在宿主中的调用优先级 |
| `identity-profile` | string | `desktop` | 请求头身份档，可选 `desktop` 或 `cli` |
| `login-profile` | string | `desktop` | 登录流程档，可选 `desktop` 或 `cli` |

两个档位互相独立。身份档决定请求头怎么写，登录档决定凭据怎么拿，允许混搭。改动后需重启宿主生效。

## 模型

模型清单来自官方客户端可见的模型集合，按官方自身的隐藏规则过滤后内嵌，不额外增删。清单包含上下文长度、输出上限与推理档位。

## 发布与安装

发布由标签触发，标签形如 `workbuddy/v0.1.0`，构建 `linux/amd64`、`linux/arm64`、`darwin/arm64` 三个平台。

宿主对产物命名有硬性要求，任一处改动都会导致安装失败：

| 项 | 要求 |
| :--- | :--- |
| 资产名 | `<id>_<version>_<goos>_<goarch>.zip` |
| 校验文件 | 必须叫 `checksums.txt` |
| 包内条目 | 只能有一个动态库，位于压缩包根级，名为 `<id><扩展名>` |
| 扩展名 | linux 用 `.so`，darwin 用 `.dylib` |

`install.artifacts[].sha256` 是必填项，缺失时报 `artifact checksum missing`，不匹配报 `artifact checksum mismatch`。清单当前尚未回填真实哈希，因此首次发布前必须补齐：发布流程产出 `checksums.txt` 后，把对应哈希写回 `plugin.json` 并重新生成根目录 `registry.json`，否则商店安装会失败。

## 贡献

插件的协议字段不靠推断，靠证据。抓包与审计工具位于 `{pi-codebuddy-provider}/scripts`，不在本仓，改动字段前先跑一次录制与差异审计：

```bash
bun run scripts/capture-traffic.ts
bun run scripts/audit-traffic-diff.ts
```

审计默认只分析最新一次会话；要指定会话用 `--session <文件名片段>`（必须唯一命中）。不同接口的样本可能落在不同会话里，此时需分别审计。

只有在抓包版本与本机客户端版本一致时，差异报告才可作为改字段的依据。

## 许可证

MIT License
