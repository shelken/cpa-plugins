# 同批推多插件标签触发 record 并发回填竞态, 哈希互相覆盖丢失

**日期**: 2026-09-16
**影响**: workbuddy 0.2.2 与 qwenworkcn 0.1.2 同时发版, registry.json 中两插件 sha256 曾全部为空, 版本条目丢失; 反复补救后又因远端重置丢失全部 bot 回填, 多轮返工
**发现人**: 用户

## 问题

`record` 作业从 `main` 分支 checkout 后用 `release.go record` + `build-registry.go` 全量重建 `registry.json` 并推回。两条 Release 流水线并发运行时, 各自基于过期 `main` 生成整文件, 后推者整文件覆盖先推者的回填; rebase 阶段撞 `registry.json` 冲突, 作业失败。补救期间远端 main 被重置回版本提交 (`88b6fbd`), 该提交只含 `plugin.json`/`main.go`, 未同步 `registry.json`, 两插件新版本哈希彻底丢失, Check Registry 在该树上实测红 (`Registry file registry.json is out of date`)。

## 现象

- 同批推送 `workbuddy/v0.2.2`、`qwenworkcn/v0.1.2` 两个 tag, 两条 Release run 均 success, 但 main 上只有一条 bot 回填
- 另一插件条目被静默覆盖: version 回退旧版、sha256 清空
- 重置后 `88b6fbd` 树上 Check Registry 红: registry.json 过期
- 本地 `build-registry.go --check` 报 out of date (与远端一致)

## 根因

- `record` 作业无跨流水线并发控制: read-modify-write `registry.json` 整文件, 并发写必然互相覆盖
- 失败恢复设计缺口: 只在文档写了「本地补齐」——这条路径本身违反「哈希只能由 CI record 作业产生」的不变量
- 整文件重建 + 无幂等重试: push 被拒即失败, 不基于最新 main 重新生成

## 修复

- `release-plugin.yml` record 作业加 `concurrency: group: release-record-main, cancel-in-progress: false` 跨流水线串行化 (f5fcc42)
- 回填步骤改为 5 次幂等重试循环: 每次尝试 `git fetch + reset --hard origin/main` 后重新下载产物、重新 record、重新 build-registry, push 被拒等 20s 重来 (f5fcc42)
- 被清掉的哈希用重打标签触发全新流水线回填: qwenworkcn 0.1.2 → 3125a09, workbuddy 0.2.2 → 38314d9, 二者均 success
- 文档纠偏 `docs/how-to/plugin-release.md`: 恢复指引从「本地补齐」改为重打标签; §4 加「多插件标签逐个推送, 等上一条流水线回填完成再推下一个」(b74b006)

## 预防

- 共享派生文件 (registry.json 这类聚合产物) 的 CI 写入点必须声明并发组; read-modify-write 循环必须幂等重试, 不允许一次 push 失败即整作业失败
- 多插件发版是串行流程, 不是并行流程; 批量推 tag 前先想清楚每个 tag 触发的作业写什么文件
- 回填类修复的唯一手段是重打标签触发全新流水线; 禁止 `gh run rerun`、禁止本地代填、禁止改写 main 既有状态
- 经打码通道取样凭据会注入 `<redacted len=N>` 字面量假值, 沙箱验证必须用进程内直注 (sec-run), 打码值一旦落进配置全部 401 且极难排查
