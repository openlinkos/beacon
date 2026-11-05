---
sourceHash: sha256-9b3412c6
---

# 角色、预设与权限 {#roles-presets-and-scopes}

在 Beacon 里行动的是三种角色之一：**人**，用账号登录，带一个**角色**；**Agent**，属于某个人，带一个**预设**；或**独立的令牌**，按名字带 scope——机器的（检测点、主机、心跳、规则引擎、变更发送方、指标读取方），或账号出现之前的旧式令牌。谁能做什么，每个请求都重新解析，所以降级或停用一个人立刻限制住它的 Agent。人和 Agent 都不逐个挑选 scope；只有机器的令牌和专家的才按名字授予。流程见[邀请你的团队](../guide/people.md)；每条路径需要什么 scope，见 [REST API](api.md) 和 [MCP 工具](mcp.md) 参考。

## 角色 {#roles}

人的角色，从弱到强；后一个能做前一个能做的一切，再多一些：

| 角色 | Scope | 用来做什么 |
| --- | --- | --- |
| `viewer` | `status:read`、`sessions:read`、`secrets:describe` | 看发生了什么：告警、证据、会话、控制台，以及密钥概要 |
| `oncall` | + `alerts:write`、`machines:ssh`、`secrets:read`、`secrets:use` | 处理告警：认领、静默、备注，经 SSH 通道上机器，以及在资格范围内取用或使用密钥 |
| `operator` | + `maintenance:write`、`incidents:write`、`machines:write`、`secrets:review` | 改运行的东西：维护窗口、机器和它们的密钥，以及在审核链上裁决申请 |
| `owner` | + `config:write`、`accounts:write`、`secrets:admin` | 部署本身：监控什么、上面的人，以及保管库的级别、资格和设置 |

## 预设 {#presets}

Agent 永远不超过其所有者的角色：只有**预设**给了它、*且*其所有者的角色也给了它的 scope 才能用。没有预设给 `accounts:write`——Agent 管人，只可能是 owner 在命令行里以自定义 Agent 的方式给了它这个 scope。

| 预设 | 它要的 scope | 谁的 Agent 拿到它 |
| --- | --- | --- |
| `readonly` | `status:read`、`sessions:read`、`secrets:describe` | viewer 的 Agent 至多拿到它 |
| `oncall` | + `alerts:write`、`machines:ssh`、`secrets:read`、`secrets:use` | 值班的人的 Agent 至多拿到它 |
| `operator` | + `maintenance:write`、`incidents:write`、`machines:write`、`secrets:review` | operator 或 owner 的 Agent 至多拿到它 |
| `custom` | 自己列一份，从人和 Agent 的十四个 scope 里选，至少一个 | 有意选择的：`beacon agent add -scopes`，或认领一个 scope 恰好不匹配任何预设的旧式令牌；认领绝不会放大令牌的权限 |

一个人的 Agent 默认拿其所有者角色允许的最强预设，除非特意选低的（`beacon agent add -preset`，或控制台）；所有者被降级后，下一个请求起 Agent 就被按新角色约束。

OAuth 登录（Claude Code、Codex）在此之外还被限制在授权页允许的预设和机器里——它可以比 Agent 本身要得更少。Agent 经 OAuth 登录添加的 SSH 密钥，也带着那次登录的上限。

## Scope {#scopes}

