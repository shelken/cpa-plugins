# QwenWork CN

CLIProxyAPI 的通义灵码（QwenWork 中国版）原生渠道插件

## 功能

- **原生渠道**：把 QwenWork 注册为 CPA 的一级上游渠道，提供鉴权、模型清单、对话执行与额度查询全套能力
- **Device Flow 登录**：遵循官方桌面端设备流授权流程，凭据由宿主统一托管与轮转
- **零远端配置**：模型清单与协议元数据内嵌在静态清单，启动不请求远端配置
- **协议保真**：完整实现 Cosy 协议签名、动态请求头渲染与网关包装帧拆包转发
- **额度可见**：以官方钱包余额（Wallets）为主源、额度包（Usage）兜底换算上报，单源失败静默降级，两源都失败时按上游真实错误上报

## 快速上手

在 CPA 的 `config.yaml` 中启用插件：

```yaml
plugins:
  enabled: true
  configs:
    qwenworkcn:
      enabled: true
      priority: 1
```

启用后在管理面触发登录，插件返回官方授权地址，在浏览器完成授权即可。凭据由宿主写入凭据目录，后续对话与额度查询由宿主按凭据调度

模型默认带 `qwenworkcn/` 前缀（例如 `qwenworkcn/pro`、`qwenworkcn/flash`），保证模型 ID 全局唯一且不与其他提供方冲突

## 配置项

| 配置字段 | 类型 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `enabled` | boolean | `false` | 是否装载并启用本插件 |
| `priority` | integer | `0` | 插件在宿主中的调用优先级 |
| `enable-model-prefix` | boolean | `true` | 注册模型 ID 是否带前缀 |
| `model-prefix` | string | `qwenworkcn` | 模型 ID 前缀，仅 `enable-model-prefix` 为 `true` 时生效 |

`enabled` 与 `priority` 由宿主解析，后两项由插件解析并出现在管理面板的插件配置表单里

## 模型

模型清单内置 3 个官方模型，均支持图像输入与推理档位，上下文长度 1000000，单次输出上限 128000：

- `flash`（标准）
- `pro`（高级）
- `qwen3.8-max-preview`（预览）

全部模型均为仅推理模型（`onlyReasoning: true`），推理档位支持 `low`、`medium`、`high`、`xhigh`、`max`，未显式指定时由插件按 `high` 兜底

## 额度查询页面

插件自带一个管理页（菜单名 `QwenWork Quota`），装好后在管理面板侧边栏点进去，即可看到每个 QwenWork 凭据的套餐档位与各窗口剩余比例进度条

- 数据来源是宿主管理 API：`/v0/management/auth-files` 列凭据，`/v0/management/plugins/qwenworkcn/quota` 查单个凭据额度，凭据密钥全程留在宿主进程
- 页面与面板同源，自动读取面板记住的管理密钥，读取失败（如面板改了本地存储格式）时页面会给出手贴密钥的输入框兜底
- 打开页面加载一次，手动「刷新」按钮更新数据，无自动轮询

## 能力边界

- 只注册 `chat-completions` 输入输出格式，不提供 Anthropic 与 Responses 入口
- `count_tokens` 按请求体字节数除以 4 估算，不调用上游计数接口
- 上游 Cosy 信封里只有 `max_tokens`、`context_length`、`reasoning_effort`（外加本插件补的 `tool_choice`、`parallel_tool_calls`）这几个落点，因此 `temperature`、`verbosity`、`store`、`stream_options` 收下即丢弃：客户端可以照常发送，但采样参数不会影响上游，也不会有报错。官方客户端抓包的 `parameters` 同样只带 `context_length` 与 `max_tokens`

## 发布与安装

发布由标签触发，标签形如 `qwenworkcn/v<version>`，构建 `linux/amd64`、`linux/arm64`、`darwin/arm64` 三个平台

宿主对产物命名有硬性要求，完整契约与常见安装错误见 [docs/reference/host-artifact-contract.md](../../docs/reference/host-artifact-contract.md)。`install.artifacts[].sha256` 必填，只能由真实发布产物回填，完整流程见 [docs/how-to/plugin-release.md](../../docs/how-to/plugin-release.md)

## 贡献

插件的协议字段与静态清单由 `{pi-qwenwork-provider}` 的 `scripts/export-cpa-static.ts` 导出并审计，产物直接写入 `data/static-config.json`，客户端升级后用同一脚本重新导出，禁止手动臆造或修改静态配置

## 许可证

MIT License
