# QwenWork 额度查询走插件自有管理页

上游管理面板（WebUI）把额度渲染硬编码为几家特定 provider，插件上报的 `QuotaProvider` capability 与宿主通用额度端点都不被管理面板消费，导致 QwenWork 凭据在面板上无法查看额度与钱包余额。基于宿主提供的完整管理页机制（`ManagementAPI` capability + `/v0/resource/plugins/<id>/`），插件内嵌独立的额度查询管理页，直接解决面板侧无额度界面的问题，无需等待上游面板改动。

## Status

accepted

## Considered Options

- 等上游面板 PR 合入：治理最干净，但排期不可控，用户在此期间一直看不到额度。
- 面板侧 fork 维护：维护成本高，宿主升级后难以持续跟进。
- 插件注册 resource 页面（选定）：改动封闭在本插件内，利用宿主已有的 ManagementAPI 能力就地提供 UI，复用 workbuddy ADR-0008 确立的自备页面模式。

## Consequences

- 插件声明 `management_api` capability，注册 resource 页面路由 `/quota`（菜单项 `QwenWork Quota`），宿主挂载至 `/v0/resource/plugins/qwenworkcn/quota`，面板侧边栏自动出现对应菜单项。
- 页面静态资源 `data/quota-page.html` 内嵌进插件动态库，GET 请求直接输出。页面与面板同源（iframe 无 sandbox），可自动读取面板 localStorage 中的管理密钥（含 `enc::v1::` 异或解码），读取失败时降级为手动输入管理密钥。
- 页面数据全走宿主管理 API：`GET /v0/management/auth-files` 筛选 `qwenworkcn` 凭据，再调用额度端点查询各凭据的钱包（Wallets）与用量（Usage）明细，凭据敏感密钥不落前端存储。
- 若日后上游面板开始消费 `QuotaProvider`，此页面与面板原生渲染并存不冲突；是否移除届时再议。
