# WorkBuddy 额度查询走插件自有管理页

上游管理面板把额度渲染硬编码为几家 provider，插件上报的 `supports_quota` 与宿主 `/v0/management/quota/fetch` 都不被面板消费，workbuddy 凭据在面板上看不到额度。宿主已提供完整的管理页机制（`ManagementAPI` capability + `/v0/resource/plugins/<id>/`），插件可以自己补上这块 UI，不必等上游面板改。

## Status

accepted

## Considered Options

- 等上游面板 PR 合入：治理最干净，但排期不可控，用户在此期间一直看不到额度
- 面板侧 fork 维护：维护成本最高，宿主升级后要持续跟
- 插件注册 resource 页面：改动封闭在本插件内，宿主与面板都不用动，社区插件已有先例

## Consequences

- 插件声明 `management_api` capability，注册 resource 页面 `WorkBuddy Quota`，宿主把它挂到 `/v0/resource/plugins/workbuddy/quota`，面板侧边栏自动出现菜单（iframe 无 sandbox，同源可读面板 localStorage 里的管理密钥）
- 页面数据全部走宿主管理 API：`GET /v0/management/auth-files` 列凭据，`POST /v0/management/plugins/workbuddy/quota` 查额度，凭据密钥不落前端
- 面板 localStorage 格式属于面板私有实现，页面读取失败时降级为手贴管理密钥输入框
- 若日后上游面板开始消费 `supports_quota`，此页面与面板原生渲染并存不冲突；是否移除届时再议
