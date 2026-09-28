# captcha F017：goja 移植放弃记录

**日期**: 2026-09-27 ~ 09-29（三个 session）
**结论**: 彻底放弃 goja 移植 captcha 求解，P0.4 改用 zcode-api 的 HD 求解器做 sidecar。

## 为什么放弃

在 Go（goja + 手写 ~1400 行 DOM 桩）里跑阿里 feilin 反爬 SDK，对抗的是**主动反模拟、每周轮换版本**（feilin029→031 调试中途轮换）的混淆目标。三天里先后定位过八个"根因"（探针污染→TrackList 空→combat 空→节流器→isTrusted→泵饥饿→静止检测→PREID 配置块），全部被后续验证推翻——不是我们查错了，是**架构错配**：happy-dom+bun 的环境保真度是多年积累，goja 手写桩补不完 100+ 个环境探测点。

决定性对照：给 HD 侧装 stringify 钩子后确认，**HD 成功路径的 combat 六桶同样全空**——combat 空、样本少、节流、静止期都不是 F017 的原因，服务端在 combat 空时也放行（PREID 宽容通道）。唯一稳定的硬差异是**令牌形态**：垫片 `SG_WEB#<sessionId>#<enc>`（完整注册通道，严格校验）vs HD `SG_WEB_PREID#...`（PREID 宽容通道）。PREID 分支的决策输入没有读完，但读完也不改变结论——版本再轮换，打地鼠重新开始。

## 唯一可复用的方法（若将来重启）

feilin 的串表解密可离线静态重建，不需要跑运行时猜：

- 每个混淆模块 = 字符串数组（密文，按 `e[r-=K]` 索引）+ 65 字节字母表（模块内 hex 串）+ 自定义 base64 变体 `LA`，输出字节再 XOR 一个**每模块独立常量**（029 的 39 个模块常量分布 1~249）。
- Python 复刻约 40 行：`alpha.index(xc ^ ord(ch))` 做 4 字符→3 字节组包，`(255 & (i >> ((-2*u) & 6))) ^ key` 输出，尾部 UTF-8 解码。
- 密钥对来源：跑一次运行时记录解码串（vm.str 钩子），对每条密文暴破 key 0..255，命中已知串即配对（029 实现 2243/2530 配对）。
- 调用点带反调试伪装：`tL(~tL&&K,~tL&&R)`、`K&~tL`、`[tL][0](K,R)`、`tL.bind(7,K,R)()`、`K..valueOf()`，包装器 `function tL(t,r){return ro(r-3,t)}` 即 **tL(key, r+3)**。
- 注意版本轮换：031 与 029 偏移/常量不同，需同法重建。

## P0.4 的新方向

插件（Go）通过 HTTP/UDS 调 zcode-api 的 HD 求解器（`solveTraceless`，production-proven）。薄壳一层，难的部分留在已验证的栈里。HD 偶发失败（版本轮换期 4.4s 成功 / 12-22s degraded 交替）是服务端波动，不是实现问题。

## 已清理

`plugins/zcode`、`docs/adr/plugins/zcode`、`docs/plans/zcode-provider-port.md`、CONTEXT.md 相关术语、根目录散落的 shim 测试、全部 /tmp 分析产物。zcode-api 参考仓库已 git checkout 还原。