人和 Agent 一共十四个 scope，覆盖 REST API、MCP 和 SSH 通道；其中五个是管保管库的 `secrets:` scope（见[管理 Agent 的密钥](../guide/secrets.md)）。Agent 预设经对应角色继承这些密钥 scope：只读 Agent 有 `secrets:describe`，值班 Agent 有 `secrets:read` 和 `secrets:use`，运维 Agent 有 `secrets:review`。其余 scope 属于机器，用在指标网关（[数据来源](sources.md)）。

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
| `secrets:describe` | viewer 及以上；`readonly` 预设 | 在资格范围内搜索、看概要（见[下面的密钥](#the-secrets-levels-entitlements-and-actions)） |
| `secrets:read` | oncall 及以上；`oncall` 预设 | 经 `get_secret` 读值，仍限资格与授权 |
| `secrets:use` | oncall 及以上；`oncall` 预设 | 不见值地使用：`beacon run`、`run_command`、`http_request`、文件下发 |
| `secrets:review` | operator 及以上；`operator` 预设 | 在审核链上裁决申请；有审核权不等于能读值 |
| `secrets:admin` | owner；可授给 Agent | 级别、标签、资格和[设置](settings.md) |
| `probe:write` | 机器的令牌，来自 `beacon enroll probe <location>` | 推送结果、取目标与设置；需要主体，只以自己的检测点身份写入 |
| `host:write` | 机器的令牌，来自 `beacon enroll host <name>` | 推送主机指标；需要主体，只以自己的主机身份写入 |
| `heartbeat:write` | 机器的令牌，来自 `beacon enroll heartbeat <name>` | 每次成功推送一个样本 |
| `metrics:read` | 机器或专家的令牌 | 经网关查询指标 |
| `alerts:send` | 机器或专家的令牌 | 像规则引擎那样发告警 |
| `changes:write` | 机器或专家的令牌 | 发送变更事件 |

独立的令牌按名字带 scope，创建时就定死；Agent 的凭据则跟着 Agent 走——无论它出示哪个令牌或 OAuth 登录，能做的是 Agent 的预设与其所有者角色此刻都允许的。`GET /api/v1/token` 和 `POST /api/v1/token/renew` 接受任何 scope：令牌本身就是凭据。

## 密钥：级别、资格与动作 {#the-secrets-levels-entitlements-and-actions}

密钥库——库、条目和它们的授权——由两个轴管着，取更严的。上面五个 scope 之一让 Agent 进门；进门之后能申请什么，是第二个轴。[管理 Agent 的密钥](../guide/secrets.md)走一遍流程；[设计](../about/secrets.md)讲为什么。

**条目的级别**——申请它要什么。放在库、标签或条目上；条目自己写的优先，否则取库默认值和条目所有标签中最严的：

| 级别 | 申请这个条目要什么 |
| --- | --- |
| `auto` | 有资格即可：申请即批准 |
| `review` | 走审核链——规则、审核 Agent、人，按[配置](settings.md#review) |
| `person` | 只能由人批准；审核 Agent 批不了 |
| `never` | 不交给任何 Agent；人仍可在控制台查看 |

**Agent 的资格**——它连能申请什么。默认拒绝：Agent 接入时什么都没有，不在资格里的条目对它不可见，搜索也一样。一份资格写明主体（Agent、Agent 标签、或某人的全部 Agent）、资源（库、标签 glob、条目，可选到字段）、下面的动作、`approval` 下限、自己的配置覆盖，以及它自己的审核者和期限。`deny` 资格先于一切允许；多份资格命中同一申请时，合成最宽的审批和最大的上限；任何资格都不能让 Agent 超出其所有者在该库的权限。改条目标签需要 `manage`，因为标签会改变权限。

**动作**——持有者拿到什么：

| 动作 | Agent 得到 | 工具 |
| --- | --- | --- |
| `describe` | 名字、类别、标签、网址、字段名、要求的级别、自己有没有授权 | `search_secrets` |
| `read` | 字段值；`otp` 字段给当前验证码和剩余秒数 | `get_secret` |
| `use` | 什么都看不见：值被注入命令、进程、请求或绑定机器上的文件 | `beacon run`、`run_command`、`http_request`、文件下发 |

条目、标签或库可以把动作限制为只能 `use`；可用的动作是资格允许的与条目允许的交集。

**人在每个库上的权限**——四档，叠在全局角色旁边：

| 权限 | 允许什么 |
| --- | --- |
| `describe` | 看条目概要 |
| `read` | 查看值，`reveal.reauth_window` 内免再验密码；每次查看和 Agent 取用一样记录 |
| `edit` | 建立条目、修改、归档、删除 |
| `manage` | 库的级别、标签、成员和资格，以及库级配置 |

owner 对每个库都有 `manage`。Agent 拿到的永远不超过其所有者的档位；所有者失去权限时，授权怎么办由配置决定（见[设置](settings.md)）。

## 机器 {#machines}

`probe:write` 和 `host:write` 需要**主体**——令牌以之写入的检测点或主机——网关给它写入的一切打上主体标签，取代发送方对自己的任何描述，一个检测点冒充不了另一个。机器已到期（目录里的 `vps` 过了日期）的令牌不能再写入。Agent 或令牌在 SSH 通道上可达的机器是各自的一份列表；空列表表示全部，OAuth 登录的列表与 Agent 的取交集，只到达两者都允许的。

令牌是 `lbt_` 加 52 个字符；Beacon 只存它们的 SHA-256。令牌在创建后 1 小时到 2 年之间过期。持有者在过期前续期——新的 secret，名字、scope、主体、机器和密钥都不变，旧 secret 再用十分钟——至多一天一次；注册的机器令牌不续期，用 `beacon enroll -rotate` 换新。吊销的令牌立即失效。
