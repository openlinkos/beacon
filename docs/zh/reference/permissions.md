---
sourceHash: sha256-96aaf77a
---

# 角色、预设与权限 {#roles-presets-and-scopes}

人、他们的 Agent 和机器的令牌在 Beacon 里各能做什么。行动的一方，是以下三者之一：

- **人**，用账号登录，带一个**角色**。
- **Agent**，属于某个人，带一个**预设**。
- **独立的令牌**，按名字带 scope。它是机器的（检测点、主机、心跳、规则引擎、变更发送方、指标读取方），或是账号出现之前的旧式令牌。

谁能做什么，每个请求都重新解析，所以降级或停用一个人，立刻就限制住他的 Agent。人不逐个挑选 scope。Agent 的来自预设，或来自所有者设定的自定义清单。只有机器的令牌和专家的令牌才按名字授予 scope。

流程见[邀请你的团队](../guide/people.md)。每条路径需要什么 scope，见 [REST API](api.md) 和 [MCP 工具](mcp.md) 参考。

## 角色 {#roles}

人的角色，从弱到强。后一个能做前一个能做的一切，再多一些：

| 角色 | Scope | 用来做什么 |
| --- | --- | --- |
| `viewer` | `status:read`、`sessions:read` | 看发生了什么：告警、证据、会话、控制台 |
| `oncall` | + `alerts:write`、`machines:ssh` | 处理告警：认领、静默、备注，以及经 SSH 通道上机器 |
| `operator` | + `maintenance:write`、`incidents:write`、`machines:write` | 改运行的东西：维护窗口、机器和它们的密钥 |
| `owner` | + `config:write`、`accounts:write`、`secrets:use`、`alerts:report` | 管理部署配置、成员、团队凭据和执行回报的委托 |

## 预设 {#presets}

Agent 能做的永远不超过其所有者的角色。一个 scope，要**预设**或自定义清单给了它、*且*其所有者的角色也给了，Agent 才能用。调用者绝不会给 Agent 一个自己并不持有的 scope 或机器。

只有所有者账号、独立管理令牌和本机命令行可以管理成员账号。即使旧的自定义权限包含 `accounts:write`，Agent 身份也不能用它管理成员账号或 Agent 身份。

独立令牌受限于其自身 scope 覆盖的角色。只有持有某个成员角色的全部 scope，它才能邀请、为此人生成重设密码链接或修改此人（角色、停用、显示名、联系方式）；只有持有某个角色的全部 scope，它才能授予这个角色。只有 `accounts:write` 的令牌谁都不能邀请、重设或修改；带值班成员 scope 和 `accounts:write` 的令牌能管到查看者和值班成员。只有持有所有者全部 scope 的令牌才能管到所有者。本机命令行不受此限，所有者账号本就持有全部 scope。撤回待用的链接只会收回访问，所以任何能管理成员账号的凭据都可以撤回。

邀请链接和重设链接在打开时和使用时都会重新检查：只有生成它的人按上述规则仍然能生成它时，链接才有效。

| 预设 | 它要的 scope | 谁的 Agent 拿到它 |
| --- | --- | --- |
| `readonly` | `status:read`、`sessions:read` | viewer 的 Agent 至多拿到它 |
| `oncall` | + `alerts:write`、`machines:ssh` | 值班的人的 Agent 至多拿到它 |
| `operator` | + `maintenance:write`、`incidents:write`、`machines:write` | operator 或 owner 的 Agent 至多拿到它 |
| `custom` | 从人和 Agent 的作用域中选至少一个 | 有意选择的：以本人身份登录的所有者在控制台里，持有 `accounts:write` 的人或旧式令牌经 API 和 MCP（绝不是 Agent），`beacon agent add -scopes`，或认领一个 scope 恰好不匹配任何预设的旧式令牌。不超过所有者的角色，也不超过调用者自己能做的。认领绝不会放大令牌的权限 |

一个人的 Agent 默认拿其所有者角色允许的最强预设，除非特意选低的（`beacon agent add -preset`，或控制台）。所有者被降级后，他的 Agent 立刻按新角色约束，从下一个请求起生效。

