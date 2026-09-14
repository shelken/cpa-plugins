# QwenWork CN 协议基准、静态模型清单与钱包额度主源

我们需要把 `pi-qwenwork-provider` 的通义灵码接入能力移植进 CLIProxyAPI，作为原生的一级 upstream provider 插件。决策：以 QwenWork CN 桌面客户端 0.1.8（Cosy 协议版本 1.1.18）为协议基准，采用纯 Go 移植 Cosy 签名算法与 Device Flow 登录握手；模型采用静态内嵌清单（3 个模型，强制默认带 `qwenworkcn/` 前缀）；额度查询采用官方钱包余额（Wallets）为主源、用量包（Usage）为辅的聚合策略；账号托管、调度、冷却与轮转完全交给 CPA 宿主。

## Status

accepted

## Context

- QwenWork（通义灵码）反向接口采用阿里 Cosy 协议，请求体需经自定义 Base64 与双端字符置换（EndSwap），请求头需携带 RSA-1024 混合加密的 `Cosy-Key`、AES-128-CBC 加密的 `Cosy-User` 载荷以及 MD5 五段哈希派生的 `Authorization: Bearer COSY.<p1>.<part2>` 动态签名。
- 官方未提供公开 API 规范与文档，接口字段与加密体系来自对客户端运行时流量与逆向成果的实证沉淀。
- 官方提供 `pro`（高级）、`flash`（标准）、`qwen3.8-max-preview`（预览）三个模型，均为仅推理模型（`onlyReasoning: true`），关闭思考等级（`none`）会导致服务端直接返回 400 错误。
- 在额度方面，普通免费/赠送用户的 `/api/v2/quota/usage` 接口当前返回全 null，实际额度以网页端 `/user/wallets` 的活跃钱包余额为准。

## Considered Options

1. **协议基准与签名实现**：
   - 方案 A：引入 CGO 胶水绑定 Node/Wasm 签名产物。
   - 方案 B（选中）：在 Go 语言内原生移植 Cosy 签名全套数学与加密算法（BigInt 裸 RSA-1024、AES-128-CBC、自定义 Base64、EndSwap），零外部进程依赖。
2. **模型清单来源**：
   - 方案 A（选中）：固化静态清单 `static-config.json`，由研究仓 `export-cpa-static.ts` 脚本基于真实客户端抓包统一导出，启动零网络延迟。
   - 方案 B：启动时通过 `/algo/api/v2/model/list` 动态拉取。
3. **额度查询主源**：
   - 方案 A（选中）：以 `/user/wallets` 钱包余额为主源（fromBalance 语义），用量包 `/api/v2/quota/usage` 兜底。
   - 方案 B：仅查询 `/api/v2/quota/usage`，用户将显示无额度。
4. **身份分档**：
   - 方案 A（选中）：单一身份档，固定 `aarch64_darwin` 客户端特征，砍掉无意义的 desktop/cli profile 分档。
   - 方案 B：保留 desktop/cli 复杂分档。

## Consequences

- **极简运行时**：插件编译为标准跨平台动态库，无需额外 node/wasm 运行时或外部可执行程序。
- **协议保真**：完全对齐官方客户端流量，每个请求独立生成 UUID、规范派生会话 ID、精确双写 user content，参数恒发 `context_length: 1000000`。
- **前缀隔离**：默认启用 `qwenworkcn/` 模型前缀（`qwenworkcn/pro` 等），避免短模型名与其他渠道冲突，满足 Monorepo 插件约束。
- **真实额度**：正确反映用户真实拥有的 100 credits 钱包余额与最早到期时间，杜绝将有效账号误判为无额度。
- **无状态设计**：多账号通过独立凭据文件由宿主托管，插件内部仅保留登录流程瞬时内存注册表，不落盘任何敏感数据。
