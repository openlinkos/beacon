---
sourceHash: sha256-4059b407
---

# MCP 工具

Beacon 的 MCP server 在 17001 端口的 `/mcp`：streamable HTTP，无状态，令牌和作用域与 [REST API](api.md) 相同。每个工具都调用和对应 REST 路径同样的方法。机器工具沿用 Agent 本地工具的名字和行为（执行命令、读文件、写文件、改文件）；结果是结构化的，不夹终端控制字符，长输出会截断，并说明怎么取全文。每次写操作和每次会话都连同调用者的名字记录下来（Agent 记为 `alice / claude-code`，其他令牌记为令牌自己的名字），并回显到告警群。

## 接入 {#connecting}

Claude Code 用 OAuth 登录：添加 Beacon，然后从 Claude Code 登录，浏览器会打开 Beacon 的授权页面，由一位成员把它允许为自己的一个 Agent（[成员、Agent 与登录](../accounts.md#connecting-claude-code-or-codex)）：

```bash
claude mcp add --transport http beacon https://status.example.com/mcp
claude mcp login beacon     # or /mcp inside Claude Code
```

Codex 也一样：`codex mcp add beacon --url https://status.example.com/mcp`，然后 `codex mcp login beacon`。无界面的 Agent 改为发送 Agent 的令牌，令牌在控制台的 **Agent** 页面或用 `beacon agent add` 创建：Claude Code 用 `--header "Authorization: Bearer lbt_…"`，Codex 写在 `~/.codex/config.toml` 里，令牌放在环境变量中：

```toml
[mcp_servers.beacon]
url = "https://status.example.com/mcp"
bearer_token_env_var = "BEACON_TOKEN"
```

其他支持 HTTP 上 MCP 的客户端也一样接入。没有凭据，或凭据通不过校验的请求，会得到 `401`，它的 `WWW-Authenticate` 指向受保护资源的元数据（`Bearer resource_metadata="https://status.example.com/.well-known/oauth-protected-resource/mcp"`），MCP 客户端从那里找到怎样登录。server 的说明会告诉 Agent 每组工具是做什么的，以及怎样谨慎地使用写工具；每个凭据都能看到全部工具，调用时缺少所需的作用域会被拒绝，消息里写明缺的是哪个，对 Agent 还说明它的预设在所有者角色范围内允许什么。

## 告警 {#alerts}

| 工具 | 作用域 | 做什么 |
| --- | --- | --- |
| `status_summary` | `status:read` | 告警现在能否送到人（不能的话为什么），按级别统计的正在触发的告警组，已认领的数量，以及本月用掉的飞书 API 额度 |
| `list_alerts` | `status:read` | 告警组，正在触发的在前、新的在前，带谁认领或静默了、升级到了哪一步。`state: all` 加上最近七天恢复的 |
| `get_alert` | `status:read` | 按 `id` 取一个告警组：标签、处理手册和面板、认领、静默、升级、备注、唤醒的 Agent，以及它页面的签名链接 |
| `list_silences` | `status:read` | 生效中和待生效的静默，最先结束的在前 |
| `list_audit` | `status:read` | 最近的写操作，带操作者和途径，新的在前（`limit`，1 到 200） |
| `ack_alert` | `alerts:write` | 认领一个正在触发的告警组（`id`、`note`）：升级到手机停止四小时。在备注里写明正在做什么 |
| `silence_alert` | `alerts:write` | 静默一个正在触发的告警组（`id`、最长 24 小时的 `duration`、必填的 `comment`）；同时认领它 |
| `expire_silence` | `alerts:write` | 立即结束一个静默（`silence_id`），它覆盖的告警重新开始通知 |

## 处理告警 {#working-an-alert}

| 工具 | 作用域 | 做什么 |
| --- | --- | --- |
| `get_evidence` | `status:read` | 判断一个告警组所需的一切，一次调用（`id`、`before`）：每个检测点现在的结果、从什么时候开始失败、为什么，耗时、HTTP 状态码、尝试次数和证书到期时间；同一故障域里的相关告警组；变更事件；人和 Agent 已经做了什么；链接 |
| `get_timeline` | `status:read` | 告警组的来龙去脉，按时间排列：开始、通知、加急、Pushover、认领、备注、静默、唤醒 Agent、变更、会话、恢复 |
| `add_note` | `alerts:write` | 给告警组留一条备注，正在触发或已恢复的都行（`id`、最多 2000 字符的 `text`）：发现了什么、做了什么、接下来做什么。值班的人在卡片上能看到。备注不算认领 |
| `list_changes` | `status:read` | 变更事件，新的在前：部署、配置修改和 Flux 的同步（`service`、`machine`、`source`、`since`、`limit`） |
| `probe_now` | `alerts:write` | 让检测某个目标的每个检测点立刻重测，在等待时间内拿到每个检测点的答复（`alert_id`、`target` 可带 `module`，或 `service`；`wait` 秒，默认 45，最多 90）。修好之后用它验证，不必等下一个检测周期 |

## 机器 {#machines}

| 工具 | 作用域 | 做什么 |
| --- | --- | --- |
| `list_machines` | `status:read` 或 `machines:ssh` | 机器列表、令牌能否到达每台机器，以及给 Agent 自己的 ssh、scp 和 rsync 用的 `ssh_config` 和一行 `known_hosts` |
| `run_command` | `machines:ssh` | 在机器上执行一条 shell 命令（`machine`、`command`、`cwd`、`stdin`、最多 600 秒的 `timeout`），以机器上的用户身份、不分配终端：退出码、stdout 和 stderr 都是纯文本。长输出保留开头和结尾；`get_session` 能取到全部 |
| `read_file` | `machines:ssh` | 文本文件的若干行（`machine`、`path`、`offset`、`limit`），每行是行号、一个制表符和这一行，和 `cat -n` 一样；超过 2000 字符的行会截断 |
| `write_file` | `machines:ssh` | 写入整个文件（`machine`、`path`、最多 1 MiB 的 `content`），需要时创建目录；已有的文件保持原来的属主和权限 |
| `edit_file` | `machines:ssh` | 替换文本文件里一段完全一致的文本（`machine`、`path`、`old_string`、`new_string`、`replace_all`）。没设 `replace_all` 时 `old_string` 必须只出现一次；期间文件变过则拒绝，而不是覆盖 |
| `list_sessions` | `status:read` 或 `machines:ssh` | 经 SSH 通道和工具产生的会话，新的在前（`machine`、`agent`、`limit`）：谁、在哪里、做什么、什么时候、退出码 |
| `get_session` | `sessions:read` 或 `machines:ssh` | 会话的文字记录（`id`）：命令的全部输出，或交互 shell 的各条命令及其输出和退出码 |
| `add_machine` | `machines:write` | 添加一台机器并钉住它的主机密钥（`name`、`address`、`user`、`critical`、`note`、`replace`）；NAT 后面的机器用 `address: connector`，带上 `connector_key`，可选 `host_key`。结果说明机器上需要做什么 |
| `set_machine_connector` | `machines:write` | 设置或更换 NAT 后面的机器连出来用的密钥（`name`、`key`）；用旧密钥建立的连接会被关掉 |
| `remove_machine` | `machines:write` | 忘掉一台机器（`name`）；它的会话记录保留 |
| `pause_ssh` | `machines:write` | 立刻停止所有 Agent 到达机器：关掉打开的会话，`resume_ssh` 之前拒绝新会话 |
| `resume_ssh` | `machines:write` | 让 Agent 重新能到达机器 |

## 配置 {#configuration}

Beacon 监控的内容，按文件里的样子读写（[目录、检测目标和检测模块](../catalog.md)）。`kind` 是 `locations`、`heartbeats`、`resources`、`targets`、`modules` 或 `status_pages`（[状态页](../status.md)）；`key` 指定其中一项。

| 工具 | 作用域 | 做什么 |
| --- | --- | --- |
| `get_config` | `status:read` 或 `config:write` | 配置和当前生效的版本；给 `kind` 只看一个列表，给 `kind` 和 `key` 只看一项 |
| `put_config` | `config:write` | 添加或替换一项（`kind`、`key`、`value`、`note`、`if_version`）。整个配置都要校验，修改立即生效；结果是这次修改，带版本号和改前改后的值 |
| `delete_config` | `config:write` | 删除一项（`kind`、`key`、`note`、`if_version`） |
| `list_config_changes` | `status:read` 或 `config:write` | 修改记录，新的在前（`kind`、`key`、`before`、`limit`）：版本、时间、谁、经什么途径、为什么，以及增加、修改或删除了哪些项 |
| `get_config_change` | `status:read` 或 `config:write` | 一次修改（`version`），带每一项改前和改后的值 |
| `revert_config_change` | `config:write` | 把一次修改作为一次新的修改撤销（`version`、`note`）；之后的修改动过其中某项时拒绝 |

把读到的版本作为 `if_version` 传回：在更新的版本之上做的修改会被拒绝，返回 `config.conflict`，所以 Agent 不会覆盖期间别人做的修改。

## 状态页与维护 {#status-pages-and-maintenance}

公开的状态页由告警组和维护窗口生成（[状态页](../status.md)）。用 `add_note` 留下的备注以 `public:` 开头时，就是事件对外的进展。

| 工具 | 作用域 | 做什么 |
| --- | --- | --- |
| `get_status_page` | `status:read` | 一个状态页，和它的公开 JSON 一样（`name`、`language`）：整体状态，每个组件现在和 90 天来的状态，事件（进行中的，然后是最近 30 天的）和对外的进展，维护窗口 |
| `list_maintenance` | `status:read` | 维护窗口，开始得越晚越靠前，结束的保留 91 天：服务、时间、标题、谁安排的、对应的静默 |
| `schedule_maintenance` | `maintenance:write` | 安排一个维护窗口（`services`、`starts_at`、`ends_at`、`title`、`description`）：状态页提前公告，窗口内静默这些服务的告警。最长 7 天，在 90 天内开始，不能从过去开始 |
| `end_maintenance` | `maintenance:write` | 立即结束一个维护窗口，或在开始前取消（`id`）：它的静默随之结束，状态页上也会显示 |

## 成员与 SSH 密钥 {#people-and-ssh-keys}

| 工具 | 作用域 | 做什么 |
| --- | --- | --- |
| `list_users` | `accounts:write` | 登录 Beacon 的成员（用户名、显示名、角色、最近登录、是否停用、他们的 Agent）和尚未使用的邀请 |
| `invite_user` | `accounts:write` | 一个一次性链接，24 小时内有效，一个人凭它以某个 `role` 加入（`note` 说明是给谁的）；只交给那个人 |
| `add_ssh_key` | `machines:ssh` | 为 SSH 通道登记你自己的公钥（`key`，一行 `authorized_keys`；`comment`），这样你自己的 ssh、scp、sftp 和 rsync 就能到达你可访问的机器；之后 `list_machines` 会给出 `ssh_config` 片段。密钥属于你调用时所用的凭据（`via`）：那个令牌或 OAuth 登录有效时它才有效，随它一起移除。你所代表的人能看到，也会回显到告警群 |
| `list_ssh_keys` | `machines:ssh` | 你的 SSH 密钥 |
| `remove_ssh_key` | `machines:ssh` | 移除你的一个密钥（`fingerprint`）；凭它登录的连接会被关闭 |

没有哪个 Agent 预设给 `accounts:write`：只有所有者在命令行上把这个作用域给了 Agent，Agent 才能管理成员。修改角色、停用成员和重设密码都不是 MCP 工具；那是人的事，在控制台或主机上做。

## 你的令牌 {#your-token}

| 工具 | 作用域 | 做什么 |
| --- | --- | --- |
| `whoami` | 任意 | 你在 Beacon 眼里是谁：审计日志给你的名字（`alice / claude-code`）、你所代表的人、你的预设和它在那个人的角色范围内当前给你的作用域、你能到达的机器、你的 SSH 密钥，以及你的凭据：令牌（到期时间、有效期长度、能否以及何时可以续期），或由客户端刷新的 OAuth 登录 |
| `renew_token` | 任意 | 一个新的密钥，有效期从现在起重新算，名字、作用域、机器和密钥不变。只显示一次；旧密钥还能用十分钟。每天最多一次，登记的机器的令牌不能续期，OAuth 登录也不能，由它的客户端刷新 |
| `revoke_token` | `machines:write` | 吊销一把能到达机器的令牌，或任何 Agent 的令牌：它在机器上打开着的东西立即关闭，之后的请求都被拒绝。针对单个 Agent 的暂停开关；机器和服务的令牌只能在主机上吊销 |

## 结果 {#results}

工具返回结构化的内容。应答的 `untrusted` 对象列出的字段来自 Beacon 之外（标签、被检测的服务、变更的发送方、机器打印出来的东西），已去掉控制字符和不可见字符，太长的会截断：Agent 应当把它们当作数据，而不是指令。被拒绝的调用返回工具错误，消息说明出了什么错、怎么改正，和 REST API 的[错误码](api.md#error-codes)一样。