OAuth 登录（Claude Code、Codex）还另外受限于这个人在授权页上允许的预设和机器。它可以比 Agent 本身要得更少。Agent 经 OAuth 登录添加的 SSH 密钥，也带着那次登录的上限。

## Scope {#scopes}

人和 Agent 的作用域用于 REST API、MCP 和 SSH 通道。机器的作用域用于指标网关（[数据来源](sources.md)）。

| Scope | 谁持有 | 允许什么 |
| --- | --- | --- |
| `status:read` | 每个角色、每个预设 | 读：告警、证据与时间线、变更、静默、审计、机器与会话、配置、状态页 |
| `sessions:read` | 每个角色、每个预设 | 凭据能看到的会话的命令、记录文本与录像。`machines:ssh` 也能看到同样的内容，所以对已有 `machines:ssh` 的凭据，`sessions:read` 没有额外作用。两者都只限于凭据能到的机器 |
| `alerts:write` | oncall 及以上 | 认领、静默、备注、按需检测 |
| `machines:ssh` | oncall 及以上 | SSH 通道和它的工具，限凭据可达的机器 |
| `maintenance:write` | operator 及以上 | 安排和结束维护窗口 |
| `incidents:write` | operator 及以上 | 为手工写事件预留；还没有路径用它 |
| `machines:write` | operator 及以上 | 增删改机器和连接器密钥（需能访问全部机器）；删除不超出自己权限的令牌；暂停 Agent 的访问 |
| `config:write` | owner | 改 Beacon 监控什么：目录、目标集合、模块、状态页 |
| `accounts:write` | 所有者账号或独立管理令牌 | 在凭据自身 scope 覆盖的角色内管理成员账号，以及管理所有人的 Agent |
| `secrets:use` | owner；仅自定义 Agent | 列出和使用明确授权给该 Agent 的团队凭据 |
| `alerts:report` | 独立令牌或 owner 的自定义 Agent | 为 `agents[].reporter` 指定给该身份的投递回报执行情况 |
| `probe:write` | 机器的令牌，来自 `beacon enroll probe <location>` | 推送结果、取目标与设置；需要主体，只以自己的检测点身份写入 |
| `host:write` | 机器的令牌，来自 `beacon enroll host <name>` | 推送主机指标；需要主体，只以自己的主机身份写入 |
| `heartbeat:write` | 机器的令牌，来自 `beacon enroll heartbeat <name>` | 每次成功推送一个样本；需要主体，只能写它自己的心跳 |
| `metrics:read` | 机器或专家的令牌 | 经网关查询指标 |
| `alerts:send` | 机器或专家的令牌 | 像规则引擎那样发它自己的告警，打上令牌的名字 |
| `changes:write` | 机器或专家的令牌 | 发送变更事件 |

独立的令牌按名字带 scope，创建时就定死。Agent 的凭据则跟着 Agent 走。无论它出示哪个令牌或 OAuth 登录，能做的都是 Agent 所要的（预设或自定义清单）与其所有者角色此刻都允许的。

`GET /api/v1/token` 和 `POST /api/v1/token/renew` 接受任何 scope：令牌本身就是凭据。

## 机器 {#machines}

`probe:write`、`host:write` 和 `heartbeat:write` 需要**主体**：令牌以之写入的检测点、主机或心跳。网关给它写入的一切打上主体标签，并把 `job` 设成令牌的种类（`probe`、`host`、`heartbeat`），取代发送方对自己的任何描述。所以一个检测点冒充不了另一个，主机冒充不了检测点，心跳令牌只能写它自己的心跳。机器已到期（目录里的 `vps` 过了日期）的令牌不能再写入。

### 机器访问 {#machine-access}

每个独立令牌、Agent 和 OAuth 登录都写明它在 SSH 通道上能到哪些机器，而且总是明确的：

| 机器访问 | 能到 |
| --- | --- |
| `all` | 全部非关键机器（含以后接入的），以及它点名的关键机器 |
| `named` | 只有它点名的机器 |

