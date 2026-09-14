---
name: verify-cpa-plugin
description: 验证 cpa-plugins 仓库插件与发布产物的可用性。涵盖本地沙箱运行、生产实例管理面检查、宿主版本门禁与安装态校验
---

# 验证 CPA 插件

插件「真实可用」由两层事实构成：宿主装载动态库并报出模型，客户端调用链路返回正常响应。只验证了第一层不等于可用。

## Launch

本地沙箱是主要驱动环境：自动编译动态库、生成隔离配置、启动宿主并执行四项装载断言

```bash
go run scripts/dev-sandbox.go -plugin <id> -timeout 120s
```

- 就绪判据: 输出包含 `[+] 沙箱验证通过` 与四项断言提示, 宿主默认监听 `18317`
- 需要保留宿主进程时传 `-keep`, 脚本退出时输出宿主 pid 与端口
- 沙箱宿主不依赖凭据即可驱动 `/v1/models` 等接口, 真实对话与额度需要注入凭据 (见 features/auth.md)
- 无论哪种启动方式, 验收结束必须按 Cleanup 清单清理

隔离性: 沙箱配置、端口、管理密钥按插件 id 隔离在 `~/.cache/cpa-plugins/sandbox/<id>/` 下, 与生产实例互不影响; 不要对生产实例做写操作驱动, 写操作只进沙箱

## Doctor

对目标实例执行单次只读探测, 回答「这个实例值得驱动吗」。任何异常先跑这个:

```bash
# 生产实例: 密钥经 sec-run 就地取得
go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins | jq -c '.plugins[] | {id, registered, enabled, effective_enabled, supports_quota, config_fields}'

# 本地沙箱: 管理密钥每次启动随机生成, 只能从沙箱目录读
go run scripts/management-api.go -path /v0/management/plugins -token-file ~/.cache/cpa-plugins/sandbox/<id>/management-key | jq -c '.plugins[] | {id, registered, enabled, effective_enabled, supports_quota, config_fields}'
```

判据:

- `HTTP 200` 且返回插件 JSON 列表
- 目标插件 `registered` 与 `enabled` 与 `effective_enabled` 均为 `true`
- 宿主版本从响应头 `X-CPA-VERSION` 读取, 额度特性要求 `>= v7.2.159` (见 features/quota.md)

## Drive

统一经仓库脚本驱动, 管理密钥默认由 `sec-run` 就地注入 `CPA_TOKEN`:

```bash
# 读取插件列表与装载状态
go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins

# 读取已保存登录账号列表
go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/auth-files

# 读取指定插件当前配置
go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins/<id>/config

# 修改插件配置 (写操作须经确认, 仅对沙箱执行)
go run scripts/management-api.go -base http://<host>:8317 -path /v0/management/plugins/<id>/config -method PUT -body '{"enabled":true}'

# 按客户端身份探测 /v1 接口, 客户端密钥由脚本就地取得
go run scripts/management-api.go -base http://<host>:8317 -auth client -path /v1/models
```

对话链路验收用专用入口, 一次跑完流式帧、思维链、上下文记忆、缓存、思考深度与非流式 (见 features/chat.md)

## 密钥纪律

密钥只在进程内流转:

- 管理密钥只经 `sec-run printenv CPA_TOKEN` 隐式读取, 不落环境变量文件, 不出现在命令行与终端输出
- 客户端密钥由脚本就地取自管理面 (`scripts/verify-chat.go` 与 `management-api.go -auth client` 都这样做), 只进请求头
- 直连 `/v1` 的探测用 `-auth client`, 不要手抄密钥拼 `curl -H "Authorization: Bearer ..."`
- 沙箱是唯一例外: 沙箱管理密钥每次 `dev-sandbox.go -keep` 随机生成, sec-run 里的 `CPA_TOKEN` 是生产密钥, 两者必然不同。驱动沙箱管理面一律 `-token-file ~/.cache/cpa-plugins/sandbox/<id>/management-key` (含 `verify-chat.go`), 密钥值不回显
- 管理面响应里的密钥与凭据字段默认打码, 判断「是哪一条、换没换」看 `<redacted len=.. sha256=..>` 的长度与前 4 字节哈希
- 失败信息不带取值: 脚本出错时只回显 URL 与状态码

