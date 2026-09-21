# WorkBuddy

CLIProxyAPI 的腾讯 WorkBuddy 原生渠道插件

## 功能

- **原生渠道**：把 WorkBuddy 注册为 CPA 的一级上游渠道，同时提供鉴权、模型清单、对话执行与额度查询四项能力
- **扫码登录**：走官方桌面端的 OAuth 握手，凭据由宿主托管，插件本身不落盘
- **手动签到**：在额度页对单个账号签到或一键签到全部账号，协议与桌面客户端一致，可在配置中关闭
- **零远端配置**：官方模型清单内嵌在静态清单里，启动不请求远端配置
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

启用后在管理面板触发一次登录，插件会返回官方登录地址，用手机扫码确认即可。凭据写入宿主 `auth-dir`，后续对话与额度查询都由宿主按凭据调度

模型以 `workbuddy/` 前缀加官方模型 id 的形态注册，例如 `workbuddy/hy3`，保证 id 全局唯一、不与其他渠道的目录冲突

## 配置项

| 配置字段 | 类型 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `enabled` | boolean | `false` | 是否装载并启用本插件 |
| `priority` | integer | `0` | 插件在宿主中的调用优先级 |
| `identity-profile` | string | `desktop` | 请求头身份档，可选 `desktop` 或 `cli` |
| `login-profile` | string | `desktop` | 登录流程档，可选 `desktop` 或 `cli` |
| `enable-model-prefix` | boolean | `true` | 注册模型 id 是否带前缀 |
| `enable-checkin` | boolean | `true` | 是否允许管理页手动签到（单账号与一键全部） |
| `model-prefix` | string | `workbuddy` | 模型 id 前缀，仅 `enable-model-prefix` 为 `true` 时生效 |

两个档位互相独立。身份档决定请求头怎么写，登录档决定凭据怎么拿，允许混搭。配置变更由宿主重载后生效，无需重启宿主

## 模型

模型清单来自官方客户端可见的模型集合，按官方自身的隐藏规则过滤后内嵌，不额外增删。清单包含上下文长度、输出上限与推理档位

## 额度查询页面

插件自带一个管理页（菜单名 `WorkBuddy Quota`，路径 `/v0/resource/plugins/workbuddy/quota`），装好后在管理面板侧边栏点进去，即可看到每个 WorkBuddy 凭据的剩余比例进度条

- 数据来源是宿主管理 API：`/v0/management/auth-files` 列凭据，`/v0/management/plugins/workbuddy/quota` 查单个凭据额度，凭据密钥全程留在宿主进程
- 页面与面板同源，自动读取面板记住的管理密钥，读取失败（如面板改了本地存储格式）时页面会给出手贴密钥的输入框兜底
- 手动「刷新」按钮更新数据，无自动轮询

### 手动签到

额度页默认开启签到能力（`enable-checkin` 可关闭）：

- 每张账号卡片有「签到」按钮，头部有「全部签到」按钮
- 签到请求走 `POST /v0/management/plugins/workbuddy/checkin`，浏览器只提交 `auth_index`，access token 始终留在宿主与插件进程内
- 「全部签到」按账号顺序逐个请求（与官方客户端行为一致），单卡失败不影响后续账号，结束后显示成功/已签/失败汇总
- 签到协议固定使用桌面主进程档（端点 `/v2/billing/meter/daily-checkin`），与 `identity-profile` 配置无关，上游返回「今日已领」时页面显示今日已签到，不重复计奖
- 插件不保存签到记录：是否已签到由上游判定，重复点击安全。关闭 `enable-checkin` 后页面隐藏签到按钮，后端端点也返回 403

## 能力边界

- 只注册 `chat-completions` 输入输出格式，不提供 Anthropic 与 Responses 入口
- `count_tokens` 按请求体字节数除以 4 估算，不调用上游计数接口
- 系统与用户文本中的竞品自指按静态清单 `sanitizations` 规则改写后再发往上游

## 发布与安装

发布由标签触发，标签形如 `workbuddy/v<version>`，构建 `linux/amd64`、`linux/arm64`、`darwin/arm64` 三个平台

宿主对产物命名有硬性要求，完整契约与常见安装错误见 [docs/reference/host-artifact-contract.md](../../docs/reference/host-artifact-contract.md)。`install.artifacts[].sha256` 必填，只能由真实发布产物回填，完整流程见 [docs/how-to/plugin-release.md](../../docs/how-to/plugin-release.md)

## 贡献

插件的协议字段不靠推断，靠证据。抓包与审计工具位于 `{pi-codebuddy-provider}/scripts`，不在本仓，录制、导出与差异审计的完整步骤见 [docs/how-to/static-manifest.md](../../docs/how-to/static-manifest.md)

## 许可证

MIT License
