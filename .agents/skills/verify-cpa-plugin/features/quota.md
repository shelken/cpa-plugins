# 额度

用户视角：管理面板能看到这个渠道的套餐余量，而不是一个空白或报错。

## Sub-features

- `quota-declare` 宿主把插件列进额度提供方
- `quota-fetch` 拉取实时额度并换算成剩余比例
- `quota-offline` 无凭据时用离线夹具验证解析，不依赖账号
- `quota-shape` 服务端把 `*Precise` 字段返回为字符串时仍能解析

## How to get to it (user POV)

- 管理面板的额度页
- `GET /v0/management/quota/providers` 看声明，`POST /v0/management/quota/fetch` 拉实数

## Driving it with the management API

Preconditions:

- 沙箱已就绪，管理面命令用 `go run scripts/management-api.go -sandbox workbuddy -path <路径>`

- **能力声明（无凭据可跑）。** `go run scripts/management-api.go -sandbox workbuddy -path /v0/management/quota/providers | jq -c '.'`。列表里有本插件，`display_name` 与 `supported_providers` 符合预期。沙箱的第三条断言已经在做这件事
- **拉取实数（凭据档）。** 先从 `auth-files` 取 `auth_index`，再 `go run scripts/management-api.go -sandbox workbuddy -method POST -path /v0/management/quota/fetch -body '{"auth_index":"<从 auth-files 取>"}' | jq -c '.'`。返回 200，且 `RemainingFraction` 落在 0 到 1 之间
- **字符串形态（离线，优先用这条）。** 服务端会把所有 `*Precise` 后缀字段返回成 JSON 字符串（`"500"`、`"499.35"`），非 Precise 的同名字段是数字，同一接口在不同账号下两种形态都可能出现。构造一份字符串形态的响应作为夹具，断言解析成功且剩余比例正确：`cd plugins/workbuddy && go test ./...` 里的额度用例就是这条夹具
- **请求体形状。** 额度请求体五个字段全部用字符串发送，且不发送时间范围过滤。改动后跑 `bun run scripts/audit-traffic-diff.ts --session WorkBuddy_20260913_174259.json`，额度接口不应有 `Missing` 项

## Gotchas

- 宿主低于 `v7.2.159` 时额度能力完全不存在：`supports_quota` 为 `null`，`quota/providers` 返回 404。这不是插件缺陷，升级宿主即可，插件无需改动。实测见 `~/.cache/cpa-plugins/evidence/workbuddy/2026-09-13-host-compat/`
- 缺 `*Precise` 字段的字符串处理会直接 `502`，错误形如 `cannot unmarshal string into Go struct field ... of type float64`。这类缺陷与账号无关，一律用离线夹具修与验收，不要去线上试
- 多发时间范围过滤条件（`PackageEndTimeRangeBegin` / `End`）会导致按范围裁剪套餐、少算额度。字段只在抓包里发的时候才发
- 无凭据时只能验声明与夹具。不要把「声明在列」说成「额度能拉」
- 账号不可用时额度拉取失败属于账号问题，不应记成插件缺陷
