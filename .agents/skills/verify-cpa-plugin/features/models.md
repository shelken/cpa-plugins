# 模型注册与可用性

插件在装载后向宿主上报自身支持的静态模型清单，并通过标准 API 暴露给调用方

## Sub-features

- `models-serve` 宿主对外接口包含插件静态声明的模型
- `models-blacklist` 插件按自身规则剔除官方隐藏模型后再上报
- `models-metadata` 上下文长度与输出上限与静态清单一致

## How to get to it (user POV)

- 客户端配置宿主地址后获取 `/v1/models` 列表
- 在客户端模型选择下拉框中直接看到该渠道对应的模型名

## Driving it

Preconditions:

- 插件已在宿主中处于启用状态（`effective_enabled` 为 `true`）
- 本地沙箱无需凭据直接访问；生产实例访问 `/v1/models` 须附带合法客户端密钥，密钥由脚本就地取得，不要手抄到命令行

- **核对在列模型清单。** 通过客户端接口拉取完整模型集合：
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -auth client -path /v1/models | jq -r '.data[].id'
  ```
  输出列表须包含插件静态数据文件 `data/static-config.json` 中声明的全部模型项

- **验证黑名单过滤。** 确认被剔除的模型没有对外暴露：
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -auth client -path /v1/models \
    | jq -r '.data[].id' | grep -E '^(auto|default|hunyuan-3b)$'
  ```
  预期无输出即为过滤生效。剔除名单与前缀规则见 `plugins/workbuddy/models.go` 的 `isModelAllowed`，该名单会随官方客户端版本变化，以代码为准

## Gotchas

- 客户端模型列表中完全看不到插件模型 -> 检查宿主配置中是否缺少 `plugins.configs.<id>.enabled: true`
- 静态配置已修改但模型列表未更新 -> 静态清单通过 Go embed 编译固化，修改后必须重新编译并替换动态库
