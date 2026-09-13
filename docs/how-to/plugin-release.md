# 插件发布

适用场景：改动了 `plugins/<id>/` 下的代码，要把新版本发布出去。六步，全部命令可复制。

前提：当前分支是 `main` 且已拉取最新。

## 1. 写变更集

在 `plugins/<id>/changesets/` 下新建 json 文件，文件名自取，建议带日期与主题：

```json
{"bump": "patch", "note": "一句话描述改动"}
```

`bump` 取 `patch`、`minor`、`major` 之一，多个变更集共存时取最高档。

## 2. 产出新版本

```bash
go run scripts/release.go version --plugin <id>
```

脚本同步源码里的版本字面量、`plugin.json` 的 `version` 与三处产物地址，清空旧哈希，删除已消费的变更集，并打印新版本与标签名。

## 3. 本地预检产物

```bash
go run scripts/release.go pack --plugin <id> --out dist
go run scripts/check-plugins.go
```

`pack` 只支持当前平台，产物按宿主契约命名并自检。此时 `sha256` 待回填属于预期警告，不要用 `--release-ready`。

## 4. 提交并推标签

```bash
git add plugins/<id> && git commit
git tag <id>/v<X.Y.Z> && git push origin main <id>/v<X.Y.Z>
```

标签版本号必须与 `plugin.json` 一致，否则产物地址指向不存在的资产。

## 5. CI 回填哈希

标签触发构建发布后，工作流的 `record` 作业自己下载产物、跑 `release.go record`，再把 `plugin.json` 与 `registry.json` 的改动提交回 `main`。哈希只能由真实上传的产物得出，本地 `pack` 的产物不能用来回填。

CI 绿后 `git pull origin main`，确认 `registry.json` 中该插件版本与哈希已就位：

```bash
jq '.plugins[] | select(.id=="<id>") | .version, .install.artifacts[0].sha256' registry.json
```

若 `record` 作业失败，本地补齐后提交推送：

```bash
go run scripts/release.go record --plugin <id> --dist dist
```

## 6. 线上验收

```bash
go run scripts/verify-registry-install.go <id>
```

通过后在宿主侧把插件更新到新版本，再用管理面确认：

```bash
go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins \
  | jq -c '.plugins[] | select(.id=="<id>") | {version: .metadata.version, config_fields}'
```

判据是 `version` 等于新标签的版本号，且声明过 `ConfigFields` 的插件 `config_fields` 非空。两者缺一，说明宿主仍跑旧产物。

## 相关

- 契约背景见 [ADR-0006](../adr/0006-changeset-driven-release.md)
- 产物命名的硬性要求见 [宿主产物契约](../reference/host-artifact-contract.md)
