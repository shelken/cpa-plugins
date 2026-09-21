# QwenWork CN

CLIProxyAPI 的通义灵码（QwenWork 中国版）原生渠道插件。

## 功能

- **原生渠道**：把 QwenWork 注册为 CPA 的一级上游渠道，提供鉴权、模型清单、对话执行与额度查询全套能力
- **Device Flow 登录**：遵循官方桌面端设备流授权流程，凭据由宿主统一托管与轮转
- **零运行时依赖**：模型清单与协议元数据内嵌在静态清单，启动不依赖远端配置
- **协议保真**：完整实现 Cosy 协议签名、动态请求头渲染与网关包装帧拆包转发
- **额度可见**：以官方钱包余额（Wallets）为主源、额度包（Usage）兜底换算上报；上游故障如实报错，不显示成零额度

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

启用后在管理面触发登录，插件返回官方授权地址，在浏览器完成授权即可。凭据由宿主写入凭据目录，后续对话与额度查询由宿主按凭据调度。

同一账号重新登录会替换旧 token，并保留用户配置。凭据过期时由宿主调用插件刷新，刷新周期根据凭据有效期计算。登录覆盖与刷新调度回归位于 `credential_merge_test.go`

模型默认带 `qwenworkcn/` 前缀（例如 `qwenworkcn/pro`、`qwenworkcn/flash`），保证模型 ID 全局唯一且不与其他提供方冲突。

## 配置项

| 配置字段 | 类型 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `enabled` | boolean | `false` | 是否装载并启用本插件 |
| `priority` | integer | `0` | 插件在宿主中的调用优先级 |
| `enable-model-prefix` | boolean | `true` | 注册模型 ID 是否带前缀 |
| `model-prefix` | string | `qwenworkcn` | 模型 ID 前缀，仅 `enable-model-prefix` 为 `true` 时生效 |

## 模型

模型清单内置 3 个官方模型：
- `pro`（高级模型，默认模型，支持推理档位与图像输入）
- `flash`（标准模型，支持推理档位与图像输入）
- `qwen3.8-max-preview`（预览模型，支持推理档位与图像输入）

全部模型均为仅推理模型（`onlyReasoning: true`），推理档位支持 `low`、`medium`、`high`、`xhigh`、`max`，默认推理档位为 `high`。

## 贡献

插件的协议字段与静态清单由研究仓 `export-cpa-static.ts` 导出并审计，严禁手动臆造或修改静态配置。

## 许可证

MIT License
