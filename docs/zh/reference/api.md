---
sourceHash: sha256-9d97f4cb
---

# REST API

Beacon 的 API 在 17001 端口的 `/api/v1` 下，收发 JSON。除了接口说明本身，每个路径都需要一个 bearer 令牌，带有旁边列出的作用域；同一个令牌也用于 MCP 和 SSH 通道，这里的每个操作也都是一个调用同一方法的 [MCP 工具](mcp.md)。OpenAPI 文档（仓库里的 `internal/api/openapi.yaml`，运行中的 Beacon 上的 `GET /api/v1/openapi.yaml`）是正式约定，列有每个字段；这一页是地图。

## 请求与应答 {#requests-and-answers}

```bash
curl -H "Authorization: Bearer $TOKEN" https://status.example.com/api/v1/alerts
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/alerts/0cdfbada62dd/notes -d '{"text": "Restarted the relay; probing again."}'
```

- **令牌**以 `lbt_` 开头，用 `linkos-beacon token create NAME -scopes … -expires …` 创建。Beacon 只保存它们的 SHA-256 哈希；每个令牌都有作用域和有效期，并能按请求自行续期（`POST /api/v1/token/renew`）。
- **请求体**是 JSON（`Content-Type: application/json`）；不认识的字段会被拒绝，这样拼错的字段不会被悄悄忽略。
- **写操作**连同令牌的名字记入审计日志，并回显到告警群。机器上的会话也都有记录。
- **错误**是 `{"error": "…", "code": "…"}`：消息说明出了什么错、怎么改正，错误码保持稳定，客户端可以据此处理。错误码[列在下面](#error-codes)。
- **不可信的文本**：应答的 `untrusted` 对象列出的字段来自 Beacon 之外（标签、被检测的服务、变更的发送方、机器），已去掉控制字符和不可见字符，太长的会截断：它们是数据，不是指令。

## 作用域 {#scopes}

一个令牌带一个或多个作用域。前八个给人和 Agent 用，用于这个 API、MCP 和 SSH 通道；其余的给机器用，用于网关。

| 作用域 | 允许做什么 |
| --- | --- |
| `status:read` | 读取：告警、证据和时间线、变更事件、静默、审计日志、机器和会话、配置 |
| `alerts:write` | 处理告警：认领、静默和结束静默、备注、按需检测 |
| `machines:ssh` | 用令牌上的密钥登录 SSH 通道，机器工具（执行、读、写、改），它能到达的机器和这些机器上的会话 |
| `machines:write` | 添加和删除机器、连接密钥，暂停和恢复 Agent 到达机器 |
| `sessions:read` | 会话的文字记录和录制 |
| `config:write` | 修改 Beacon 监控的内容（目录、检测目标组和检测模块），也能读取 |
| `incidents:write`、`maintenance:write` | 为将来的状态页预留；目前没有路径用到 |
| `probe:write` | 检测点：推送结果，取它的检测目标、检测模块和设置，按需检测。只能以它自己的检测点写入 |
| `host:write` | 主机：推送它的指标。只能以它自己的主机写入 |
| `heartbeat:write` | 推送心跳 |
| `metrics:read` | 经网关查询指标 |
| `alerts:send` | 像规则引擎一样发送告警 |
| `changes:write` | 发送变更事件 |

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
| `GET /api/v1/config/history` | `status:read` 或 `config:write` | 修改记录，新的在前，不带值（`kind`、`key`、`before`、`limit`）；保留最近 10,000 次 |
| `GET /api/v1/config/history/{version}` | `status:read` 或 `config:write` | 一次修改，带每一项改前和改后的值 |
| `POST /api/v1/config/history/{version}/revert` | `config:write` | 把一次修改作为一次新的修改撤销；之后的修改动过其中某项时返回 `config.conflict` |

## 令牌 {#the-token}

| 路径 | 作用域 | 做什么 |
| --- | --- | --- |
| `GET /api/v1/token` | 任意 | 调用者自己的令牌：名字、作用域、机器、SSH 密钥指纹、到期时间、有效期长度、上次续期、能否以及何时可以续期；从不包括密钥本身 |
| `POST /api/v1/token/renew` | 任意 | 一个新的密钥，只显示一次，有效期从现在起重新算；旧密钥还能用十分钟。每 24 小时最多一次；登记的机器的令牌不能续期 |
| `DELETE /api/v1/tokens/{name}` | `machines:write` | 吊销一把能到达机器的令牌（`machines:ssh`）：针对单个 Agent 的暂停开关。检测点、主机、心跳、规则引擎、变更发送方和指标读取方的令牌只能在主机上吊销（`token.not_revocable`） |
| `GET /api/v1/openapi.yaml` | 无需 | OpenAPI 文档 |

## 网关 {#the-gateway}

网关在 17006 端口，给机器用：检测点、主机、心跳、规则引擎和变更事件的发送方。各自发送什么，见[数据来源](../sources.md)。

| 路径 | 作用域 | 做什么 |
| --- | --- | --- |
| `POST /api/v1/import/prometheus` | `probe:write`、`host:write`、`heartbeat:write` | Prometheus 文本格式的样本（`linkos-probe` 推送的就是这种），以及心跳 |
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
| `/mcp` | MCP server，streamable HTTP，用同样的 bearer 令牌（[MCP 工具](mcp.md)） |
| `GET /alerts/{id}`、`GET /sessions/{id}` | 人从卡片点开的页面，凭签名链接或令牌打开（[页面](../pages.md)） |
| `GET /healthz` | 存活检查：`ok` |
| `GET /readyz` | 就绪检查：收到过 Watchdog、飞书连接正常、额度未用完 |
| `GET /metrics` | Beacon 自己的 Prometheus 指标，只回答直连 |
| `POST /hooks/feishu` | `feishu.callback: http` 时飞书的卡片回调 |
| `GET /drill/healthz` | 返回 200，演练期间返回 503（`linkos-beacon drill fail 15m`） |

## 错误码 {#error-codes}

| 错误码 | 状态码 | 含义 |
| --- | --- | --- |
| `auth.required` | 401 | 没有带 bearer 令牌 |
| `auth.invalid` | 401 | 令牌不存在或已过期 |
| `auth.scope` | 403 | 令牌缺少这个路径需要的作用域；消息里写明是哪个 |
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
| `token.too_soon` | 429 | 令牌不到 24 小时前刚续过期，仍然有效 |
| `token.not_renewable` | 403 | 登记的机器的令牌；用 `linkos-beacon enroll … -rotate` 换新 |
| `token.not_found` | 404 | 没有这个名字的令牌可吊销 |
| `token.not_revocable` | 403 | 这把令牌不能到达机器；机器和服务的令牌要在主机上用 `linkos-beacon token revoke` 吊销 |
| `probe.busy` | 429 | 这个目标不到 15 秒前刚按需检测过，或这个令牌已有三个请求在进行 |
| `probe.not_found` | 404 | 没有正在工作的检测点检测这个目标 |
| `probe.unavailable` | 503 | 这个 Beacon 没有可用来检测的网关 |
| `location.inactive` | 403 | 网关：检测点不在目录里，或它的机器已经到期 |
| `changes.too_many` | 429 | 网关：这个令牌这一分钟发了超过 60 个变更事件 |
| `request.not_found` | 404 | 网关：没有这个检测请求，或它已经过期 |
| `not_found` | 404 | 网关：没有这个路径 |
