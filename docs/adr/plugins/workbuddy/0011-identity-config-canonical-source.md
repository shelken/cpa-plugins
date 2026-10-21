# 伪装配置权威源上移到生成器,Go 侧只留渲染引擎

workbuddy 插件的伪装字段(请求头模板、动态值、user 消息簿记注入)曾以 Go 硬编码 + 产物 JSON 各存一份,两处会漂移且产物不可手改(staticctl 会覆盖)。现决策:全部伪装配置收敛到上游生成器的规范源 `{pi-codebuddy-provider}` `src/api/static-config.json` 的 `cpaPlugins.workbuddy` 段,经 `staticctl` 透传为插件产物;插件仓 Go 代码不含任何具体头名,只实现渲染引擎。决策依据与抓包证据见 `{pi-codebuddy-provider}` `docs/codebuddy-api/workbuddy-desktop-chat-fields.md`。

## Considered Options

- 继续双份维护(Go 硬编码 + 产物 JSON):改一处忘另一处,漂移已实际发生(`identityHeaders` 解析后无消费者)。
- 只在插件仓产物 JSON 手改:产物是生成器输出,手改会被下次同步覆盖。
- 规范源放生成器输入侧(采用):伪装策略随协议逆向演进,逆向工具与抓包在 `{pi-codebuddy-provider}`,策略变更改规范源后一条命令同步两仓。

## Consequences

- values 声明语义成为两仓契约:`{"from":"credential"|"credentialToken"|"manifest"}` 直取,`{"derive":"uuid"|"hex32","salt":"…","scope":"session"|"request"}` 派生;scope=session 用 CanonicalSessionID 确定性派生,request 每请求掺随机熵。改语义需两仓同版本发布。
- 未知 `{{占位符}}` 在 manifest 解析时 fail-fast,配置错误启动即暴露,不静默发残缺头。
- 按官方 5.8.3 抓包(2026-10-21)对齐:新增 `X-Agent-Type: main`,移除官方已不发的 `X-Root-Request-ID`,删除 `identityHeaders`;`prompt_cache_key` 与 `thinking:{type:'enabled'}` 确定不发(缓存命中 98%+ 佐证)。
- salt 以明文存于规范源,与旧算法取值完全一致; salt 泄露无独立危害(派生材料含服务端下发的会话 ID)。
