# qwenworkcn 模型 id 前缀化

QwenWork 官方网关暴露的原始模型键均为短 id（如 `pro`、`flash`、`qwen3.8-max-preview`）。若直接以裸 id 注册给宿主，omp 等客户端按裸 id 查内置模型目录时会撞上其他通道的目录行，拿到错误的档位与能力信息，造成调度与档位错乱。因此插件注册的模型 id 统一带上前缀（默认 `qwenworkcn/`），保证 id 全局唯一；而在向 Cosy 网关发起上行请求时于协议边界剥除前缀，保持上游协议保真。

## Status

accepted

## Considered Options

- 注册裸 id（`pro`、`flash`）：实现简单，但极短 id 必然与其他 provider 冲突，破坏 Monorepo 插件的命名空间隔离。
- 仅在 models.yml 侧改名或把通道标识放进 `owned_by` 而不动 id：客户端仍按裸 id 查目录，无法解决短 id 撞行问题。
- 注册侧统一加前缀、执行侧剥前缀（选定）：注册给宿主的 id 全局唯一（如 `qwenworkcn/pro`），执行器在构建 Cosy 请求信封与 `X-Model-Key` 请求头时剥除前缀，兼顾全局唯一性与上游协议保真。

## Consequences

- `/v1/models` 暴露的模型 id 规范为 `qwenworkcn/<裸id>`（如 `qwenworkcn/pro`、`qwenworkcn/flash`、`qwenworkcn/qwen3.8-max-preview`），客户端需使用完整带前缀 id。
- 执行器在协议边界通过 `manifestModelID` 剥除前缀，上游 Cosy 网关收到的 `model_config.key` 与 `X-Model-Key` 恒为裸 id，完全对齐官方客户端抓包流量。
- 新增 `enable-model-prefix`（默认 true）与 `model-prefix`（默认 `qwenworkcn`）配置项，前缀可关可换。
- 与 workbuddy ADR-0007 确立的 Monorepo provider 插件规范保持一致。
