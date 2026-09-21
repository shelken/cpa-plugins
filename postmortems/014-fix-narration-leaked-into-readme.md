# 014 · 修复叙事与测试路径写进了用户 README

**日期**: 2026-09-21
**影响**: 两个插件 README 的「快速上手」段落里混进变更说明与内部测试文件名, 用户读到的产品文档变成变更日志残片
**发现人**: 用户指出

## 问题

修复凭据覆盖缺陷后, 我把「重新登录会替换旧 token、刷新周期如何计算」以及 `credential_merge_test.go` 写进了 `plugins/qwenworkcn/README.md` 与 `plugins/workbuddy/README.md` 的快速上手段落。workbuddy README 里更早有同类的版本修复叙述。这类内容属于过程性知识, 应当只出现在尸检报告, README 只描述当前能力与用法。

## 现象

```bash
grep -nE '回归位于|修复了|_test\.go' plugins/*/README.md
# plugins/qwenworkcn/README.md:28:...登录覆盖与刷新调度回归位于 `credential_merge_test.go`
# plugins/workbuddy/README.md:29:...登录覆盖与刷新调度回归位于 `credential_merge_test.go`
# plugins/workbuddy/README.md:75:另外，v0.2.0 修复了额度查询请求体与桌面客户端抓包不一致的问题...
```

## 根因

- 错误假设: 本次修复改变了用户可见行为, 所以要在 README 里说明。实际约束: 用户可见行为用陈述句描述即可, 「修了什么、哪条回归在哪个测试文件」属于过程性知识, 归 `postmortems/`
- 错误假设: 顺手补一句测试位置能帮后续维护者。实际约束: README 面向使用者, 测试文件路径会随重构失效, 是典型的易腐信息
- 缺失检查点: 动 README 之前没先查 `postmortems/` 是否才是该内容的归处, 也没检查 README 里是否已有同类陈旧叙述

## 修复

- 两个 README 删除修复叙事与测试文件路径, 相关内容并入 `013-plugin-credential-metadata-overwrite-and-refresh-gap.md`
- 清理 workbuddy README 里按版本号叙述的修复段与内部占位符, 改为描述当前能力
- 补齐 qwenworkcn README 缺失的额度页与插件联动说明, 与 workbuddy README 的章节结构对齐

## 预防

- 提交前跑 `grep -nE '回归位于|修复了|_test\.go|v[0-9]+\.[0-9]+\.[0-9]+' plugins/*/README.md`, 有命中就移到 `postmortems/`
- README 里禁止出现测试文件路径与版本号; 需要指向实现细节时用文件级 slug 或指向 `docs/`
- 修复完成后先问一句「这条知识归 README 还是尸检」, 只有描述当前行为与用法的句子留在 README
- 新增能力写进 README 时, 用陈述句写能力和默认值, 不写「之前是什么样」
