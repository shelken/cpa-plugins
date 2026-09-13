# Echo Probe

CLIProxyAPI 的轻量级健康与分发验证探针插件。

## 功能

- **链路验证**：证实 C ABI 动态链接库（`.so` 或 `.dylib`）能被 CLIProxyAPI 宿主发现、装载并初始化
- **使用量统计**：通过 `usage_plugin` 观察每个完成的代理请求并递增计数
- **状态接口**：通过 `management_api` 注册 `/v0/management/plugins/echo-probe/status` 页面与 JSON 响应

## 快速上手

在 CLIProxyAPI 的 `config.yaml` 中启用此插件：

```yaml
plugins:
  enabled: true
  configs:
    echo-probe:
      enabled: true
      priority: 1
```

启动代理后，请求状态接口验证探针存活：

```bash
curl -H "Authorization: Bearer <your-management-key>" http://127.0.0.1:8317/v0/management/plugins/echo-probe/status
```

预期返回 JSON：

```json
{
  "status": "ok",
  "plugin": "echo-probe",
  "observed_requests": 0,
  "message": "cpa-plugins probe healthy and running"
}
```

响应另含 `version` 字段，取值由插件自身报告

## 配置项

| 配置字段 | 类型 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `enabled` | boolean | `false` | 是否装载并启用本探针插件 |
| `priority` | integer | `0` | 插件在宿主中的调用优先级 |

## 许可证

MIT License
