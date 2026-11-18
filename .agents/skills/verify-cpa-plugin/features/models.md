# 模型注册与可用性

插件装载后向宿主上报模型清单, 并经标准 API 暴露给调用方。qwenworkcn 只报编译进动态库的静态清单; workbuddy 启动时同样报静态清单, 登录成功或凭据刷新后拉取官方 `/v3/config`, 过滤后的非空 live 清单整体替换静态清单 (失败或为空时保留当前清单, 日志 `[workbuddy] live model list applied: N models`)

## Sub-features

- `models-serve` 宿主对外接口包含插件当前生效清单的模型: 无凭据时是静态清单, workbuddy 在完成扫码登录或触发凭据刷新后切换为 live 清单 (仅 `auth.parse` 载入凭据本身不触发远端拉取)
- `models-blacklist` workbuddy 只对 live 清单做运行时过滤: 先按能力剔除非 chat 模型 (disabled、视频/媒体、缺上下文窗口、不支持工具调用), 再查 ID 黑名单 (补全模型、路由别名、旧模型、渠道副本等); 静态清单已由生成器过滤, 映射时不再丢项。qwenworkcn 全量直报, 无此项
- `models-metadata` 上下文长度与输出上限与当前生效清单一致 (workbuddy live 生效后取 live 的 `maxInputTokens/maxOutputTokens`)

## 断言子集

`dev-sandbox.go --checks models` 覆盖 `models-serve` 的「声明的必须都在」方向 (改了静态清单或注册过滤后点名它, 常与 `load` 同跑):

```bash
go run scripts/dev-sandbox.go -plugin <id> -checks load,models
```

比对规则是「裸 id 或 `<插件 id>/<model>` 命中其一即算在列」; 插件无静态清单时跳过比对 (不算失败)。它只比对**静态清单**: 无凭据沙箱里就是全部清单, 不能当作 live 清单正确性的断言。不支持自定义 `model-prefix` (该形态下必然报缺), 也不检查前缀开关的精确结果; 多插件共用宿主时, 另一个插件的同名裸 id 也能让比对通过。反向方向不覆盖: 断言只判「声明的没少」, 不判「该剔除的没多」, 黑名单的负面判据见下方 Driving it 与插件单测。有任一缺失时报错文案为 `插件 <id> 的静态清单声明了 N 个模型, 但 /v1/models 少了 M 个: <名单>`。断言按注册顺序在各插件间 fail-fast: 前一个插件报错即终止后续插件比对。

多插件共生时一个宿主只拉一次 `/v1/models`, 各插件分别比对; 断言名与描述以 `--checks list` 为准。

`models-metadata` 无脚本断言, 按下方 Driving it 的对照法人工核。

## How to get to it (user POV)

- 客户端配置宿主地址后获取 `/v1/models` 列表
- 客户端模型下拉框直接看到该渠道的模型名

## Driving it

Preconditions:

- 插件已启用 (`effective_enabled` 为 `true`)
- 用 `management-api.go -auth client` 访问 `/v1/models`: 沙箱须传 `-token-file ~/.cache/cpa-plugins/sandbox/<id>/management-key` (脚本启动时先读管理密钥以查询 `api-keys`, 漏传会去读生产缓存报 401 或退出码 3)

- **核对在列模型。** 拉取完整模型集合, 无凭据时须包含 `data/static-config.json` 声明的全部模型项。id 形态由插件配置决定: 默认 `<插件 id>/<model>`; `enable-model-prefix: false` 为裸 id; 设置 `model-prefix` 时为 `<model-prefix>/<model>`。局部切换前缀开关用 `-method PATCH` (不要用 `PUT`, `PUT` 整体替换会抹掉 `enabled:true` 导致卸载), 异步重载后数秒内 `/v1/models` 即换成新形态:
  ```bash
  go run scripts/management-api.go -token-file ~/.cache/cpa-plugins/sandbox/<id>/management-key \
    -auth client -path /v1/models | jq -r '.data[].id'
  ```

- **验证黑名单过滤。** 先确认宿主日志已打印 `[workbuddy] live model list applied: N models` (未触发刷新时测到的只是静态清单), 被剔除的模型不得对外暴露, 预期无输出:
  ```bash
  go run scripts/management-api.go -token-file ~/.cache/cpa-plugins/sandbox/workbuddy/management-key \
    -auth client -path /v1/models \
    | jq -r '.data[].id' | grep -E '(^|/)(codewise-completions|hunyuan-3b|kling-video-v3|deepseek-v3-0324)$'
  ```
  workbuddy 的黑名单与能力过滤规则见 `plugins/workbuddy/models.go` 的 `excludeModelReasons` 与 `nonChatReason` (其余插件无此过滤, 直报全量); 名单随官方客户端版本变化, 以代码为准
- **核对元数据。** `/v1/models` 只返回 id 与归属, 上下文长度等元数据以清单与单测为准: 对比 `data/static-config.json` 中该模型的声明, 并跑 `cd plugins/<id> && go test ./...` 中钉住元数据映射的用例 (qwenworkcn 的 `TestFilterAndMapModels` 直接断清单里的 `contextLength`/`maxCompletionTokens`; workbuddy 的 `TestMapManifestModels` 用自建样例, 只断 id、推理与输入模态, 不断长度字段; live 优先由 `TestCurrentModelsPrefersLive` 钉住)

## Gotchas

- 客户端模型列表看不到插件模型 -> 检查宿主配置 `plugins.configs.<id>.enabled` 是否为 `true`
- 静态配置已改但模型列表未更新 -> 沙箱宿主下静态清单经 Go embed 编译固化, 改完须重编动态库; 若 workbuddy 已拉取过 `liveModels`, 还需重启进程清内存缓存
- 反查静态清单时注意剥离渠道前缀 -> `/v1/models` 暴露 `<plugin>/<model>`, 静态清单存裸 id, 历史上未剥通用前缀导致反查 miss (postmortems/005)
