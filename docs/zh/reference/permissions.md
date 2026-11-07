---
sourceHash: sha256-0c260479
---

# 角色、预设与权限 {#roles-presets-and-scopes}

人、他们的 Agent 和机器的令牌在 Beacon 里各能做什么。行动的一方，是以下三者之一：

- **人**，用账号登录，带一个**角色**。
- **Agent**，属于某个人，带一个**预设**。
- **独立的令牌**，按名字带 scope。它是机器的（检测点、主机、心跳、规则引擎、变更发送方、指标读取方），或是账号出现之前的旧式令牌。

谁能做什么，每个请求都重新解析，所以降级或停用一个人，立刻就限制住他的 Agent。人和 Agent 都不逐个挑选 scope。只有机器的令牌和专家的令牌才按名字授予 scope。

流程见[邀请你的团队](../guide/people.md)。每条路径需要什么 scope，见 [REST API](api.md) 和 [MCP 工具](mcp.md) 参考。

## 角色 {#roles}

人的角色，从弱到强。后一个能做前一个能做的一切，再多一些：

| 角色 | Scope | 用来做什么 |
| --- | --- | --- |
| `viewer` | `status:read`、`sessions:read` | 看发生了什么：告警、证据、会话、控制台 |
| `oncall` | + `alerts:write`、`machines:ssh` | 处理告警：认领、静默、备注，以及经 SSH 通道上机器 |
| `operator` | + `maintenance:write`、`incidents:write`、`machines:write` | 改运行的东西：维护窗口、机器和它们的密钥 |
| `owner` | + `config:write`、`accounts:write` | 部署本身：监控什么，以及上面的人 |

## 预设 {#presets}

Agent 能做的永远不超过其所有者的角色。一个 scope，要**预设**给了它、*且*其所有者的角色也给了，Agent 才能用。

没有预设给 `accounts:write`。Agent 要管人，只能是 owner 在命令行里以自定义 Agent 的方式给了它这个 scope。

| 预设 | 它要的 scope | 谁的 Agent 拿到它 |
| --- | --- | --- |
| `readonly` | `status:read`、`sessions:read` | viewer 的 Agent 至多拿到它 |
| `oncall` | + `alerts:write`、`machines:ssh` | 值班的人的 Agent 至多拿到它 |
| `operator` | + `maintenance:write`、`incidents:write`、`machines:write` | operator 或 owner 的 Agent 至多拿到它 |
| `custom` | 自己列一份，从人和 Agent 的九个 scope 里选，至少一个 | 有意选择的：`beacon agent add -scopes`，或认领一个 scope 恰好不匹配任何预设的旧式令牌。认领绝不会放大令牌的权限 |

一个人的 Agent 默认拿其所有者角色允许的最强预设，除非特意选低的（`beacon agent add -preset`，或控制台）。所有者被降级后，他的 Agent 立刻按新角色约束，从下一个请求起生效。

OAuth 登录（Claude Code、Codex）还另外受限于这个人在授权页上允许的预设和机器。它可以比 Agent 本身要得更少。Agent 经 OAuth 登录添加的 SSH 密钥，也带着那次登录的上限。

## Scope {#scopes}

前九个属于人和 Agent，用在 REST API、MCP 和 SSH 通道。其余属于机器，用在指标网关（[数据来源](sources.md)）。

| Scope | 谁持有 | 允许什么 |
| --- | --- | --- |
| `status:read` | 每个角色、每个预设 | 读：告警、证据与时间线、变更、静默、审计、机器与会话、配置、状态页 |
| `sessions:read` | 每个角色、每个预设 | 会话的记录文本与录像 |
| `alerts:write` | oncall 及以上 | 认领、静默、备注、按需检测 |
| `machines:ssh` | oncall 及以上 | SSH 通道和它的工具，限凭据可达的机器 |
| `maintenance:write` | operator 及以上 | 安排和结束维护窗口 |
| `incidents:write` | operator 及以上 | 为手工写事件预留；还没有路径用它 |
| `machines:write` | operator 及以上 | 增删机器、连接器密钥、暂停 Agent 的访问 |
| `config:write` | owner | 改 Beacon 监控什么：目录、目标集合、模块、状态页 |
| `accounts:write` | owner；无预设 | 管理人和所有人的 Agent；只有 owner，且只能手工给 |
| `probe:write` | 机器的令牌，来自 `beacon enroll probe <location>` | 推送结果、取目标与设置；需要主体，只以自己的检测点身份写入 |
| `host:write` | 机器的令牌，来自 `beacon enroll host <name>` | 推送主机指标；需要主体，只以自己的主机身份写入 |
| `heartbeat:write` | 机器的令牌，来自 `beacon enroll heartbeat <name>` | 每次成功推送一个样本 |
| `metrics:read` | 机器或专家的令牌 | 经网关查询指标 |
| `alerts:send` | 机器或专家的令牌 | 像规则引擎那样发告警 |
| `changes:write` | 机器或专家的令牌 | 发送变更事件 |

独立的令牌按名字带 scope，创建时就定死。Agent 的凭据则跟着 Agent 走。无论它出示哪个令牌或 OAuth 登录，能做的都是 Agent 的预设与其所有者角色此刻都允许的。

`GET /api/v1/token` 和 `POST /api/v1/token/renew` 接受任何 scope：令牌本身就是凭据。

## 机器 {#machines}

`probe:write` 和 `host:write` 需要**主体**：令牌以之写入的检测点或主机。网关给它写入的一切打上主体标签，取代发送方对自己的任何描述。所以一个检测点冒充不了另一个。机器已到期（目录里的 `vps` 过了日期）的令牌不能再写入。

Agent 或令牌在 SSH 通道上可达的机器，是各自的一份列表。空列表表示全部。OAuth 登录的列表与 Agent 的取交集，只到达两者都允许的机器。

令牌是 `lbt_` 加 52 个字符，Beacon 只存它们的 SHA-256。令牌在创建后 1 小时到 2 年之间过期。吊销的令牌立即失效。

持有者在过期前续期，至多一天一次。续期得到一个新的 secret，名字、scope、主体、机器和密钥都不变，旧 secret 还能再用十分钟。注册的机器令牌不续期，用 `beacon enroll -rotate` 换新。
