# 额度查询

管理面板提供各渠道实时配额查询; 两个插件的额度查询仍用插件本地 HTTP 客户端 (45 秒超时), 不走宿主 HTTP 回调, 对话链路的下游取消语义不适用于额度

## Sub-features

- `quota-providers` 宿主识别具备额度查询能力的已注册插件
- `quota-fetch` 通过管理接口拉取实时余额并返回格式化比例
- `quota-checkin` workbuddy 额度页支持单卡与全部账号手动签到 (`enable-checkin` 默认开启, 5 秒独立超时, 固定桌面档 `checkin` 头)

## 断言子集

`dev-sandbox.go --checks quota` 断言「插件声明的额度能力」与「宿主注册的额度提供方」一致: 插件声明了 `QuotaProvider` 却不在提供方列表即失败, 未声明却混入也失败, 未声明的插件跳过 (改了 QuotaProvider 注册后点名它):

```bash
go run scripts/dev-sandbox.go -plugin <id> -checks load,quota
```

真实余额数值必须走下方 Driving it 的 `quota/fetch`, 沙箱离线断言不覆盖。

**判据陷阱**: 判「额度有没有被消耗」必须读上游原始数值, 不能看管理面文案。文案是插件用取整格式拼的 (如 `余额 %.0f credits` 把 99.5675 显示成 100)。钱包型渠道没有总量上限, `remainingFraction` 由插件写死为 `1.0`, 根本不反映消耗; 有总量的渠道按各插件算法给比例 (workbuddy 先把 used/limit 舍入到六位小数再算, qwenworkcn 直接 `remaining/total`, 都夹在 0~1)。判断消耗一律回到 `/user/wallets` 的 `balance` 等上游原始字段。

**接口契约**: 宿主内建两个额度管理路由:
1. `POST /v0/management/quota/fetch`: `auth_index` 必填 (`400 auth_index is required`); 凭据不存在返 `404 auth not found`; 渠道未实现额度返 `501`; 插件执行失败返 `502`。
2. `POST /v0/management/plugins/<id>/quota` (各插件自带额度页的数据源): 先按 `auth_index` 查凭据 (不存在返 `404 auth not found`), 凭据存在但目标插件未启用或未实现 `QuotaProvider` 时返回 **`404 quota provider not found for plugin`**。
此外, `workbuddy` 注册插件私有管理路由 `POST /v0/management/plugins/workbuddy/checkin`: `enable-checkin: false` 时返 `403 {"error":"签到功能已关闭"}` 且页面注入 `data-checkin-enabled="false"`; 开启时对真实账号执行当日签到, 返回 `200 {"status":"claimed",...}` 或 `{"status":"already_claimed"}`。

## How to get to it (user POV)

- 各插件自带额度页: 管理面侧边栏菜单 `WorkBuddy Quota` / `QwenWork Quota`, 页面路径 `/v0/resource/plugins/<id>/quota`, 数据源为宿主路由 `POST /v0/management/plugins/<id>/quota` (`workbuddy` 另提供签到按钮调 `POST /v0/management/plugins/workbuddy/checkin`)
- 管理面接口 `GET /v0/management/quota/providers` 与 `POST /v0/management/quota/fetch`

## Driving it

Preconditions:

- 宿主与插件 SDK 同属 v8, 本地沙箱装载成功且额度提供方已注册 (`dev-sandbox.go -checks quota`)
- 插件已启用且 `supports_quota` 为 `true`, 沙箱管理密钥传 `-token-file ~/.cache/cpa-plugins/sandbox/<ids>/management-key`
- 沙箱每次启动都会清空 `auth/`, 测真实余额前须在宿主就绪后用 `scripts/copy-auth.go` 搬入 `status: active` 凭据 (postmortems/003)

- **查询额度提供方。** 确认宿主识别了插件的额度扩展点, 预期 `HTTP 200` 且列表含目标插件条目:
  ```bash
  go run scripts/management-api.go -token-file ~/.cache/cpa-plugins/sandbox/<ids>/management-key \
    -path /v0/management/quota/providers
  ```

- **拉取实时额度。** 按凭据索引取实时余额, 输出按分组给出剩余比例, JSON 字段为 `remainingFraction`。`auth_index` 的值取 `auth-files` 条目的 `auth_index` 字段 (16 个十六进制字符, 如 `27f747038b35e8c7`), 不是文件 id; `[host]`/`[res]` 两行打在 stderr, 不影响 jq:
  ```bash
  KEY=~/.cache/cpa-plugins/sandbox/<id>/management-key
  AUTH_INDEX=$(go run scripts/management-api.go -token-file "$KEY" -path /v0/management/auth-files \
    | jq -r '[.files[] | select(.provider=="<id>" and .status=="active")][0].auth_index')
  go run scripts/management-api.go -token-file "$KEY" -path /v0/management/quota/fetch -method POST -body "{\"auth_index\":\"$AUTH_INDEX\"}"
  go run scripts/management-api.go -token-file "$KEY" -path /v0/management/plugins/<id>/quota -method POST -body "{\"auth_index\":\"$AUTH_INDEX\"}"
  ```
  断言 `HTTP 200` 且返回与该账号实际余额量级一致的数值, 记录该数值供后续验收对照。账号确实耗尽时返回 0 是合法结果, 要与查询失败 (非 200) 区分; 只有事先确认账号有余额时才断言为正

- **验证签到开关门禁 (`workbuddy`)。** 不消耗真实签到次数时, 先 `PATCH {"enable-checkin":false}` 关闭开关, 确认 `/v0/resource/plugins/workbuddy/quota` 含 `data-checkin-enabled="false"` 且 `POST /v0/management/plugins/workbuddy/checkin` 返回 `403`, 测完恢复原配置

## Gotchas

- 额度接口 404 -> 先看响应体: `auth not found` 是 `auth_index` 不存在 (取错字段或凭据已删); `quota provider not found for plugin` 是 `/plugins/<id>/quota` 的目标插件未启用或未实现额度; 路由级 404 才是宿主没有额度路由
- 额度查询返回 502 -> 宿主把插件额度查询的任何错误统一映射为 502, 先读错误正文: 网络错误、上游 401/非 200、数值反序列化失败 (上游部分精确度数值带字符串格式) 都会落在这里
- 多账号场景按凭据逐条 fetch, 两条凭据各自返回余额且互不影响 (验收清单硬门禁)