每条命令是独立 shell, 上一条 `export` 的变量在下一条不存在; 需要复用的取值写进同一条命令, 或由脚本参数传入

## Evidence

证据记录真实终端输出与命令原文, 存放于 `~/.cache/cpa-plugins/evidence/<plugin>/`:

- 插件状态: 管理面响应体及 `[host]` 版本头
- 账号归属: `auth-files` 中脱敏后的 `provider`、`type` 与 `status` 字段
- 模型报送: `/v1/models` 实际暴露的模型列表

证明标准: 驱动真实用户路径 (管理面入口 + `/v1` 接口), 不用测试专用端点; 同时记录动作与结果状态; 凭据注入这类副作用要二次只读确认 (`auth-files` 再查一次), 不以注入时的响应为准

## Cleanup

每次验收结束 (无论成败) 必须执行, 全部做完才算收尾:

1. 杀沙箱宿主进程: `pkill -f "cliproxyapi -config.*sandbox/<id>"`, 杀完用 `pgrep -f cliproxyapi` 确认无本沙箱残留
2. 删临时凭据: 注入沙箱的凭据 JSON 与密钥中转文件全部删除, 含整个沙箱目录 `~/.cache/cpa-plugins/sandbox/<id>/`; 严禁删除生产宿主凭据目录里的真实凭据
3. 删临时脚本与产物: 驱动脚本、mock 上游、修改过的 static-config 副本、抓包日志
4. 恢复外部状态: 验证时 PUT 过的插件配置改回原值; 悬挂登录会话用 `DELETE /v0/management/oauth-session?state=<state>` 注销
5. 证据先落盘 `~/.cache/cpa-plugins/evidence/<plugin>/`, 再执行上述删除

跳过清理的后果: 残留宿主进程占住 18317 端口让下次沙箱起不来; 残留凭据让下轮验证误判「已登录」; 残留 mock 上游让后续请求打到假服务得出假结论

## Helpers

- `scripts/management-api.go`: 管理面单点交互工具, 默认由 `sec-run` 注入 `CPA_TOKEN` 鉴权, 沙箱场景用 `-token-file` 读沙箱密钥, 响应里的密钥与凭据字段默认打码; `-auth client` 时改为按客户端身份访问 `/v1`, 客户端密钥由进程自己从管理面取; `-body-file` 从文件读请求体, 用于凭据上传
- `scripts/verify-chat.go`: 对话链路验收入口, 判据项清单见 features/chat.md
- `scripts/dev-sandbox.go`: 沙箱启动与装载断言套件, 只覆盖装载与报送, 替代不了真机对话验收
- `scripts/check-plugins.go`: 检查仓库清单、构建矩阵、声明平台一致性
- `scripts/verify-registry-install.go`: 在线拉取已发布产物校验哈希与动态库格式

## 刚发布的产物怎么读

`raw.githubusercontent.com` 上的 `registry.json` 走 CDN, 推送后可能仍返回上一版, 表现为清单里的 `version` 已是新版而 `sha256` 还是旧值。此时:

- 要权威内容就直接读仓库
  ```bash
  gh api repos/<owner>/<repo>/contents/registry.json --jq .content | base64 -d
  ```
- 要校验刚发布的产物就加 `-local` 对本地 `registry.json` 跑 `verify-registry-install.go`
- 要装到宿主就直接触发安装并看响应, 宿主自己会拉到新鲜内容

## 特性地图

按用户可见功能组织的验证用例, 每个文件含驱动命令、判据与坑:

- [插件装载与配置](features/plugins-config.md)
- [扫码登录与凭据保存](features/auth.md)
- [模型注册与可用性](features/models.md)
- [对话链路](features/chat.md)
- [额度查询与版本门禁](features/quota.md)
