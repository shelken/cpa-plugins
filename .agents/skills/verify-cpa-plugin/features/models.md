# 模型注册与可用性

插件装载后向宿主上报静态模型清单, 并经标准 API 暴露给调用方

## Sub-features

- `models-serve` 宿主对外接口包含插件静态声明的模型
- `models-blacklist` 带动态清单过滤的插件 (如 workbuddy) 按自身规则剔除官方隐藏模型后再上报; 纯静态插件 (如 qwenworkcn) 全量直报, 无此项
- `models-metadata` 上下文长度与输出上限与静态清单一致

## How to get to it (user POV)

- 客户端配置宿主地址后获取 `/v1/models` 列表
- 客户端模型下拉框直接看到该渠道的模型名

## Driving it

Preconditions:

- 插件已启用 (`effective_enabled` 为 `true`)
- 本地沙箱无需凭据即可访问; 生产实例访问 `/v1/models` 须用 `-auth client`, 客户端密钥由脚本就地取得

- **核对在列模型。** 拉取完整模型集合, 输出须包含 `data/static-config.json` 声明的全部模型项 (插件启用模型前缀时为 `<id>/<model>` 形态):
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -auth client -path /v1/models | jq -r '.data[].id'
  ```

- **验证黑名单过滤。** 被剔除的模型不得对外暴露, 预期无输出。正则须兼容前缀形态, 行首行尾定界会漏判 `workbuddy/auto` 这类带前缀泄漏:
  ```bash
  go run scripts/management-api.go -base http://<host>:8317 -auth client -path /v1/models \
    | jq -r '.data[].id' | grep -E '(^|/)(auto|default|hunyuan-3b)$'
  ```
  剔除名单与前缀规则见 `plugins/<id>/models.go` 的 `isModelAllowed`, 名单随官方客户端版本变化, 以代码为准
- **核对元数据。** `/v1/models` 只返回 id 与归属, 上下文长度等元数据以静态清单与单测为准: 对比 `data/static-config.json` 中该模型的声明, 并跑 `cd plugins/<id> && go test ./...` 中钉住元数据映射的用例

## Gotchas

- 客户端模型列表看不到插件模型 -> 检查宿主配置 `plugins.configs.<id>.enabled` 是否为 `true`
- 静态配置已改但模型列表未更新 -> 静态清单经 Go embed 编译固化, 改完必须重新编译并替换动态库
- 沙箱断言或比对模型 id 时注意前缀形态, 历史上按裸 id 比对导致 miss (postmortems/005)
