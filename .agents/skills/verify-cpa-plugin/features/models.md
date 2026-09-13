# 模型清单

用户视角：启用插件后，宿主里直接可选到该渠道的官方模型，且看不到官方自己隐藏的模型。

## Sub-features

- `models-serve` 宿主把插件声明的模型全部报出来。
- `models-blacklist` 官方隐藏的模型不出现在列表里。
- `models-metadata` 上下文长度、输出上限与推理档位来自静态清单。
- `models-static` 模型集合不依赖运行时联网。

## How to get to it (user POV)

- 管理面板或客户端里看可用模型列表。
- `GET /v1/models` 直接拿清单。

## Driving it with dev-sandbox

Preconditions:

- 沙箱已就绪（`dev-sandbox` 输出中出现三条断言全过）。
- `plugins/workbuddy/data/static-config.json` 是当前模型集合的权威来源。

- **清单覆盖。** 跑 `go run scripts/dev-sandbox.go -plugin workbuddy -timeout 120s`。输出出现 `断言通过: /v1/models 返回 N 个模型, 清单声明的 N 个全部在列`。断言方向是"清单声明的必须都在"，插件按自身规则多报不在判据内。
- **数量对得上。** 起宿主并 `curl -s http://127.0.0.1:18317/v1/models | jq -r '.data[].id' | sort > /tmp/served.txt`，再 `jq -r '.models[].id' plugins/workbuddy/data/static-config.json | sort > /tmp/declared.txt`，`comm -23 /tmp/declared.txt /tmp/served.txt` 必须为空。沙箱不配 `api-keys`，所以 `/v1` 不带任何头即可；真实部署上 `/v1` 要客户端 API key，管理密钥不能替代。
- **黑名单生效。** `grep -c 'hy4-preview-x' /tmp/served.txt` 为 `0`，而 `hy4-preview-f` 在列（用清单里实际存在的一对隐藏/可见模型替换这两个 id）。
- **推理档位。** `curl -s http://127.0.0.1:18317/v1/models | jq '.data[] | select(.id=="hy3")'` 的档位字段与静态清单里同 id 的条目一致。
- **零联网。** 沙箱宿主的 `host.log` 在启动段不出现 `/v3/config` 之类的配置拉取；模型报送由 `go:embed` 的清单驱动。

## Gotchas

- 断言是"声明的都在"而不是"完全相等"，插件多报模型时沙箱不会失败，需要自己比对数量。
- 清单漂移只能靠重新导出修：`{pi-codebuddy-provider}/scripts/export-static.ts`。不要手改 JSON 里的 id。
- 隐藏规则来自官方客户端行为。用户报"看不到某个模型"时，先确认它在静态清单里，再看是否被过滤规则挡掉。
- 宿主侧还有 `force-model-prefix` 之类的配置会改模型 id 的呈现，排查前先看 `/v0/management/config`。
