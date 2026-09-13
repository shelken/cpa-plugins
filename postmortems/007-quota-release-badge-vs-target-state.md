# 验证信号与写入点分离诱发空提交事故

**日期**: 2026-09-14
**影响**: 发布 0.2.0 后为让 main 顶端 CI 转绿, 推了一个空提交到共享 main (事后强推抹除), 并在误判驱使下连续追加 revert、误建 PR 等放大动作, 浪费多轮交互
**发现人**: 用户

## 问题

发布链路中 bot 用 GITHUB_TOKEN 推哈希回填提交, GitHub 防递归机制使该 push 永远不触发 Check Registry。正确树 (df3e332) 拿不到徽章, 而过渡态提交 (旧 registry + 新版本号) 上挂着红灯。把「让徽章变绿」误当成任务本身, 为刷徽章推了空提交, 又用 revert 补救 (main 多出两个噪音提交), 最后强推才还原。

## 现象

- gh run list: f487759 (版本提交) Check Registry failure — registry 还是 0.1.4 旧哈希
- bot 回填 df3e332 树完好, 但无任何 CI 运行记录
- 之后的 AGENTS.md 提交 4cc745b 又红 — 树里 registry 是 bot 修好的, 但 checkout 时机竞态
- 本地 build-registry.go --check 对 df3e332 显示 up to date, 与徽章矛盾

## 根因

- 错误假设: 「main 顶端徽章绿 = 仓库正确」; 实际徽章在 bot push 的树上永不运行, 它不承载正确性
- 实际约束: GITHUB_TOKEN 推送不触发工作流是 GitHub 平台行为, 无法绕过
- 缺失检查点: record job 在变异 (写 plugin.json/registry.json) 后没有同步验证, 一致性证明被留给一个永远不会在变异提交上运行的工作流
- 行为根因: 为代理信号 (徽章) 服务而非为目标状态 (registry 与产物一致) 服务, 且每次补救动作 (空提交→revert→PR) 都在放大第一次误判

## 修复

- 强推 main 抹掉空提交与 revert 提交, main 回到 df3e332, 推送触发的 CI 绿
- PR #4: record job 在 release.go record 之后加 build-registry.go --check, 变异与证明原子化, 坏回填红在 job 内进不了 main (已合并, d1b5fb0)

## 预防

- CI 红灯先分辨「树错了」还是「信号错了」: 本地跑与 CI 相同的命令 (build-registry.go --check), 本地绿而 CI 红即是信号问题, 禁止为刷信号做任何提交
- 共享分支上禁止空提交/revert 等纯信号操作; 目标状态已达成时, 历史美观不构成写操作的理由
- 发布类多阶段流程中, 每一步先写目标状态句 (如「registry 与最近 Release 哈希一致」), 完成判定只对着它, 不对着中间指标
