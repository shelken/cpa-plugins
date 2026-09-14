# 功能分支内合并主干

**日期**: 2026-09-14
**影响**: 功能分支历史被套娃 merge 污染, PR 提交数由 9 变 10; 回退只能改写已推送历史
**发现人**: shelken（「你认为功能分支应该merge吗!???????」「你他妈给我使用正确方式处理」）

## 问题

功能分支与主干冲突时用 `git merge origin/main` 同步, 把 upstream 历史与一个 merge 提交带进分支。做这件事时分支只在本地, `git rebase` 完全可用, 六分钟后才首次推送。

## 现象

```bash
git log --merges --oneline origin/main..HEAD | wc -l    # 1
gh pr view <pr> --json commits --jq '.commits | length' # 10
```

## 根因

- **错误假设**: 「先 merge 主干, 再在 merge 提交里解冲突」是同步主干的常规做法。实际约束是单作者、未评审、未推送的功能分支, 同步只该 rebase; merge 提交让分支在评审里读成掺入 upstream 的混合链路, 且回退只能靠改写已推送历史
- **错误假设**: main 上的 `Merge pull request #N` 说明「分支内要合主干」。那是 forge 合并 PR 时产生的提交, 与分支内同步无关
- **缺失检查点**: 没有一次「这条分支推送过没有」的检查。当时分支只在本地, 任何时候都能改判为 rebase

## 修复

- 先在临时克隆里排练再动真分支, 判据是内容零损失: `git rebase origin/main` 后线性 8 提交, merge 数 0, `git diff <旧 head> HEAD` 为空
- 冲突按内容归属解, 不按「一律取最终版」: `scripts/dev-sandbox.go` 取 main 版, `postmortems/README.md` 取合并后的完整索引, `features/chat.md` 取不含本轮新增行的版本
- 一律取最终版会让最后一个文档提交变成空补丁被丢掉, 要保留某提交的独立记录就得用它的意图版本
- 编号冲突按 main 已占用处理: 本轮新增的 006/007 重编号为 008/009, 并改写那条提交信息里残留的旧编号
- 落地用 `git push --force-with-lease`, 复查远端 merge 数为 0

## 预防

- 功能分支同步主干只用 `git rebase`; `git merge` 只出现在 forge 合并 PR 的那一刻
- 推送前跑 `git log --merges origin/main..HEAD`, 非空即停
- 已推送分支要回退历史, 先用 `--force-with-lease`, 并在临时克隆里排练到 `git diff <旧 head> HEAD` 为空再动真分支
- 重放提交遇冲突, 先问「这个提交的意图版本是什么」; 取错版本会把它变成空补丁静默丢弃
- 改写历史中某条提交信息用 `git checkout <commit>` 加 `git commit --amend` 再用 `git rebase --onto` 重放后代, 别用交互式编辑器的脚本钩子
