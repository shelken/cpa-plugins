# WorkBuddy 动态模型清单：凭据事件驱动的 /v3/config 同步

静态清单是构建期产物，上游模型的增删只在发版时可见：本地 main 与 2026-09-24 抓包对比，静态清单已含 8 个上游不再引用的旧代模型，且上游新增模型（如图像生成、新旗舰档）要等下一次发版才暴露。决策：插件在**凭据刷新成功**与**登录成功**两个事件点拉取 `GET /v3/config`，经显式黑名单过滤后存入独立的 `liveModels` 内存状态作为生效模型表，静态清单降级为无凭据与重启窗口期的兜底。

## Status

accepted（对 [ADR-0002](./0002-workbuddy-native-provider-and-traffic-sync.md) 做两处局部取代：①「不实现动态模型发现，静态清单是模型清单的唯一来源」——动态清单以 executor 侧事件驱动拉取实现，与被否决的 per-auth `model.for_auth` 回调不同路；②「插件无状态」——新增 `liveModels` 一项进程内状态。其余边界不变）

## Considered Options

- 维持纯静态清单：实现最简，但上游增删模型只能靠发版同步，用户在发版间隔内看不到新模型
- 恢复 ADR-0002 否决的 per-auth `model.for_auth` 动态回调：把清单查询绑到凭据维度，宿主每轮调度都触发上游请求，语义重、行为难预期
- 插件级事件驱动拉取 + 显式黑名单（选定）：刷新周期（小时级）即天然节流，黑名单保证「上游新增模型默认暴露」，过滤规则全部在代码中显式可见

## Consequences

- 模型过滤是**黑名单**（`excludeModelReasons`，精确 id + 理由，判据来自 2026-09-24 抓包：这些模型 `tags` 为空且不被任何 agent 引用），上游新增模型不在表内即自动暴露；pi 侧 `models-filter.ts` 的旧代前缀排除不搬入——服务器网关多暴露旧模型无副作用
- `supportsImages` 三态归一：上游缺失（nil）视为 true（fail-open，与 executor 对未知模型的既有默认一致），显式 false 才关闭图片透传；防 Go 零值把「未声明」静默解释成「不支持」
- 同名模型 `DisplayName` 加 id 差量后缀（`Hy3 (B)`），消歧在 `mapManifestModels` 统一生效，静态路径不受影响；积分倍率归一后写 `ModelInfo.Description`，`x0.00` 标免费。注意：宿主 `/v1/models` 当前只透传 `id/object/owned_by`，两者的用户可见性待宿主端点透传
- `liveModels` 是**插件级 last-writer-wins**：多凭据各刷新时后写覆盖先写。同套餐多账号的 `/v3/config` 实测一致（2026-09-24 抓包），当前无差异；若未来不同账号清单分化，需回访为取交集或固定凭据
- 拉取在凭据刷新/登录的同步路径内，独立 10s 超时上界（宿主传入的 ctx 是 Background 无法取消，不加界时上游挂住会拖慢刷新响应并连带推迟其余凭据轮转）；失败只记日志，等下一轮刷新自然重试，静态清单兜底，无退避计时器
- 不落盘：宿主不提供插件持久化 API，宿主重启后到首次凭据刷新之间（小时级）暴露静态清单，属接受的窗口期
- 静态清单（`data/static-config.json`）保留并继续由生成器同步，映射路径零丢失由既有测试钉住（`TestManifestModelsAllRegistered`）
