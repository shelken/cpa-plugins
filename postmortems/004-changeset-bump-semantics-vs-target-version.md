# 变更集 bump 语义与目标版本号冲突

**日期**: 2026-09-14
**影响**: workbuddy 0.1.4 发布，计划写 minor 首次运行产出 0.2.0 并已写入 plugin.json，需回退重跑
**发现人**: 执行发布命令后核对输出版本号

## 问题

计划拍板「bump 报 minor，0.1.3 -> 0.1.4」，两者语义矛盾：`release.go bumpVersion` 按语义化版本实现，minor 是次版本号加一（0.1.3 -> 0.2.0），patch 才是第三位加一（0.1.4）。按计划写 `{"bump":"minor"}` 执行 `go run scripts/release.go version` 后，脚本把 0.2.0 写进了 plugin.json、三处产物地址并清空旧哈希。人写下变更集的那一刻起，版本号就由脚本决定，「拍板了目标版本号」和「拍板了 bump 档位」只能留一个。

## 现象

```bash
go run scripts/release.go version --plugin workbuddy
# 新版本: 0.2.0
# 标签: workbuddy/v0.2.0
jq .version plugins/workbuddy/plugin.json   # "0.2.0"，而验收标准是 0.1.4
```

## 根因

错误假设：认为 minor 对应 0.1.x 内的升级（把「向后兼容的功能新增」理解成了第三位跳变）。实际约束：脚本忠实实现 semver，minor 加第二位。缺失检查点：跑 `version` 子命令前没有先心算一遍 bump 档位会落到哪个具体版本号。

## 修复

```bash
git checkout -- plugins/workbuddy/plugin.json   # 回退 0.2.0 的写入
# 重写变更集为 {"bump":"patch", ...} 后重跑
go run scripts/release.go version --plugin workbuddy
# 新版本: 0.1.4
```

## 预防

- 写变更集前先心算：目标版本号 = 当前版本按 bump 档位推一位，两者对不上时以目标版本号反推 bump 档位，不直接抄「改动重要性」
- `version` 子命令跑完第一眼核对打印出的版本号与预期标签，不一致立即 `git checkout -- plugins/<id>/plugin.json` 回退，变更集未提交前可随意改
