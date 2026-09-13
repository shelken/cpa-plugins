# workbuddy 模型 id 前缀化

workbuddy 注册给宿主的模型 id 是裸 id（如 `hy3`）。omp 等客户端按裸 id 查内置模型目录，`hy3` 这类短 id 会撞上其他通道的目录行，拿到错误的档位与能力信息。id 冲突的根源是裸 id 不保证全局唯一，只在一个 provider 范围内唯一。因此插件注册的模型 id 统一带上前缀（默认 `workbuddy/`），保证 id 全局唯一，客户端按完整 id 查目录不再撞行。

## Status

accepted

## Considered Options

- 只在 models.yml 侧改名：撞名只在一个消费端被绕过，其他按裸 id 查目录的客户端仍然拿到错误档位
- 把通道标识放进 `owned_by` 而不动 id：omp 目录仍按裸 id 键查，不解决撞名
- 注册 id 统一加前缀（选定）：id 全局唯一，所有消费端一次修正；代价是 id 形态变更对所有客户端可见

## Consequences

- `/v1/models` 暴露的 id 全部变为 `workbuddy/<裸id>`，属破坏性改名，依赖旧裸 id 的客户端配置必须迁移
- 执行器在协议边界剥掉前缀，上游仍收到裸 id，与 ADR-0002 的协议保真不变
- 新增 `enable-model-prefix`（默认 true）与 `model-prefix`（默认 `workbuddy`）两个配置项，前缀可关可换
- 同类 provider 插件沿用此约定：注册 id 带前缀，执行侧剥前缀
