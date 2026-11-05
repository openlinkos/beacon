---
sourceHash: sha256-84ac89de
---

# REST API

REST API 面向通过 HTTP 使用 Beacon 的脚本、机器人和 Agent：它们用它读告警和证据，留备注，让检测点重测，在机器上干活，修改 Beacon 监控的内容。它在 17001 端口的 `/api/v1` 下（参考部署里是 `https://status.example.com/api/v1`），收发 JSON。这里的每个操作也都是一个 [MCP 工具](mcp.md)，调用的是同一个方法；在这里能用的凭据，在 MCP 上也能用，SSH 通道则凭密钥让同一个 Agent 登录。

列有每个字段的正式约定是 OpenAPI 文档：仓库里的 `internal/api/openapi.yaml`，或者运行中的 Beacon 上的 `GET /api/v1/openapi.yaml`。这一页是地图：每个接口和它需要的作用域、网关给机器用的接口，以及所有错误码。

## 获取凭据 {#getting-a-credential}

除了 `GET /api/v1/openapi.yaml`，每个接口都需要 bearer 凭据，放在 `Authorization: Bearer …` 里。用哪一种，取决于谁来调用：

| 谁来调用 | 凭据 | 从哪里来 |
| --- | --- | --- |
| 独自运行的 Agent、脚本、CI 任务 | Agent 的令牌，`lbt_…` | 控制台的 **Agent** 页面，或在中心上用 `beacon agent add`（[无界面的 Agent 和 CI](../guide/people.md#headless-agents-and-ci)） |
| 由人登录的 MCP 客户端，比如 Claude Code | OAuth 访问令牌，`lba_…`，有效一小时，由客户端刷新 | 通过浏览器登录（[接入 Claude Code](../guide/people.md#connect-claude-code)） |
| 机器或服务（检测点、主机、心跳、变更发送方、指标读取方），或者熟手 | 按名字指定作用域的令牌，`lbt_…` | 在中心上用 `beacon enroll`，或 `beacon token create NAME -scopes … -expires …` |

比如一个处理告警的脚本：

```bash
docker exec beacon beacon agent add ops-script -owner alice -preset oncall > ops-script.token
```

密钥是 `lbt_` 加 52 个字符，只在标准输出打印这一次，使用它的命令打印在标准错误里；Beacon 只保存它的 SHA-256 哈希。令牌到期失效（默认 365 天），到期前可以用 `POST /api/v1/token/renew` 给自己续期（[令牌](#the-token)）；`beacon token list` 列出所有令牌，`beacon token revoke NAME` 立即作废一个。Agent 的凭据能做什么，取决于它的[预设和所有者的角色](#scopes)。

## 请求与应答 {#requests-and-answers}

```bash
curl -H "Authorization: Bearer $TOKEN" https://status.example.com/api/v1/alerts
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/alerts/0cdfbada62dd/notes -d '{"text": "Restarted the relay; probing again."}'
```

- **请求体**是 JSON（`Content-Type: application/json`）。带了不认识的字段，整个请求会被拒绝，所以拼错的字段不会被悄悄忽略。
- **写操作**连同操作者记入审计日志（Agent 记为 `alice / claude-code`，其他令牌记为令牌自己的名字），并回显到告警群。机器上的会话也都有记录。
- **错误**是 `{"error": "…", "code": "…"}`：消息说明哪里出错、怎么改正；错误码保持稳定，客户端可以据此处理。错误码[列在下面](#error-codes)。`401`（`auth.required`、`auth.invalid`）在这里和网关上还带有 `"docs": "https://beacon.openlinkos.com/reference/api"`，指向本页的英文版，方便第一次接触 Beacon 的人。
- **失败次数**按客户端地址计算：10 分钟内失败 20 次，这个地址在这 10 分钟剩下的时间里会被挡住（`auth.too_many_failures`）。消息里写明还要等多久，响应带 `Retry-After`。网关同样如此。`POST /oauth/token` 回答 429 `too_many_requests`，`error_description` 里是同一段等待，响应同样带这个头。MCP 的 401 正文里写明等待时间，并带 `Retry-After`。
- **不可信文本**：应答里 `untrusted` 对象列出的字段来自 Beacon 之外（标签、被检测的服务、变更的发送方、机器），已去掉控制字符和不可见字符，太长的会截断。把它们当作数据，不要当作指令。

## 作用域 {#scopes}

一个凭据带一个或多个作用域。前十个给人和 Agent 用，用于这个 API、MCP 和 SSH 通道；其余的给机器用，用于网关。人的作用域来自角色，Agent 的来自预设，且不超出所有者的角色；只有机器的令牌和熟手用的令牌是按名字指定作用域创建的。

| 作用域 | 允许做什么 |
| --- | --- |
| `status:read` | 读取：告警、证据和时间线、变更事件、静默、审计日志、机器和会话、配置、状态页和维护窗口 |
| `alerts:write` | 处理告警：认领、静默和结束静默、备注、按需重测 |
| `machines:ssh` | 用 Agent 自己的密钥登录 SSH 通道，使用机器工具（执行、读、写、改），查看它能到达的机器和这些机器上的会话 |
| `machines:write` | 添加和删除机器、设置连接器密钥、暂停和恢复 Agent 上机器、吊销能上机器的令牌和 Agent 的令牌 |
| `sessions:read` | 会话的文字记录和录像 |
| `config:write` | 修改 Beacon 监控的内容（目录、检测目标集和检测模块）和状态页，也能读取 |
| `secrets:admin` | 修改密钥托管所依赖的系统设置（[见下](#settings)），也能读取；每次修改都有版本、有审计、有群内回声。默认只有所有者有 |
| `accounts:write` | 管理成员：列出、邀请、修改角色、停用、生成重设密码链接；在控制台上归入旧令牌、管理所有人的 Agent。只有所有者有；没有哪个 Agent 预设给它 |
| `maintenance:write` | 安排和结束维护窗口：状态页会提前公告，窗口内相关服务的告警会被静默 |
| `incidents:write` | 留给在状态页上手写事件；目前还没有接口用到（事件来自告警组） |
| `probe:write` | 检测点：推送结果，获取它的检测目标、检测模块和设置，按需重测。只能以它自己的检测点身份写入 |
| `host:write` | 主机：推送它的指标。只能以它自己的主机身份写入 |
| `heartbeat:write` | 推送心跳 |
| `metrics:read` | 通过网关查询指标 |
| `alerts:send` | 像规则引擎一样发送告警 |
| `changes:write` | 发送变更事件 |

人和 Agent 不用一个个挑作用域：人的作用域来自角色，Agent 的由预设申请，且不超出所有者的角色（[成员和 Agent](../guide/people.md#roles)）。

| 角色 | Agent 预设 | 作用域 |
| --- | --- | --- |
| 查看者 | 只读 | `status:read`、`sessions:read` |
| 值班 | 值班 | + `alerts:write`、`machines:ssh` |
| 运维 | 运维 | + `maintenance:write`、`incidents:write`、`machines:write` |
| 所有者 | | + `config:write`、`accounts:write` |

Agent 要用一个作用域，得它的预设给了、它的所有者的角色也给了，每次请求时都会检查：降低或停用所有者，会立即限制这个 Agent。OAuth 登录还另外受成员当初允许的预设和机器限制。被拒绝时（`auth.scope`），消息会说明这个 Agent 的预设在所有者角色范围内允许什么。

`GET /api/v1/token` 和 `POST /api/v1/token/renew` 不限作用域：令牌本身就是凭据。

## 告警 {#alerts}

| 接口 | 作用域 | 做什么 |
| --- | --- | --- |
| `GET /api/v1/bridge` | `status:read` | 告警现在能否送到人（`ready`；不能时 `not_ready` 列出原因：没收到规则引擎的 Watchdog、飞书连接断了、最近 10 分钟有飞书调用失败、额度用完），按级别统计的正在触发的告警组，已认领的数量，本月飞书 API 调用次数和额度 |
| `GET /api/v1/alerts` | `status:read` | 告警组，正在触发的在前，新的在前；`?state=all` 加上最近七天恢复的 |
| `GET /api/v1/alerts/{id}` | `status:read` | 一个告警组：标签、处理手册和面板、认领、静默、升级、备注、唤醒过的 Agent，以及 `page`，即它在控制台里那一页的签名链接 |
| `POST /api/v1/alerts/{id}/ack` | `alerts:write` | 认领一个正在触发的告警组，可以带 `note`：升级到手机暂停 `ack_ttl` 这么久（默认 4 小时） |
| `GET /api/v1/silences` | `status:read` | 生效中和待生效的静默，最先结束的在前 |
| `POST /api/v1/silences` | `alerts:write` | 静默一个正在触发的告警组：`alert`、`duration`（最长 24 小时）和必填的 `comment`。同时认领它 |
| `DELETE /api/v1/silences/{id}` | `alerts:write` | 立即结束一个静默；它覆盖的告警重新开始通知 |
| `GET /api/v1/audit` | `status:read` | 最近的写操作，新的在前，带操作者和途径（`limit`，默认 100，最多 1000） |

## 处理告警 {#working-an-alert}

| 接口 | 作用域 | 做什么 |
| --- | --- | --- |
| `GET /api/v1/alerts/{id}/evidence` | `status:read` | 判断一个告警组需要的一切，一次给全：每个检测点的结果和失败原因、相关告警组、变更事件、已经做了什么、链接（[让 Agent 值班](../guide/on-call.md#the-evidence)）。时间窗口从告警组开始前 30 分钟算起；`before` 可以往前放宽，最多 `24h` |
| `GET /api/v1/alerts/{id}/timeline` | `status:read` | 告警组的来龙去脉，按时间排列，覆盖证据的时间窗口 |
| `POST /api/v1/alerts/{id}/notes` | `alerts:write` | 留一条备注，`text` 最多 2000 字符，正在触发和已恢复的告警组都可以。备注不算认领 |
| `GET /api/v1/changes` | `status:read` | 变更事件，新的在前，可以按 `service`、`machine`、`source` 和 `since`（`2h`、`7d` 或一个时间；默认最近一天）筛选，`limit` 最多 1000 |
| `POST /api/v1/probe-now` | `alerts:write` | 让检测某个目标的每个检测点立刻重测，并拿到每个检测点的结果：`alert_id`、`target`（可以带 `module`）或 `service`；`wait` 秒数，默认 45，最多 90 |

## 机器与会话 {#machines-and-sessions}

| 接口 | 作用域 | 做什么 |
| --- | --- | --- |
| `GET /api/v1/ssh` | `status:read` | SSH 通道的状态（是否暂停）、它的主机密钥，以及机器信任的 CA 公钥 |
| `POST /api/v1/ssh/pause` | `machines:write` | 关掉所有 Agent 在机器上的会话，并拒绝新会话 |
| `POST /api/v1/ssh/resume` | `machines:write` | 让 Agent 重新能上机器 |
| `GET /api/v1/machines` | `status:read` 或 `machines:ssh` | 机器列表、调用者能否到达每台机器、连接器的状态（`connector`、`connected`、`connected_since`），以及给 Agent 自己的 ssh 用的一段 `ssh_config` 和一行 `known_hosts` |
| `POST /api/v1/machines` | `machines:write` | 添加一台机器并固定它的主机密钥；NAT 后面的机器用 `address: connector`，带上它连出来用的密钥。返回机器上需要做的设置 |
| `DELETE /api/v1/machines/{name}` | `machines:write` | 移除一台机器；它的会话记录保留 |
| `PUT /api/v1/machines/{name}/connector` | `machines:write` | 设置或更换 NAT 后面的机器连出来用的密钥；用旧密钥建立的连接会被关掉 |
| `POST /api/v1/machines/{name}/run` | `machines:ssh` | 以机器上的用户身份，在 `sh` 里、不分配终端执行一条命令（`command`、`cwd`、`stdin`、`timeout` 秒数，默认 120，最多 600）：返回退出码、stdout 和 stderr |
| `GET /api/v1/machines/{name}/file` | `machines:ssh` | 文本文件的若干行，带行号（`path`、`offset`、`limit`：默认 2000 行） |
| `PUT /api/v1/machines/{name}/file` | `machines:ssh` | 写入整个文件，最多 1 MiB，需要时创建目录 |
| `PATCH /api/v1/machines/{name}/file` | `machines:ssh` | 替换一段完全一致的文本，一处或全部（`old_string`、`new_string`、`replace_all`）；读取之后文件变过则拒绝 |
| `GET /api/v1/sessions` | `status:read` 或 `machines:ssh` | 机器上的会话，新的在前，可以按 `machine` 和 `agent` 筛选（`limit`，默认 50，最多 1000）：谁、在哪里、做了什么、什么时候、退出码 |
| `GET /api/v1/sessions/{id}` | `sessions:read` 或 `machines:ssh` | 会话的文字记录：命令的输出，或者交互 shell 的每条命令及其输出和退出码 |
| `GET /api/v1/sessions/{id}/recording` | `sessions:read` 或 `machines:ssh` | 录像文件，asciicast v3，可以用 asciinema 回放 |

## 配置 {#configuration}

Beacon 监控的内容，每一项都按它在文件里的格式读写（[检测什么，从哪里检测](../guide/monitoring.md)）。读取需要 `status:read` 或 `config:write`；修改需要 `config:write`。`PUT` 和 `DELETE` 接受 `?note=`（随修改保存，并显示在告警群里）和 `?if_version=`（已有更新的版本生效时拒绝，返回 `config.conflict`）。通不过校验的修改会被拒绝，返回 `config.invalid`，什么都不会改。

| 接口 | 作用域 | 做什么 |
| --- | --- | --- |
| `GET /api/v1/config` | `status:read` 或 `config:write` | 全部配置，带当前生效的版本 |
| `GET /api/v1/config/locations` | `status:read` 或 `config:write` | 检测点，按 id |
| `GET /api/v1/config/locations/{id}` | `status:read` 或 `config:write` | 一个检测点 |
| `PUT /api/v1/config/locations/{id}` | `config:write` | 添加或替换一个检测点 |
| `DELETE /api/v1/config/locations/{id}` | `config:write` | 删除一个检测点；还有只以它命名的检测目标集时拒绝 |
| `GET /api/v1/config/heartbeats` | `status:read` 或 `config:write` | 心跳，按名字 |
| `GET /api/v1/config/heartbeats/{name}` | `status:read` 或 `config:write` | 一个心跳 |
| `PUT /api/v1/config/heartbeats/{name}` | `config:write` | 添加或替换一个心跳 |
| `DELETE /api/v1/config/heartbeats/{name}` | `config:write` | 删除一个心跳 |
| `GET /api/v1/config/resources` | `status:read` 或 `config:write` | 到期资源，按名字 |
| `GET /api/v1/config/resources/{name}` | `status:read` 或 `config:write` | 一项资源 |
| `PUT /api/v1/config/resources/{name}` | `config:write` | 添加或替换一项资源 |
| `DELETE /api/v1/config/resources/{name}` | `config:write` | 删除一项资源 |
| `GET /api/v1/config/targets` | `status:read` 或 `config:write` | 检测目标集，按名字，带上检测每一集的检测点 |
| `GET /api/v1/config/targets/{name}` | `status:read` 或 `config:write` | 一个检测目标集里的各个分组 |
| `PUT /api/v1/config/targets/{name}` | `config:write` | 添加或替换一个检测目标集；检测点在一分钟内拿到 |
| `DELETE /api/v1/config/targets/{name}` | `config:write` | 删除一个检测目标集 |
| `GET /api/v1/config/modules` | `status:read` 或 `config:write` | 检测模块，按名字 |
| `GET /api/v1/config/modules/{name}` | `status:read` 或 `config:write` | 一个检测模块 |
| `PUT /api/v1/config/modules/{name}` | `config:write` | 添加或替换一个检测模块；模块文件里的其他模块和注释保持不变 |
| `DELETE /api/v1/config/modules/{name}` | `config:write` | 删除一个检测模块 |
| `GET /api/v1/config/status_pages` | `status:read` 或 `config:write` | 公开状态页，按名字（[状态页](../guide/status-pages.md)） |
| `GET /api/v1/config/status_pages/{name}` | `status:read` 或 `config:write` | 一个状态页的定义 |
| `PUT /api/v1/config/status_pages/{name}` | `config:write` | 添加或替换一个状态页；组件对应的服务还没有检测目标时照样接受，并给出警告 |
| `DELETE /api/v1/config/status_pages/{name}` | `config:write` | 删除一个状态页 |
| `GET /api/v1/config/history` | `status:read` 或 `config:write` | 修改记录，新的在前，不带具体的值（`kind`、`key`、`before` 一个版本号、`limit` 最多 1000）；保留最近 10,000 次 |
| `GET /api/v1/config/history/{version}` | `status:read` 或 `config:write` | 一次修改，带每一项改前和改后的值 |
| `POST /api/v1/config/history/{version}/revert` | `config:write` | 以一次新修改的方式撤销某次修改；之后的修改动过其中某一项时返回 `config.conflict` |

## 系统设置 {#settings}

密钥托管所依赖的系统设置：申请与授权的限度、审核链、限流与异常、保留期、导入级别、备份与存储租约——每一项都有开箱默认值，每次修改都是带谁改的与原因的版本、可回滚，每一行都带签名，直接改数据库不会生效（控制台的「系统设置」页全部展示）。读取要 `status:read` 或 `secrets:admin`；修改要 `secrets:admin` 和 `reason`。MCP 的配置工具以 `settings` 类提供同一套操作。

| 路由 | 作用域 | 说明 |
| --- | --- | --- |
| `GET /api/v1/settings` | `status:read` 或 `secrets:admin` | 全部设置项：默认值、系统层生效值、值来自哪一层、谁何时为何改的；其他作用域的行在 `overrides`；签名不符的行在 `bad_rows`（视为不存在） |
| `GET /api/v1/settings/{key}` | `status:read` 或 `secrets:admin` | 一个设置项按 `?collection=`、`?tag=`（可多个）、`?entitlement=`、`?request=` 逐层解析：越具体越优先，标签取更严，申请夹在上一级上限内；`levels` 列出经过的每一层 |
| `PUT /api/v1/settings/{key}` | `secrets:admin` | 修改一个设置项。请求体 `{"value": …, "reason": "…", "scope": "system", "target": ""}`；`scope` 与 `target` 可把值放在库、标签、资格或申请上。值必须符合要求（`settings.invalid`）；行在此期间变过时 `if_version` 拒绝（`settings.conflict`） |
| `DELETE /api/v1/settings/{key}` | `secrets:admin` | 重置一个设置项，由上一级重新生效（该行删除）；`?scope=`、`?target=`、`?reason=`、`?if_version=` |
| `GET /api/v1/settings/history` | `status:read` 或 `secrets:admin` | 修改历史，最新在前（`key`、`scope`、`target`、`before` 某版本、`limit` 至多 200） |
| `GET /api/v1/settings/history/{version}` | `status:read` 或 `secrets:admin` | 一次修改，含改前与改后的值 |
| `POST /api/v1/settings/history/{version}/revert` | `secrets:admin` | 把一次修改撤销为一次新修改；同一行被更晚的修改动过时 `settings.conflict` |

## 保管库 {#vault}

密钥保管库：库里的条目，值由 Beacon 代为保管。条目的概要——标题、类别、标签、网址、字段名——是明文，供搜索与策略使用；全部字段值、用户名、笔记与验证码种子在详情里，按版本用该库的密钥加密。版本保留谁何时为何改了什么；附件走同一套加密，单个不超过 `attachments.max_size`（10 MB）。每一条策略行——库、人员权限、标签、条目——都带一条由根密钥派生的签名密钥算出的签名，直接改数据库不会生效：该行视为不存在并上报（下方的 `rows_failed_signature`）。同一套操作也是控制台的「保管库」页与同名 MCP 工具；根密钥从哪来见 [config.yaml](config.md#the-vault)。

条目路由给人用，每次修改都记审计并回声到告警群。不是人的令牌被拒（`secrets.person`），Agent 一律被拒（`secrets.agent`）：Agent 经由后续版本到来的授权取条目。查看值要重新输入账号密码，窗口为 `reveal.reauth_window`（默认 5 分钟），并且先写审计再返回值——与人将来取值、Agent 经授权取值同一规矩。owner 对所有库有 `manage`；其他人按库所给的档位：`describe`（看概要）、`read`（看值，需密码）、`edit`（改条目）、`manage`（改权限与策略）。保管库封存期间（口令模式重启后、owner 启封前）这些路由返回 `secrets.sealed` 并告警；监控与 SSH 通道照常。

| 路由 | 作用域 | 说明 |
| --- | --- | --- |
| `GET /api/v1/vault` | 任意凭据 | 状态：`sealed`、`mode`（`file`、`passphrase`、`aws-kms`、`gcp-kms`、`transit`）与签名不符的策略行 `rows_failed_signature` |
| `POST /api/v1/vault/unseal` | 任意凭据 | 用 `{"passphrase"}` 启封口令模式；口令不对是 `secrets.wrong_passphrase` |
| `POST /api/v1/vault/seal` | `secrets:admin` | 重新封存（口令模式）：密钥功能等待口令 |
| `POST /api/v1/vault/rotate-key` | `secrets:admin` | 换根密钥：全部库密钥重新包裹，秒级完成。口令模式下 `{"passphrase"}` 是新口令 |
| `POST /api/v1/vault/rotate-collection-key` | `secrets:admin` | 换一个库的密钥（`{"collection"}`）；该库条目在后台重新加密。移除成员不需要换钥：成员从来不经手密钥 |
| `GET /api/v1/vault/collections` | 任意凭据 | 全部库：名字、说明、审批级别、密钥版本 |
| `POST /api/v1/vault/collections` | `secrets:admin` | 建库（`name`、`description`、`level`：条目继承的审批级别） |
| `GET /api/v1/vault/collections/{id}` | 人 | 一个库，以及调用者能否在其中读值 |
| `PUT /api/v1/vault/collections/{id}` | `secrets:admin` | 改库的说明或级别 |
| `GET /api/v1/vault/collections/{id}/items` | 人，`describe` | 条目清单（`?state=normal`、`archived` 或 `trash`）：标题、版本、状态、标签——概要，绝无值 |
| `POST /api/v1/vault/collections/{id}/items` | 人，`edit` | 加条目：`title`、`category`、`urls`、`tags`、`level`（空则继承库的）、`actions`、`fields`（每个 `section`、`name`、`type`——`concealed`、`string`、`email`、`url`、`otp`、`date`、`file`——`purpose`、`value`）、`notes`、`attachments`（`name`、base64 `data`）、`reason` |
| `GET /api/v1/vault/collections/{id}/perms` | 人，`manage` | 谁在这个库里能做什么 |
| `POST /api/v1/vault/collections/{id}/perms` | 人，`manage` | 给人或角色授一档权限：`{"subject": "user:alice"}` 或 `role:oncall`，`perm` 为 `describe`、`read`、`edit`、`manage` 之一（空为移除），`reason` |
| `GET /api/v1/vault/items/{id}` | 人，`describe` | 一个条目：所在库、生效的审批级别、各版本（谁、何时、为何、字段清单——绝无密文）、`who_can_get`（授权，后续版本）与它的 `ref`，`beacon://collection/item[/section/field]`。调用者连 describe 都没有的条目返回 `secrets.not_found`，而不是拒绝 |
| `PUT /api/v1/vault/items/{id}` | 人，`edit` | 改条目；请求体与新增相同，实际要求带 `reason`，产生一个新版本 |
| `DELETE /api/v1/vault/items/{id}` | 人，`edit` | 把条目移入回收站，`retention.trash`（30 天）保留 |
| `POST /api/v1/vault/items/{id}/archive` | 人，`edit` | 归档条目：保留、让开 |
| `POST /api/v1/vault/items/{id}/restore` | 人，`edit` | 从归档或回收站恢复条目 |
| `POST /api/v1/vault/items/{id}/rollback` | 人，`edit` | 让旧版本重新成为当前版本（`version`、`reason`）；各版本仍在 |
| `POST /api/v1/vault/items/{id}/generate` | 人，`edit` | 让 Beacon 为一个字段生成新值（`field`、`section`、`length`——默认 24——`charset`：`letters`、`alnum` 或 `all`、`reason`）：作为新版本保存，值本身不返回；要看就 reveal |
| `POST /api/v1/vault/items/{id}/reveal` | 人，`read` | 值（`fields` 指定字段名，不给则全部；`password` 为账号密码）。`otp` 字段返回当前验证码与剩余秒数，绝无种子。先写审计，值才离开 |
| `POST /api/v1/vault/import` | `secrets:admin`，人 | 导入一份已解析的批次：`batch`（`source`——`1password`、`chrome`、`bitwarden`、`csv`——及其 `collections`，条目的 `fields` 带着各自的值；即 `beacon secret import` 与控制台导入向导所发）和 `options`（`dry_run`、`on_conflict`：`ask`/`new_version`/`skip`/`keep_both`、按库名的 `levels`、`collection` 把整批并入一个库、`reason`）。返回报告：每库的条数、类别分布与起始级别，以及冲突——同一处地方（网址规范化后相同）且同一用户名即视为同一条——绝无任何值。dry-run 不写任何东西，其批次可以完全不带值；已存在的目标库需要 `edit` |
| `GET /api/v1/vault/items/{id}/versions/{version}/attachments/{name}` | 人，`read` | 一个附件的字节 |
| `GET /api/v1/vault/search` | 人，`describe` | 标题、类别、标签或字段名匹配 `q` 的条目（`?collection=`、`?tag=` 可多个），只在调用者有 describe 的库里找 |
| `GET /api/v1/vault/tags` | 任意凭据 | 全部标签：名字（小写、`/` 分层）、审批级别、说明 |
| `POST /api/v1/vault/tags` | 人，owner | 设置标签（`name`、`level`、`description`）：标签的级别是策略——带它的条目至少那么严——所以只有 owner 能改 |
| `DELETE /api/v1/vault/tags/{name}` | 人，owner | 删除标签；条目上的名字保留 |

## 状态页与维护 {#status-pages-and-maintenance}

公开页面本身不需要令牌（[见下文](#other-routes-on-port-17001)）；这些接口把同样的内容给 Agent，并让它安排维护（[状态页](../guide/status-pages.md)）。

| 接口 | 作用域 | 做什么 |
| --- | --- | --- |
| `GET /api/v1/status/{name}` | `status:read` | 状态页的公开 JSON：每个组件现在和 90 天来的状态、事件及其公开进展、维护窗口；`?lang=` |
| `GET /api/v1/maintenance` | `status:read` | 维护窗口，开始得越晚越靠前；结束的保留 91 天 |
| `POST /api/v1/maintenance` | `maintenance:write` | 安排一个窗口：`services`、`starts_at`（不填就是现在）、`ends_at`（最多 7 天后）、`title`、`description`。状态页会公告它，每个服务一条静默，窗口内的告警不会叫醒人 |
| `DELETE /api/v1/maintenance/{id}` | `maintenance:write` | 立即结束一个窗口，或在开始前取消 |

## 成员与 SSH 密钥 {#people-and-ssh-keys}

登录 Beacon 的成员，供所有者的凭据使用；以及调用者自己用于 SSH 通道的 SSH 密钥（[成员和 Agent](../guide/people.md)）。没有哪个 Agent 预设给 `accounts:write`：只有所有者在命令行上把这个作用域给了 Agent，Agent 才能管理成员。重设密码没有 MCP 工具。

| 接口 | 作用域 | 做什么 |
| --- | --- | --- |
| `GET /api/v1/users` | `accounts:write` | 成员（用户名、显示名、角色、最近登录、是否停用、他们的 Agent；从不包括密码或它的哈希）和尚未使用的邀请 |
| `POST /api/v1/users/invitations` | `accounts:write` | 邀请一个人，带 `role` 和 `note`：一个一次性链接，24 小时内有效，只在应答里出现 |
| `PATCH /api/v1/users/{name}` | `accounts:write` | 修改成员的 `role`、`display_name` 或 `feishu_open_id`（联系信息，从不用于登录），或设置 `disabled`；立即作用于他的会话和 Agent。Beacon 始终保留一个启用的所有者（`user.last_owner`） |
| `POST /api/v1/users/{name}/reset` | `accounts:write` | 一个一次性的重设密码链接，30 分钟内有效，只在应答里出现 |
| `GET /api/v1/ssh-keys` | `machines:ssh` | 调用者自己的 SSH 密钥；Agent 的密钥属于它的 Agent，无论哪个凭据来问，Agent 自己登记的密钥会写明经由哪个凭据（`via`：`token:NAME` 或 `grant:ID`） |
| `POST /api/v1/ssh-keys` | `machines:ssh` | 登记一个公钥：`key`（一行 `authorized_keys`）和 `comment`。Agent 的密钥属于它登记时所用的凭据：那个令牌或 OAuth 登录有效时它才有效，随它一起移除。一个密钥只属于一个 Agent 或令牌，绝不会是机器的连接器密钥（`key.exists`） |
| `DELETE /api/v1/ssh-keys` | `machines:ssh` | 移除一个，`?fingerprint=SHA256:…`；凭它登录的连接会被关闭 |

## 令牌 {#the-token}

| 接口 | 作用域 | 做什么 |
| --- | --- | --- |
| `GET /api/v1/token` | 不限 | 调用者：是谁（`actor`、`user`、`agent`、`preset`），用的什么凭据（`token`，或带 `client` 的 `oauth`），现在能做什么、机器、SSH 密钥指纹、到期时间、有效期长度、上次续期、能否续期以及何时可以续期；从不返回密钥本身 |
| `POST /api/v1/token/renew` | 不限 | 一个新的密钥，只显示一次，有效期从现在起重新算；旧密钥还能用十分钟。每 24 小时最多一次；登记的机器的令牌不能续期，OAuth 访问令牌也不能，它由客户端刷新 |
| `DELETE /api/v1/tokens/{name}` | `machines:write` | 吊销一个能上机器的令牌（`machines:ssh`），或任何 Agent 的令牌：只针对一个 Agent 的暂停开关，它用这个令牌在机器上打开着的会话立即关闭。检测点、主机、心跳、规则引擎、变更发送方和指标读取方的令牌只能在中心上吊销（`token.not_revocable`） |
| `GET /api/v1/openapi.yaml` | 不需要 | OpenAPI 文档 |

## 网关 {#the-gateway}

网关在 17006 端口（参考部署里是 `https://metrics.example.com`），给机器用：检测点、主机、心跳、规则引擎和变更事件的发送方。它们各用自己的令牌：检测点、主机和心跳的令牌由 `beacon enroll` 创建，变更发送方和指标读取方的令牌由 `beacon token create` 创建，规则引擎用规则令牌（`rules.token_file`）。各自发送什么，见[数据来源](sources.md)。

| 接口 | 作用域 | 做什么 |
| --- | --- | --- |
| `POST /api/v1/import/prometheus` | `probe:write`、`host:write`、`heartbeat:write` | Prometheus 文本格式的样本（`beacon-probe` 推送的就是这种），以及心跳 |
| `POST /api/v1/write` | `probe:write`、`host:write` | Prometheus remote write |
| `GET /api/v1/targets` | `probe:write` | 这个检测点要检测的目标 |
| `GET /api/v1/modules` | `probe:write` | 检测模块文件，逐字节原样返回 |
| `GET /api/v1/agent` | `probe:write` | 检测点的设置：它的 id、检测超时和重试次数 |
| `GET /api/v1/agent/requests` | `probe:write` | 按需重测：长轮询（`?wait=25s`，最长 55 秒），等待要立刻检测的目标 |
| `POST /api/v1/agent/requests/{id}` | `probe:write` | 检测点交回某个请求的检测结果 |
| `POST /api/v2/alerts` | 规则令牌，或 `alerts:send` | 告警，Alertmanager API v2 的格式 |
| `POST /api/v1/changes` | `changes:write` | 一个变更事件：一次部署、一次配置修改。每个令牌每分钟最多 60 个 |
| `POST /api/v1/changes/flux` | `changes:write` | Flux notification-controller 的事件，来自它的 `generic` provider |
| `GET /api/v1/query`、`query_range`、`series`、`labels`、`label/{name}/values`、`metadata`、`status/{what}`、`export`、`rules`、`alerts` | `metrics:read` | 转给 VictoriaMetrics 和 vmalert 的查询，转发时去掉令牌 |

## 17001 端口上的其他路径 {#other-routes-on-port-17001}

| 路径 | 做什么 |
| --- | --- |
| `/mcp` | MCP server，streamable HTTP，用同样的 bearer 令牌或 OAuth 访问令牌；`401` 会指明去哪里登录（`WWW-Authenticate: Bearer resource_metadata="…"`）（[MCP 工具](mcp.md)） |
| `GET /` | 公开：默认状态页，和 `/status` 的内容相同；没有状态页时，是一个写明这套部署、带**登录**按钮的页面（[状态页](../guide/status-pages.md#branding)） |
| `GET /console`、`GET /console/sign-in` 和 `/console` 下的页面 | 控制台，给用自己账号登录的人用；再保留一个版本，也可以用令牌登录（[控制台](../guide/console.md)） |
| `GET /.well-known/oauth-protected-resource/mcp`、`GET /.well-known/oauth-protected-resource`、`GET /.well-known/oauth-authorization-server` | MCP 客户端用来找到 Beacon 登录方式的 OAuth 元数据（RFC 9728、RFC 8414） |
| `POST /oauth/register`、`GET /oauth/authorize`、`POST /oauth/token`、`POST /oauth/revoke` | Beacon 作为 MCP 客户端的授权服务器：注册、授权页面、令牌和吊销（[成员和 Agent](../guide/people.md#the-oauth-endpoints)） |
| `GET /console/alerts/{id}`、`GET /console/sessions/{id}` | 人从卡片上打开的页面，凭签名链接或令牌打开（[控制台](../guide/console.md#signed-links)） |
| `GET /alerts/{id}`、`GET /sessions/{id}`、`GET /sign-in` 以及控制台的其他旧路径 | `308` 转到 `/console` 下的同一路径，带着查询参数；为旧路径签名的链接换成新路径的签名，到期时间不变 |
| `GET /status`、`GET /status/{name}`、`GET /status/{name}/incidents/{id}` | 公开状态页和其中的事件，不需要令牌（[状态页](../guide/status-pages.md)）；在状态页自己的域名上是 `/` 和 `/incidents/{id}` |
| `GET /status.json`、`GET /status/{name}.json`、`GET /status/{name}/feed.atom`、`GET /status/{name}/badge.svg`、`GET /status/{name}/components/{component}/badge.svg` | 状态页的 JSON、Atom 订阅和徽章，公开，可以缓存 |
| `GET /robots.txt` | 允许搜索引擎读取 `/`、`/status` 和 `/static/`，不让它们进 `/console`、`/api/`、`/mcp`、`/alerts/` 和 `/sessions/` |
| `GET /healthz` | 存活检查：`ok` |
| `GET /readyz` | 就绪检查：收到过 Watchdog、飞书连接正常、额度没用完 |
| `GET /metrics` | Beacon 自己的 Prometheus 指标，只回答直连 |
| `POST /hooks/feishu` | `feishu.callback: http` 时飞书的卡片回调 |
| `GET /drill/healthz` | 返回 200；演练期间返回 503（`beacon drill fail 15m`） |

## 错误码 {#error-codes}

| 错误码 | 状态码 | 含义 |
| --- | --- | --- |
| `auth.required` | 401 | 没有带 bearer 令牌 |
| `auth.invalid` | 401 | 令牌不存在或已过期 |
| `auth.scope` | 403 | 凭据缺少这个接口需要的作用域；消息里写明是哪个，对 Agent 还会说明它的预设在所有者角色范围内允许什么 |
| `auth.too_many_failures` | 429 | 这个地址失败次数太多；消息里写明还要等多久，响应带 `Retry-After` |
| `bad_input` | 400、415 | 请求不符合这个接口的要求：不是 JSON、有不认识的字段、值超出范围。消息里写明是哪一处 |
| `upstream` | 502、503 | Beacon 依赖的东西没有回应（VictoriaMetrics、存储），或者这个 Beacon 不接收这类请求 |
| `audit.unavailable` | 500 | 审计日志读不了 |
| `alert.not_found` | 404 | 没有这个 id 的告警组 |
| `alert.not_firing` | 409 | 告警组不在触发中，不能认领或静默 |
| `ssh.disabled` | 503 | 没有配置 SSH 通道 |
| `ssh.paused` | 409 | Agent 上机器的通道已暂停 |
| `machine.not_found` | 404 | 没有这个名字的机器 |
| `machine.forbidden` | 403 | 调用者不能到达这台机器 |
| `machine.unreachable` | 502 | Beacon 连不上这台机器 |
| `machine.disconnected` | 502 | 机器在 NAT 后面，它的连接器没有连上 |
| `machine.exists` | 409 | 已有同名机器；要替换请明确指定 |
| `file.not_found` | 404 | 没有这个文件 |
| `file.not_regular` | 400 | 这个路径不是普通文件 |
| `file.binary` | 415 | 文件不是文本 |
| `file.too_large` | 413 | 文件或内容超过上限 |
| `file.changed` | 409 | 读取之后文件变过；请重新读取 |
| `edit.no_match` | 422 | 文件里没有 `old_string` |
| `edit.ambiguous` | 422 | `old_string` 出现了不止一次；多带一些上下文，或者设置 `replace_all` |
| `session.not_found` | 404 | 没有这个 id 的会话 |
| `config.not_found` | 404 | 没有这个配置项或这次修改 |
| `config.invalid` | 422 | 修改通不过校验；消息说明原因，什么都没有改 |
| `config.conflict` | 409 | 已有更新的版本生效，或者之后的修改动过同一项；重新读取，再做决定 |
| `settings.not_found` | 404 | 没有这个版本的设置修改，或该设置在这一作用域没有设置 |
| `settings.unknown` | 404 | 没有这个键的设置项 |
| `settings.invalid` | 422 | 值不符合该设置项的要求；消息说明原因，什么都没有改 |
| `settings.conflict` | 409 | 之后的修改动过同一设置行；重新读取后再试 |
| `secrets.sealed` | 503 | 保管库处于封存状态（口令模式重启后）；owner 输入口令启封 |
| `secrets.wrong_passphrase` | 403 | 口令打不开这个保管库 |
| `secrets.reauth` | 401 | 查看值要重新输入账号密码（重新打开 `reveal.reauth_window` 窗口） |
| `secrets.agent` | 403 | Agent 来问；Agent 经由授权取条目，授权随后续版本到来 |
| `secrets.person` | 403 | 保管库的条目路由给人用；请用账号登录 |
| `secrets.permission` | 403 | 调用者在这个库里做不了这件事 |
| `secrets.not_found` | 404 | 没有这个库、条目、标签或字段 |
| `secrets.exists` | 409 | 已有这个名字的库或标签 |
| `secrets.conflict` | 409 | 自调用者上次看到之后它变过；重新读取 |
| `secrets.attachment_too_large` | 413 | 附件超过 `attachments.max_size` |
| `secrets.invalid` | 422 | 条目不符合要求（级别、字段、值）；消息说明原因，什么都没有改 |
| `secrets.bad_input` | 400 | 请求不是路由要的形状；消息说明是哪里 |
| `secrets.crypto` | 500 | 密文在该密钥下验证不过——这一行不是它自称的那一行 |
| `status.not_found` | 404 | 没有这个名字的状态页 |
| `maintenance.not_found` | 404 | 没有这个 id 的维护窗口 |
| `maintenance.ended` | 409 | 维护窗口已经结束或已取消 |
| `token.too_soon` | 429 | 令牌在 24 小时内刚续过期，仍然有效 |
| `token.not_renewable` | 403 | 这是登记的机器的令牌，用 `beacon enroll … -rotate` 换新；或者是 OAuth 访问令牌，由它的客户端刷新 |
| `token.not_found` | 404 | 没有这个名字的令牌可以吊销 |
| `token.not_revocable` | 403 | 这个令牌既不能上机器，也不属于任何 Agent；机器和服务的令牌要在中心上用 `beacon token revoke` 吊销 |
| `user.not_found` | 404 | 没有这个名字的成员 |
| `user.last_owner` | 409 | 这样改会让 Beacon 没有启用的所有者；先让另一个人成为所有者 |
| `key.not_found` | 404 | 调用者没有这个指纹的 SSH 密钥 |
| `key.exists` | 409 | 这个密钥属于另一个 Agent 或令牌，或者是某台机器的连接器密钥 |
| `probe.busy` | 429 | 这个目标在 15 秒内刚按需重测过，或者这个令牌已有三个请求在进行 |
| `probe.not_found` | 404 | 没有正在工作的检测点检测这个目标 |
| `probe.unavailable` | 503 | 这个 Beacon 没有网关，无法检测 |
| `location.inactive` | 403 | 网关：检测点不在目录里，或者它的机器已经到期 |
| `changes.too_many` | 429 | 网关：这个令牌这一分钟发了超过 60 个变更事件 |
| `request.not_found` | 404 | 网关：没有这个检测请求，或者它已经过期 |
| `not_found` | 404 | 网关：没有这个接口 |
