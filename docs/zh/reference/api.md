---
sourceHash: sha256-7c5c0a3c
---

# REST API

Beacon 的 API 在 17001 端口的 `/api/v1` 下，收发 JSON。除了接口说明本身，每个路径都需要一个 bearer 令牌，带有旁边列出的作用域；同一个令牌也用于 MCP 和 SSH 通道，这里的每个操作也都是一个调用同一方法的 [MCP 工具](mcp.md)。OpenAPI 文档（仓库里的 `internal/api/openapi.yaml`，运行中的 Beacon 上的 `GET /api/v1/openapi.yaml`）是正式约定，列有每个字段；这一页是地图。

## 请求与应答 {#requests-and-answers}

```bash
curl -H "Authorization: Bearer $TOKEN" https://status.example.com/api/v1/alerts
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/alerts/0cdfbada62dd/notes -d '{"text": "Restarted the relay; probing again."}'
```

- **凭据**是令牌或 OAuth 访问令牌。令牌以 `lbt_` 开头，这是 Beacon 令牌的标记：Agent 的令牌在控制台的 **Agent** 页面或用 `beacon agent add` 创建，机器的或熟手用的令牌用 `beacon token create NAME -scopes … -expires …` 创建；它有有效期，并能按请求自行续期（`POST /api/v1/token/renew`）。OAuth 访问令牌（`lba_…`）是 Claude Code 这类 MCP 客户端在成员允许后拿到的；它有效一小时，由客户端刷新（[成员、Agent 与登录](../accounts.md)）。Beacon 只保存它们的 SHA-256 哈希。
- **请求体**是 JSON（`Content-Type: application/json`）；不认识的字段会被拒绝，这样拼错的字段不会被悄悄忽略。
- **写操作**连同操作者记入审计日志（Agent 记为 `alice / claude-code`，其他令牌记为令牌自己的名字），并回显到告警群。机器上的会话也都有记录。
- **错误**是 `{"error": "…", "code": "…"}`：消息说明出了什么错、怎么改正，错误码保持稳定，客户端可以据此处理。错误码[列在下面](#error-codes)。`401`（`auth.required`、`auth.invalid`）在这里和网关上还带着 `"docs": "https://beacon.openlinkos.com/reference/api"`，指向本页的英文版，给第一次接触 Beacon 的人。
- **不可信的文本**：应答的 `untrusted` 对象列出的字段来自 Beacon 之外（标签、被检测的服务、变更的发送方、机器），已去掉控制字符和不可见字符，太长的会截断：它们是数据，不是指令。

## 作用域 {#scopes}

一个凭据可以用一个或多个作用域。前九个给人和 Agent 用，用于这个 API、MCP 和 SSH 通道；其余的给机器用，用于网关。人的作用域来自角色，Agent 的来自预设，且不超出所有者的角色；只有机器的令牌和熟手用的令牌按名字指定作用域。

| 作用域 | 允许做什么 |
| --- | --- |
| `status:read` | 读取：告警、证据和时间线、变更事件、静默、审计日志、机器和会话、配置、状态页和维护窗口 |
| `alerts:write` | 处理告警：认领、静默和结束静默、备注、按需检测 |
| `machines:ssh` | 用令牌上的密钥登录 SSH 通道，机器工具（执行、读、写、改），它能到达的机器和这些机器上的会话 |
| `machines:write` | 添加和删除机器、连接密钥，暂停和恢复 Agent 到达机器 |
| `sessions:read` | 会话的文字记录和录制 |
| `config:write` | 修改 Beacon 监控的内容（目录、检测目标组和检测模块）和状态页，也能读取 |
| `accounts:write` | 管理成员：列出、邀请、修改角色、停用、重设密码链接；在控制台上归入旧令牌、管理所有人的 Agent。所有者才有；没有哪个 Agent 预设给它 |
| `maintenance:write` | 安排和结束维护窗口：状态页提前公告，窗口内静默相关服务的告警 |
| `incidents:write` | 为在状态页上手写事件预留；目前没有路径用到（事件来自告警组） |
| `probe:write` | 检测点：推送结果，取它的检测目标、检测模块和设置，按需检测。只能以它自己的检测点写入 |
| `host:write` | 主机：推送它的指标。只能以它自己的主机写入 |
| `heartbeat:write` | 推送心跳 |
| `metrics:read` | 经网关查询指标 |
| `alerts:send` | 像规则引擎一样发送告警 |
| `changes:write` | 发送变更事件 |

| 角色 | Agent 预设 | 作用域 |
| --- | --- | --- |
| 查看者 | 只读 | `status:read`、`sessions:read` |
| 值班 | 值班 | + `alerts:write`、`machines:ssh` |
| 运维 | 运维 | + `maintenance:write`、`incidents:write`、`machines:write` |
| 所有者 | | + `config:write`、`accounts:write` |

Agent 能用一个作用域，要它的预设给了、它的所有者的角色也给了，每次请求时检查：降低或停用所有者会立即限制这个 Agent。OAuth 登录还要受成员当初允许它的预设和机器限制。被拒绝时（`auth.scope`），消息会说明这个 Agent 的预设在所有者角色范围内允许什么。

`GET /api/v1/token` 和 `POST /api/v1/token/renew` 接受任何作用域：令牌本身就是凭据。

## 告警 {#alerts}

| 路径 | 作用域 | 做什么 |
| --- | --- | --- |
| `GET /api/v1/bridge` | `status:read` | 告警现在能否送到人（不能的话为什么），按级别统计的正在触发的告警组，已认领的数量，本月飞书 API 调用次数和额度 |
| `GET /api/v1/alerts` | `status:read` | 告警组，正在触发的在前，新的在前；`?state=all` 加上最近七天恢复的 |
| `GET /api/v1/alerts/{id}` | `status:read` | 一个告警组：标签、处理手册和面板、认领、静默、升级、备注、唤醒的 Agent，以及 `page`，即给人看的页面的签名链接 |
| `POST /api/v1/alerts/{id}/ack` | `alerts:write` | 认领一个正在触发的告警组，可带 `note`：升级到手机停止四小时 |
| `GET /api/v1/silences` | `status:read` | 生效中和待生效的静默，最先结束的在前 |
| `POST /api/v1/silences` | `alerts:write` | 静默一个正在触发的告警组：`alert`、`duration`（最长 24 小时）和必填的 `comment`。同时认领它 |
| `DELETE /api/v1/silences/{id}` | `alerts:write` | 立即结束一个静默 |
| `GET /api/v1/audit` | `status:read` | 最近的写操作，新的在前，带操作者和途径（`limit`，最多 1000） |

## 处理告警 {#working-an-alert}

| 路径 | 作用域 | 做什么 |
| --- | --- | --- |
| `GET /api/v1/alerts/{id}/evidence` | `status:read` | 判断一个告警组所需的一切，一次给全：每个检测点的结果和失败原因、相关告警组、变更事件、已经做了什么、链接（[Agent 与告警](../agents.md#the-evidence)）。`before` 把时间窗口往前放宽，最多 `24h` |
| `GET /api/v1/alerts/{id}/timeline` | `status:read` | 告警组的来龙去脉，按时间排列，覆盖证据的时间窗口 |
| `POST /api/v1/alerts/{id}/notes` | `alerts:write` | 留一条备注，`text` 最多 2000 字符，正在触发或已恢复的告警组都行。备注不算认领 |
| `GET /api/v1/changes` | `status:read` | 变更事件，新的在前，可按 `service`、`machine`、`source` 和 `since`（`2h`、`7d` 或一个时间；默认最近一天）筛选 |
| `POST /api/v1/probe-now` | `alerts:write` | 让检测某个目标的每个检测点立刻重测：`alert_id`、`target`（可带 `module`）或 `service`；`wait` 秒，默认 45，最多 90 |

## 机器与会话 {#machines-and-sessions}

| 路径 | 作用域 | 做什么 |
| --- | --- | --- |
| `GET /api/v1/ssh` | `status:read` | SSH 通道的状态（是否暂停）、它的主机密钥，以及机器所信任的 CA 公钥 |
| `POST /api/v1/ssh/pause` | `machines:write` | 关掉所有 Agent 在机器上的会话，并拒绝新会话 |
| `POST /api/v1/ssh/resume` | `machines:write` | 让 Agent 重新能到达机器 |
| `GET /api/v1/machines` | `status:read` 或 `machines:ssh` | 机器列表、令牌能否到达每台机器、连接程序的状态，以及给 Agent 自己的 ssh 用的 `ssh_config` 和一行 `known_hosts` |
| `POST /api/v1/machines` | `machines:write` | 添加一台机器并钉住它的主机密钥；NAT 后面的机器用 `address: connector`，带上它连出来用的密钥。返回机器上需要做的设置 |
| `DELETE /api/v1/machines/{name}` | `machines:write` | 忘掉一台机器；它的会话记录保留 |
| `PUT /api/v1/machines/{name}/connector` | `machines:write` | 设置或更换 NAT 后面的机器连出来用的密钥 |
| `POST /api/v1/machines/{name}/run` | `machines:ssh` | 以机器上的用户身份、在 `sh` 里、不分配终端执行一条命令：退出码、stdout 和 stderr |
| `GET /api/v1/machines/{name}/file` | `machines:ssh` | 文本文件的若干行，带行号，可用 `offset` 和 `limit` |
| `PUT /api/v1/machines/{name}/file` | `machines:ssh` | 写入整个文件，最多 1 MiB，需要时创建目录 |
| `PATCH /api/v1/machines/{name}/file` | `machines:ssh` | 替换一段完全一致的文本，一次或全部；读取后文件变过则拒绝 |
| `GET /api/v1/sessions` | `status:read` 或 `machines:ssh` | 机器上的会话，新的在前：谁、在哪里、做什么、什么时候、退出码 |
| `GET /api/v1/sessions/{id}` | `sessions:read` 或 `machines:ssh` | 会话的文字记录：命令的输出，或交互 shell 的各条命令及其输出和退出码 |
| `GET /api/v1/sessions/{id}/recording` | `sessions:read` 或 `machines:ssh` | 录制文件，asciicast v3，可以用 asciinema 回放 |

## 配置 {#configuration}

Beacon 监控的内容，每一项都按它在文件里的样子读写（[目录、检测目标和检测模块](../catalog.md)）。读取需要 `status:read` 或 `config:write`；修改需要 `config:write`。`PUT` 和 `DELETE` 接受 `?note=`（随修改保存，并显示在告警群里）和 `?if_version=`（已有更新的版本生效时拒绝，返回 `config.conflict`）。

| 路径 | 作用域 | 做什么 |
| --- | --- | --- |
| `GET /api/v1/config` | `status:read` 或 `config:write` | 全部配置，带当前生效的版本 |
| `GET /api/v1/config/locations` | `status:read` 或 `config:write` | 检测点，按 id |
| `GET /api/v1/config/locations/{id}` | `status:read` 或 `config:write` | 一个检测点 |
| `PUT /api/v1/config/locations/{id}` | `config:write` | 添加或替换一个检测点 |
| `DELETE /api/v1/config/locations/{id}` | `config:write` | 删除一个检测点；还有检测目标组只以它命名时拒绝 |
| `GET /api/v1/config/heartbeats` | `status:read` 或 `config:write` | 心跳，按名字 |
| `GET /api/v1/config/heartbeats/{name}` | `status:read` 或 `config:write` | 一个心跳 |
| `PUT /api/v1/config/heartbeats/{name}` | `config:write` | 添加或替换一个心跳 |
| `DELETE /api/v1/config/heartbeats/{name}` | `config:write` | 删除一个心跳 |
| `GET /api/v1/config/resources` | `status:read` 或 `config:write` | 会到期的资源，按名字 |
| `GET /api/v1/config/resources/{name}` | `status:read` 或 `config:write` | 一项资源 |
| `PUT /api/v1/config/resources/{name}` | `config:write` | 添加或替换一项资源 |
| `DELETE /api/v1/config/resources/{name}` | `config:write` | 删除一项资源 |
| `GET /api/v1/config/targets` | `status:read` 或 `config:write` | 检测目标组，按名字，带由谁检测 |
| `GET /api/v1/config/targets/{name}` | `status:read` 或 `config:write` | 一个检测目标组的各个分组 |
| `PUT /api/v1/config/targets/{name}` | `config:write` | 添加或替换一个检测目标组；检测点在一分钟内拿到 |
| `DELETE /api/v1/config/targets/{name}` | `config:write` | 删除一个检测目标组 |
| `GET /api/v1/config/modules` | `status:read` 或 `config:write` | 检测模块，按名字 |
| `GET /api/v1/config/modules/{name}` | `status:read` 或 `config:write` | 一个检测模块 |
| `PUT /api/v1/config/modules/{name}` | `config:write` | 添加或替换一个检测模块；模块文件里的其他模块和注释保持不变 |
| `DELETE /api/v1/config/modules/{name}` | `config:write` | 删除一个检测模块 |
| `GET /api/v1/config/status_pages` | `status:read` 或 `config:write` | 公开的状态页，按名称（[状态页](../status.md)） |
| `GET /api/v1/config/status_pages/{name}` | `status:read` 或 `config:write` | 一个状态页的定义 |
| `PUT /api/v1/config/status_pages/{name}` | `config:write` | 添加或替换一个状态页；组件的服务还没有检测目标时照样接受，并给出警告 |
| `DELETE /api/v1/config/status_pages/{name}` | `config:write` | 删除一个状态页 |
| `GET /api/v1/config/history` | `status:read` 或 `config:write` | 修改记录，新的在前，不带值（`kind`、`key`、`before`、`limit`）；保留最近 10,000 次 |
| `GET /api/v1/config/history/{version}` | `status:read` 或 `config:write` | 一次修改，带每一项改前和改后的值 |
| `POST /api/v1/config/history/{version}/revert` | `config:write` | 把一次修改作为一次新的修改撤销；之后的修改动过其中某项时返回 `config.conflict` |

## 状态页与维护 {#status-pages-and-maintenance}

公开的页面本身不需要令牌（[见下](#other-routes-on-port-17001)）；这些路径给 Agent 同样的内容，并让它们安排维护（[状态页](../status.md)）。

| 路径 | 作用域 | 内容 |
| --- | --- | --- |
| `GET /api/v1/status/{name}` | `status:read` | 状态页的公开 JSON：每个组件现在和 90 天来的状态、事件及其公开进展、维护窗口；`?lang=` |
| `GET /api/v1/maintenance` | `status:read` | 维护窗口，开始得越晚越靠前；结束的保留 91 天 |
| `POST /api/v1/maintenance` | `maintenance:write` | 安排一个窗口：`services`、`starts_at`（不填就是现在）、`ends_at`（最长 7 天）、`title`、`description`。状态页会公告它，每个服务一个静默，窗口内告警不会叫醒人 |
| `DELETE /api/v1/maintenance/{id}` | `maintenance:write` | 立即结束一个窗口，或在开始前取消 |

## 成员与 SSH 密钥 {#people-and-ssh-keys}

成员，供所有者的凭据使用；以及调用者自己用于 SSH 通道的 SSH 密钥（[成员、Agent 与登录](../accounts.md)）。没有哪个 Agent 预设给 `accounts:write`，所以只有所有者在命令行上把这个作用域给了 Agent，Agent 才能管理成员；重设密码没有 MCP 工具。

| 路径 | 作用域 | 做什么 |
| --- | --- | --- |
| `GET /api/v1/users` | `accounts:write` | 成员（用户名、显示名、角色、最近登录、是否停用、他们的 Agent；从不包括密码或它的哈希）和尚未使用的邀请 |
| `POST /api/v1/users/invitations` | `accounts:write` | 邀请一个人，带 `role` 和 `note`：一个一次性链接，24 小时内有效，只在应答里出现 |
| `PATCH /api/v1/users/{name}` | `accounts:write` | 修改成员的 `role`、`display_name` 或 `feishu_open_id`（联系信息，从不用于登录），或设置 `disabled`；立即作用于他的会话和 Agent。Beacon 始终保留一个启用的所有者（`user.last_owner`） |
| `POST /api/v1/users/{name}/reset` | `accounts:write` | 一个一次性的重设密码链接，30 分钟内有效，只在应答里出现 |
| `GET /api/v1/ssh-keys` | `machines:ssh` | 调用者自己的 SSH 密钥；Agent 的密钥属于它的 Agent，无论哪个凭据来问，Agent 自己登记的密钥会写明经由哪个凭据（`via`：`token:NAME` 或 `grant:ID`） |
| `POST /api/v1/ssh-keys` | `machines:ssh` | 登记一个公钥：`key`（一行 `authorized_keys`）和 `comment`。Agent 的密钥属于它登记时所用的凭据：那个令牌或 OAuth 登录有效时它才有效，随它一起移除。一个密钥只属于一个 Agent 或令牌，绝不属于机器的连接程序（`key.exists`） |
| `DELETE /api/v1/ssh-keys` | `machines:ssh` | 移除一个，`?fingerprint=SHA256:…`；凭它登录的连接会被关闭 |

## 令牌 {#the-token}

| 路径 | 作用域 | 做什么 |
| --- | --- | --- |
| `GET /api/v1/token` | 任意 | 调用者：是谁（`actor`、`user`、`agent`、`preset`），它的凭据（`token`，或带 `client` 的 `oauth`），当前能做什么、机器、SSH 密钥指纹、到期时间、有效期长度、上次续期、能否以及何时可以续期；从不包括密钥本身 |
| `POST /api/v1/token/renew` | 任意 | 一个新的密钥，只显示一次，有效期从现在起重新算；旧密钥还能用十分钟。每 24 小时最多一次；登记的机器的令牌不能续期，OAuth 访问令牌也不能，由它的客户端刷新 |
| `DELETE /api/v1/tokens/{name}` | `machines:write` | 吊销一把能到达机器的令牌（`machines:ssh`），或任何 Agent 的令牌：针对单个 Agent 的暂停开关，它用这把令牌在机器上打开着的会话立即关闭。检测点、主机、心跳、规则引擎、变更发送方和指标读取方的令牌只能在主机上吊销（`token.not_revocable`） |
| `GET /api/v1/openapi.yaml` | 无需 | OpenAPI 文档 |

## 网关 {#the-gateway}

网关在 17006 端口，给机器用：检测点、主机、心跳、规则引擎和变更事件的发送方。各自发送什么，见[数据来源](../sources.md)。

| 路径 | 作用域 | 做什么 |
| --- | --- | --- |
| `POST /api/v1/import/prometheus` | `probe:write`、`host:write`、`heartbeat:write` | Prometheus 文本格式的样本（`beacon-probe` 推送的就是这种），以及心跳 |
| `POST /api/v1/write` | `probe:write`、`host:write` | Prometheus remote write |
| `GET /api/v1/targets` | `probe:write` | 这个检测点要检测的目标 |
| `GET /api/v1/modules` | `probe:write` | 检测模块文件，逐字节原样 |
| `GET /api/v1/agent` | `probe:write` | 检测点的设置：它的 id、检测超时和重试次数 |
| `GET /api/v1/agent/requests` | `probe:write` | 按需检测：长轮询（`?wait=25s`，最多 55 秒）等待要立刻检测的目标 |
| `POST /api/v1/agent/requests/{id}` | `probe:write` | 检测点交回一个请求的检测结果 |
| `POST /api/v2/alerts` | 规则令牌，或 `alerts:send` | 告警，Alertmanager API v2 的格式 |
| `POST /api/v1/changes` | `changes:write` | 一个变更事件：部署、配置修改。每个令牌每分钟最多 60 个 |
| `POST /api/v1/changes/flux` | `changes:write` | Flux notification-controller 的事件，来自它的 `generic` provider |
| `GET /api/v1/query`、`query_range`、`series`、`labels`、`label/{name}/values`、`metadata`、`status/{what}`、`export`、`rules`、`alerts` | `metrics:read` | 转给 VictoriaMetrics 和 vmalert 的查询，不带令牌 |

## 17001 端口上的其他路径 {#other-routes-on-port-17001}

| 路径 | 做什么 |
| --- | --- |
| `/mcp` | MCP server，streamable HTTP，用同样的 bearer 令牌或 OAuth 访问令牌；`401` 会指明去哪里登录（`WWW-Authenticate: Bearer resource_metadata="…"`）（[MCP 工具](mcp.md)） |
| `GET /` | 公开：默认状态页，与 `/status` 提供的相同；没有状态页时，是一个写明这套部署、带**登录**的页面（[状态页](../status.md#branding)） |
| `GET /console`、`GET /console/sign-in` 和 `/console` 下的页面 | 控制台，供用账号登录的人使用，再保留一个版本也可以用令牌登录（[控制台](../pages.md)） |
| `GET /.well-known/oauth-protected-resource/mcp`、`GET /.well-known/oauth-protected-resource`、`GET /.well-known/oauth-authorization-server` | MCP 客户端用来发现 Beacon 的 OAuth 元数据（RFC 9728、RFC 8414） |
| `POST /oauth/register`、`GET /oauth/authorize`、`POST /oauth/token`、`POST /oauth/revoke` | Beacon 作为 MCP 客户端的授权服务器：注册、授权页面、令牌和吊销（[成员、Agent 与登录](../accounts.md#the-oauth-endpoints)） |
| `GET /console/alerts/{id}`、`GET /console/sessions/{id}` | 人从卡片点开的页面，凭签名链接或令牌打开（[控制台](../pages.md#signed-links)） |
| `GET /alerts/{id}`、`GET /sessions/{id}`、`GET /sign-in` 以及控制台的其他旧路径 | `308` 转到 `/console` 下的同一路径，带着查询参数；为旧路径签名的链接换上新路径的签名，到期时间不变 |
| `GET /status`、`GET /status/{name}`、`GET /status/{name}/incidents/{id}` | 公开的状态页和其中的事件，不需要令牌（[状态页](../status.md)）；在状态页自己的域名上是 `/` 和 `/incidents/{id}` |
| `GET /status.json`、`GET /status/{name}.json`、`GET /status/{name}/feed.atom`、`GET /status/{name}/badge.svg`、`GET /status/{name}/components/{component}/badge.svg` | 状态页的 JSON、Atom 订阅和徽章，公开、可缓存 |
| `GET /robots.txt` | 让搜索引擎读取 `/`、`/status` 和 `/static/`，不进 `/console`、`/api/`、`/mcp`、`/alerts/` 和 `/sessions/` |
| `GET /healthz` | 存活检查：`ok` |
| `GET /readyz` | 就绪检查：收到过 Watchdog、飞书连接正常、额度未用完 |
| `GET /metrics` | Beacon 自己的 Prometheus 指标，只回答直连 |
| `POST /hooks/feishu` | `feishu.callback: http` 时飞书的卡片回调 |
| `GET /drill/healthz` | 返回 200，演练期间返回 503（`beacon drill fail 15m`） |

## 错误码 {#error-codes}

| 错误码 | 状态码 | 含义 |
| --- | --- | --- |
| `auth.required` | 401 | 没有带 bearer 令牌 |
| `auth.invalid` | 401 | 令牌不存在或已过期 |
| `auth.scope` | 403 | 凭据缺少这个路径需要的作用域；消息里写明是哪个，对 Agent 还说明它的预设在所有者角色范围内允许什么 |
| `auth.too_many_failures` | 429 | 这个地址失败次数太多；稍后再试 |
| `bad_input` | 400、415 | 请求不合这个路径的要求：不是 JSON、有不认识的字段、值超出范围。消息里写明是哪里 |
| `upstream` | 502、503 | Beacon 依赖的东西没有回应（VictoriaMetrics、存储），或这个 Beacon 不接收这类请求 |
| `alert.not_found` | 404 | 没有这个 id 的告警组 |
| `alert.not_firing` | 409 | 告警组没有在触发，所以不能认领或静默 |
| `ssh.disabled` | 503 | 没有配置 SSH 通道 |
| `ssh.paused` | 409 | Agent 到达机器的通道已暂停 |
| `machine.not_found` | 404 | 没有这个名字的机器 |
| `machine.forbidden` | 403 | 这个令牌不能到达这台机器 |
| `machine.unreachable` | 502 | Beacon 连不上这台机器 |
| `machine.disconnected` | 502 | 机器在 NAT 后面，它的连接程序没有连上 |
| `machine.exists` | 409 | 已有同名的机器；要替换请明确指定 |
| `file.not_found` | 404 | 没有这个文件 |
| `file.not_regular` | 400 | 这个路径不是普通文件 |
| `file.binary` | 415 | 文件不是文本 |
| `file.too_large` | 413 | 文件或内容超过上限 |
| `file.changed` | 409 | 读取之后文件变过；请重新读取 |
| `edit.no_match` | 422 | 文件里没有 `old_string` |
| `edit.ambiguous` | 422 | `old_string` 出现了不止一次；多给些上下文，或设置 `replace_all` |
| `session.not_found` | 404 | 没有这个 id 的会话 |
| `config.not_found` | 404 | 没有这个配置项或这次修改 |
| `config.invalid` | 422 | 修改通不过校验；消息说明原因，什么都没有改 |
| `config.conflict` | 409 | 已有更新的版本生效，或之后的修改动过同一项；重新读取，再做决定 |
| `status.not_found` | 404 | 没有这个名称的状态页 |
| `maintenance.not_found` | 404 | 没有这个 id 的维护窗口 |
| `maintenance.ended` | 409 | 维护窗口已经结束或已取消 |
| `token.too_soon` | 429 | 令牌不到 24 小时前刚续过期，仍然有效 |
| `token.not_renewable` | 403 | 登记的机器的令牌，用 `beacon enroll … -rotate` 换新；或 OAuth 访问令牌，由它的客户端刷新 |
| `token.not_found` | 404 | 没有这个名字的令牌可吊销 |
| `token.not_revocable` | 403 | 这把令牌既不能到达机器，也不属于 Agent；机器和服务的令牌要在主机上用 `beacon token revoke` 吊销 |
| `user.not_found` | 404 | 没有这个名字的成员 |
| `user.last_owner` | 409 | 这样改会让 Beacon 没有启用的所有者；请先让另一个人成为所有者 |
| `key.not_found` | 404 | 调用者没有这个指纹的 SSH 密钥 |
| `key.exists` | 409 | 这个密钥属于另一个 Agent 或令牌，或是机器的连接密钥 |
| `probe.busy` | 429 | 这个目标不到 15 秒前刚按需检测过，或这个令牌已有三个请求在进行 |
| `probe.not_found` | 404 | 没有正在工作的检测点检测这个目标 |
| `probe.unavailable` | 503 | 这个 Beacon 没有可用来检测的网关 |
| `location.inactive` | 403 | 网关：检测点不在目录里，或它的机器已经到期 |
| `changes.too_many` | 429 | 网关：这个令牌这一分钟发了超过 60 个变更事件 |
| `request.not_found` | 404 | 网关：没有这个检测请求，或它已经过期 |
| `not_found` | 404 | 网关：没有这个路径 |