其他情况一台也到不了：没有给机器访问、空列表、Beacon 不认识的值，都表示没有机器。`beacon token create` 和 `beacon agent add` 建出的凭据默认没有机器，除非用 `-machines` 点名机器或写 `all`。

标为**关键**的机器，只有点名它的凭据能到：只有 `all` 永远到不了关键机器。给 Agent 的凭据（Agent、独立令牌或 OAuth 登录）点名的关键机器，默认 30 天后到期，点名的人可以另给期限。命令行写成 `name@72h`、`name@2026-12-31` 或 `name@never`。人自己账号上点名的机器默认不过期。到期后下一个请求起即无权访问；一分钟内 Beacon 去掉这个名字，记入审计日志（`machine_access_end`），并关闭在它上面打开的一切。

人按账号访问机器。owner 能到全部机器，包括关键机器。其他角色能到全部非关键机器，以及 owner 在其账号上点名的关键机器（`beacon user machines`）。Agent 能到的永远不超过它的所有者，OAuth 登录永远不超过它的 Agent。每一层都允许的才能到，每次请求都重新读取。

谁能给出机器访问：

- 只有人（不超过自己的角色）或主机上的 `beacon` 能给出 `all`。令牌和 Agent 只能点名机器，而且只能点名自己能到的。
- 只有 owner 或主机上的 `beacon` 能点名关键机器，无论是给 Agent、令牌、OAuth 登录还是人。授权页上关键机器只对 owner 显示，单独列出、默认不勾，勾选要明确确认。
- 不增加任何东西的修改（机器更少，或更早到期）不受调用者限制。

### 修改机器 {#changing-machines}

使用一台机器需要 `machines:ssh` 和对它的访问。修改机器需要 `machines:write` 并能访问全部机器，所以点名机器的凭据一台机器也改不了，包括它自己点名的。这包括添加机器、用 `replace` 重新添加、修改、恢复早期设置、移除和设置连接器密钥。

标记或取消关键机器，以及对关键机器的任何修改，只能由 owner 或主机上的 `beacon machine` 来做。Beacon 登录机器所用的账号，只有人或主机上的 `beacon` 能改，改成 `root` 只有主机上的 `beacon` 能做。机器本身只为这一个账号信任 Beacon 的证书颁发机构（[添加机器](../guide/machines.md#add-a-machine)），所以在 Beacon 里改了账号，还要在机器上重新执行那一行。

目标集合的响应机器（某组的 `machine`），无论经哪条路径设置、清除或移动（专门的路径、整组目标集合的 put、删除或回滚），都需要 `machines:write`，以及对改前和改后机器的 SSH 访问。

### 删除令牌 {#deleting-tokens}

`DELETE /api/v1/tokens/{name}`、`revoke_token` 和控制台的**令牌**页删除能连机器的令牌，或 Agent 的令牌。它们需要 `machines:write` 并能访问全部机器。独立令牌只能由持有它全部 scope、能到它能到的全部机器的调用者删除。Agent 的令牌由这个 Agent 自己、人，或管理 Agent 的管理员删除，其他 Agent 和令牌都不行。删除立即结束这个令牌；Agent 保留它的其他凭据。Agent 的所有者在 Agent 页面删除它的令牌，不需要 `machines:write`。

机器访问改为明确之前的凭据保持原来能到的机器。升级后的 Beacon 第一次启动时，把没有点名机器的令牌、Agent 和 OAuth 登录改为 `all`，并点名当时所有的关键机器；已经点名机器的列表保持点名。每个不是 owner 的人，账号上点名当时的关键机器。每一项改动都记入审计日志（`machine_access_migrate`）。

令牌是 `lbt_` 加 52 个字符，Beacon 只存它们的 SHA-256。令牌在创建后 1 小时到 2 年之间过期。吊销的令牌立即失效。

持有者在过期前续期，至多一天一次。续期得到一个新的 secret，名字、scope、主体、机器访问和密钥都不变，旧 secret 还能再用十分钟。注册的机器令牌不续期，用 `beacon enroll -rotate` 换新。
