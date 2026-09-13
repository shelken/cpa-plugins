# 注释声明被测试钉死成错误形状：额度请求体与抓包漂移

**日期**: 2026-09-14
**影响**: 生产环境 workbuddy 凭据额度查询持续 502（上游 400），面板上看不到额度；错误实现被既有测试当作契约保护着，红绿修复前无法直接改
**发现人**: 主代理（用户报障后排查）

## 问题

`plugins/workbuddy/quota.go` 的额度请求体与桌面客户端真实抓包不符：全部字段发成字符串、多了 `OnlyValidPeriod`。上游直接 400。更要命的是 `executor_test.go` 里的 `TestQuotaRequestPayloadMatchesDesktopClient` 把这个错误形状当成「桌面客户端形状」钉死了——注释声称形状来自客户端，测试则保证任何向正确形状的修改都编译失败/断言失败。

002 号报告（伪占位哈希）的教训是「清单声明未经真实产物验证」，本例是同款漂移的另一个变体：**注释声明未经抓包验证，且测试反向锁定了错误**。

## 现象

```text
POST /v0/management/quota/fetch  (auth_index=e4ca8c094ddd7da2)
→ HTTP 502
{"error":"failed to fetch quota: ... 400 Bad Request: cannot unmarshal
string into Go struct field ResourceDetailsRequest.OnlyValidPeriod of type bool"}
```

而抓包 (`data/static-config.json` `requestBodies.userResource`, 客户端 5.3.14) 是：

```json
{"PageNumber":1,"PageSize":100,"ProductCode":"p_tcaca","Status":[0,3]}
```

改测试期望为抓包形状后，`go test .` 编译失败：

```text
./executor_test.go:233:16: cannot use 1 (untyped int constant) as string value
```

## 根因

- 错误假设：写实现时认为「上游接受字符串化的参数」（对部分国内 API 成立），且认为带上 `OnlyValidPeriod` 更安全；没有用抓包逐字段核对。
- 实际约束：上游对该端点按 Go 结构体严格反序列化，数字/bool 字段不接受字符串。
- 缺失检查点：测试名字与注释都写着「与桌面客户端一致」，但断言内容只是「与实现一致」。测试的期望值是从实现抄来的，不是从抓包抄来的，于是测试从「契约校验器」退化成「现状锁定器」。

## 修复

- 测试期望改为抓包形状（红：编译失败证明现有实现与新期望互斥），再改 `quotaRequestPayload` 为 `int`/`[]int` 字段并删 `OnlyValidPeriod`（绿）。
- 注释改写为可追溯来源：指向 `data/static-config.json` `requestBodies.userResource` 与客户端版本，不再使用「客户端不发送时间过滤」这类无出处的描述。

## 预防

- 新增/修改协议字段时，测试期望值必须从抓包文件逐字段抄写，禁止从实现代码反向抄；期望值旁注明抓包来源（文件路径 + 客户端版本）。
- 形状类测试（marshal 断言）在合入前先做一次反向验证：故意把实现改错，确认测试真的红；抄实现而来的断言一律视为现状锁定器。
- 「真实客户端发什么」的唯一权威是抓包（`static-config.json` 的 `provenance` 字段标注版本），任何注释、测试、文档与抓包冲突时以抓包为准并当场修正。
