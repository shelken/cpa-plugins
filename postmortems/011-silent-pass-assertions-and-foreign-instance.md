# 沙箱断言恒真, 且就绪判定让断言打到陌生实例上

**日期**: 2026-09-17
**影响**: 验证技能的 `config` / `quota` 判据实际没有否定路径, 管理面不可达、插件没实现、注册被改坏三种情况都报「通过」; `resource` 断言把路径写死, 对注册了别的 resource 路径的插件直接失败。另有一次实跑: 上一轮 `-keep` 的宿主占住 18317, 本轮沙箱判定「就绪」后把全部断言打到那个陌生实例上, 报出与本次插件无关的失败结论
**发现人**: 复查最近提交时的只读审计 (静态缺陷) + 复核实跑 (陌生实例事故)

## 问题

`dev-sandbox.go` 的六个断言里, 有四个的判据不成立:

- `checkQuota` / `checkConfig` 的任何分支都 `return nil`: 请求失败、非 200、JSON 解析失败、列表里没有该插件, 一律当成通过并继续。它们被注册为「断言」, 输出里也出现「断言通过」, 但实际无法判失败
- `checkResource` 把路径硬编码成 `/v0/resource/plugins/<id>/quota`, 而 resource 路径是插件自己声明的 (`ResourceRoute.Path`), 换一个插件就失效
- `startHost` 的就绪判据只看宿主日志里出现 `API server started successfully`; 该行在 bind **之前**打印, 端口被占时它照样出现, 进程随后退出。退出检测用 `command.ProcessState`, 而 `ProcessState` 只在 `Wait` 之后才有值, 等于永不触发。于是本轮沙箱「就绪」了, 断言全部发给端口上那个陌生宿主

## 现象

硬编码路径: 对注册 `/status` 的插件跑 resource 断言, 报 404 并终止整轮:

```bash
go run scripts/dev-sandbox.go -plugin echo-probe -checks all
# [-] GET /v0/resource/plugins/echo-probe/quota 返回 404:
```

陌生实例: 另一轮 `-keep` 的宿主残留占着 18317, 本轮三插件沙箱照常「就绪」, 断言对象却是残留宿主 (它只跑 echo-probe + qwenworkcn, `/v1/models` 只有 3 个模型):

```bash
go run scripts/dev-sandbox.go -plugin echo-probe -plugin qwenworkcn -plugin workbuddy -checks all
# [+] 断言通过: qwenworkcn, /v1/models 返回 3 个模型, 清单声明的 3 个全部在列
# [-] 插件 workbuddy 的静态清单声明了 21 个模型, 但 /v1/models 少了 21 个: deepseek-v4-flash, ...
# --- 宿主日志尾部 ---
# API server started successfully on: :18317
# [error] proxy service exited with error: failed to start HTTP server: listen tcp :18317: bind: address already in use
```

结论是「workbuddy 少了 21 个模型」, 而 workbuddy 根本没进那个宿主 —— 失败指向了错误的插件。

## 根因

- **错误假设 1**: 用返回值表达「这条断言不适用」。插件没声明 QuotaProvider / ConfigFields 时确实不该判失败, 但这个「不适用」被写成了 `return nil`, 顺手把传输错误、非 200、解析失败一起吞了。断言因此失去否定路径, 输出仍然是「通过」
- **错误假设 2**: 就绪 = 日志出现 `API server started successfully`。实际约束是这一行早于 bind, 端口冲突时它必然出现; 而「进程是否还活着」的判据用了只有 `Wait` 之后才被赋值的 `ProcessState`, 永远不会为真
- **缺失检查点**: 断言之前没有一次「用本轮密钥确认应答者身份」的动作, 启动之前也没有端口空闲检查。`resource` 的路径没有从插件声明派生, 因此只对第一个插件成立

## 修复

- 断言改成双边比对: `config` 读插件源码里声明的 `ConfigFields`, `quota` 读宿主回报的 `supports_quota`, 再与注册结果比对。未声明打印 `[i] ... 跳过 (不判失败)`, 声明了却对不上才失败, 传输/状态码/解析错误直接报错
- `resource` 从管理面回报的菜单路径取值 (即插件自己声明的 `ResourceRoute.Path`), 支持任意路径与多个 resource 页面
- 启动前预检端口占用, 被占直接报错退出; 就绪后追加 `verifyOwnership()`: 用本次沙箱密钥请求 `/v0/management/plugins`, 非 200 即判定「占端口应答的不是本次启动的宿主」; 进程退出检测改由 `startHost` 里唯一的 `Wait` 通道承担, `stop()` 复用同一通道
- 对话链路补 `guard` 场景, 给「只接受宿主已报送的模型 id」补上判据 (未知 id 须在路由阶段被拒 `400 model_not_found`; 对照: 合法模型在同一无凭据沙箱会走到 `503 auth_not_found`, 两者处理阶段不同)
- 验证: 三插件 × 6 断言全过 (`echo-probe` 首次纳入); 有残留宿主时脚本直接拒绝启动; `guard` 用真实模型反向跑出 FAIL

## 预防

- 断言函数禁止无条件 `return nil`; 「不适用」必须打印跳过标记, 且与失败在输出上可区分
- 就绪判据不能只看日志行; 启动后必须用本轮凭据访问一次目标接口确认归属
- 断言前检查端口占用 (`lsof -nP -iTCP:<port> -sTCP:LISTEN`), 被占先杀残留, 不改端口绕过
- 硬编码路径/端点的断言必须从插件声明派生 (菜单、清单、注册元数据), 否则只对第一个插件成立
- 用 `-keep` 起沙箱的轮次必须在同轮清理: `pkill -f "cliproxyapi -config.*sandbox/"`
