# 模型注册与可用性

插件在装载后向宿主上报自身支持的静态模型清单，并通过标准 API 暴露给调用方

## Sub-features

- `models-serve` 宿主对外接口包含插件静态声明的模型
- `models-blacklist` 插件内置的隐藏模型被自动剔除不予暴露
- `models-metadata` 上下文长度与倍率参数与静态清单一致

## How to get to it (user POV)

- 客户端配置宿主地址后获取 `/v1/models` 列表
- 在客户端模型选择下拉框中直接看到该渠道对应的模型名

## Driving it with management-api.go

Preconditions:

- 插件已在宿主中处于启用状态（`effective_enabled` 为 `true`）
- 本地沙箱无需凭据直接访问；生产实例访问 `/v1/models` 须附带合法客户端密钥

- **核对在列模型清单。** 通过客户端接口拉取完整模型集合：
  ```bash
  curl -s http://<host>:8317/v1/models -H "Authorization: Bearer <api-key>" | jq -r '.data[].id'
  ```
  输出列表须包含插件静态数据文件 `data/static-config.json` 中声明的全部模型项

- **验证黑名单过滤。** 检查官方实验性或未开放模型是否被有效拦截：
  ```bash
  curl -s http://<host>:8317/v1/models -H "Authorization: Bearer <api-key>" | jq -r '.data[].id' | grep 'preview-x'
  ```
  预期输出为空，确认隐藏规则生效

## Gotchas

- 客户端模型列表中完全看不到插件模型 -> 检查宿主配置中是否缺少 `plugins.configs.<id>.enabled: true`
- 静态配置已修改但模型列表未更新 -> 静态清单通过 Go embed 编译固化，修改后必须重新编译并替换动态库
