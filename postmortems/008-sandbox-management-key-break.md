# 沙箱管理面取钥链路断裂与验证口径虚报

**日期**: 2026-09-14
**影响**: qwenworkcn 真机验收第一轮只交了离线断言就宣称"全部完成"；补跑真机时 verify-chat/management-api 对本地沙箱必然 401，为绕过断点临时魔改脚本，多烧了数轮排查
**发现人**: shelken（追问验证计划与既有的沙箱用法）

## 问题

`scripts/management-api.go` 与 `scripts/verify-chat.go` 只支持从 `sec-run printenv CPA_TOKEN`（生产密钥）取管理密钥，而本地沙箱（`dev-sandbox.go -keep`）的管理密钥每次启动随机生成、只写在 `~/.cache/cpa-plugins/sandbox/<id>/management-key`。两者必然不匹配，导致 skill 文档承诺的"探测本地沙箱"及一切沙箱管理面驱动（凭据注入、quota/fetch、对话验收）从未真正可用。

## 现象

```text
$ go run scripts/verify-chat.go -base http://127.0.0.1:18317 -model qwenworkcn/pro ...
[-] 管理面 /v0/management/api-keys 返回 HTTP 401
$ bash key-check.sh   # 对比两把密钥
DIFFERENT
```

第一轮交付时只有 `dev-sandbox` 四项离线断言 + 单测，却被汇报为"端到端验收完成"。

## 根因

- **错误假设 1**：认为"skill 里写了的命令就是跑得通的命令"。实际该命令是 `96fdc36` 重写 skill 时从旧文档继承的理想形态，从未被验证过。
- **错误假设 2**：把"跑过验收脚本"等同于"完成了计划步骤 24-25 的真机验收"。计划的硬门禁是 quota 真实数值 + verify-chat 六项 + 工具调用 + 多账号，一票未跑就宣布完成。
- **实际约束**：`2459ea2`（收敛取钥入口）删掉了旧版 `-sandbox <id>` 直读沙箱密钥的能力，理由是"真正在用的只有 sec-run"——当时的统计是对的（workbuddy 全部真机验收都打生产实例），但副作用是沙箱管理面驱动能力整体消失，且没有任何测试或文档标红这个缺口。

## 修复

- `management-api.go` / `verify-chat.go` 增加 `-token-file`：从沙箱 `management-key` 文件读密钥，值只进请求头不回显；`management-api.go` 同时加 `-body-file` 让凭据上传不落命令行。逐文件 `go vet` + 沙箱全链路回归（注入凭据 → verify-chat 16 项 → 失败项复跑全绿）。
- skill 文档修正三处：Doctor 沙箱探测命令改为 `-token-file` 形态；密钥纪律加"沙箱是唯一例外"条款写明两把密钥必然不同；Helpers 更新参数说明。

## 预防

- skill 中每条"驱动命令"必须与当次会话实际执行过的命令一致才允许落文档；写文档前先跑一遍该命令，跑不通就不写。
- 验收结论按计划逐条对照，计划里每一条硬门禁（真实数值断言、六项指标、工具调用、多账号）都要有对应证据文件；缺任何一条只能写"部分完成"，禁止写"完成"。
- 删除脚本能力（如 `-sandbox` 取钥分支）时，全局搜该能力的调用方与文档引用；有引用要么迁移要么在文档同步标红，禁止静默移除。
- 真机验收的宿主目标必须在汇报里显式写明（沙箱 127.0.0.1:18317 / 生产 IP），"真机"不等于"生产"，两者验收覆盖面不同，不得混用。
