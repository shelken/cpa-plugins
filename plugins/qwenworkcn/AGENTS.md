## 注意点

- `data/static-config.json` 由上游 `shelken/pi-qwenwork-provider` 生成，本地克隆一般在 `~/Code/active/pi-qwenwork-provider`：
  - `just static --export` 只重导清单；`just static` 导出并跑契约校验；App 升级后先 `just static --app` 把新版本写回 `headers.json`
  - 产物直接覆盖本插件的 `data/static-config.json`（带 `provenance` 来源标记），协议字段禁止手工臆造或修改
