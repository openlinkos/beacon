---
sourceHash: sha256-db20b63c
---

# 让 Agent 值班

服务出问题时，Beacon 可以叫醒你指定的 Agent，一次调用就把证据交给它，让它登上机器处理，修完让每个检测点重新检测，并把发生的一切记在一条时间线上，人随时可以看。这一页写给负责搭建这套流程的人：你已经装好 Beacon，Agent 也已经通过 MCP 接入，也就是做完了[30 分钟装好](../self-hosting/install.md)的第 1 到 4 步。下面依次讲 Agent 怎样得知告警、拿到什么、怎样处理告警，以及它的令牌怎样一直有效、不用人再发新的。

Beacon 不调用任何模型，也不替 Agent 做决定。它只负责叫醒 Agent、回答 Agent 的查询、记下 Agent 做了什么。告警怎么处理由 Agent 决定，决定发生在运行 Agent 的程序里（下文称为 **harness**），比如下面的 `beacon-oncall`、一个 GitHub workflow，或者你自己写的程序。harness 不属于 Beacon。

## 告警怎样传到 Agent {#how-an-alert-reaches-an-agent}

1. **检测失败，规则触发。** 属于同一件事的告警（默认是名称和服务都相同的告警）合成一个**告警组**：一张卡片、一次钩子调用、一个 id。
2. **Beacon 调用你配置的钩子。** 钩子是配置里列出的一个带签名的 webhook，或一次 GitHub workflow 触发。调用里写明是哪个告警组、证据在哪里，不带任何凭据。
3. **harness 启动 Agent**，把调用的内容交给它。
4. **Agent 用自己的凭据查询证据**（`get_evidence`）：每个检测点看到了什么、为什么失败，同一故障域里还有哪些告警，故障前刚改了什么，已经有人做了什么。
5. **它到证据指向的机器上排查**：通过 [SSH 通道](machines.md)，用它自己的 `ssh`，或者用 Beacon 的工具（`run_command`、`read_file`、`edit_file`）。每条命令都有记录。
6. **修完让检测点验证**（`probe_now`）：每个检测点立刻重新检测这个目标。告警是否恢复，看检测结果，不看 Agent 怎么说。
7. **它留下一条备注**（`add_note`），写明出了什么问题、做了什么、还剩什么。人在卡片、告警群和控制台里都能看到。
8. **告警组恢复时**，Beacon 可以再调用一次钩子，让 Agent 写一条收尾备注。

## 先手动试一遍 {#try-it-by-hand-first}

接钩子之前，先由你代替钩子，把整个流程走一遍。开始一次演练（`docker exec beacon beacon drill fail 15m`），等大约五分钟 `ServiceDown` 触发，然后问你的 Agent：*「现在有什么在报错吗？看一下证据，上机器查一查，告诉我你打算怎么处理。」* 它会用 `list_alerts` 找到告警，读 `get_evidence`，再根据检测结果回答。被叫醒的 Agent 也是这样工作的，钩子只是代替了那个开口问的人。

## 叫醒 Agent {#wake-an-agent}

在配置里列出钩子：

```yaml
public_url: https://status.example.com     # 必填：钩子要告诉 Agent 证据在哪里

agents:
  - name: claude-oncall
    kind: webhook
    url: https://agents.example.com/hooks/beacon
    secret_file: /etc/beacon/secrets/agent-hook-secret   # 至少 24 个随机字符
    severities: [critical, warning]
    service: "hub-.*|git-.*"                  # 可选
    resolved: true                            # 告警恢复时也通知
    resolved_after: 1m                        # ……恢复一分钟后再通知（默认值）
  - name: ops-workflow
    kind: github
    repository: example/ops
    workflow: oncall.yml
    ref: main
    token_file: /etc/beacon/secrets/github-dispatch-token
```

| 字段 | 含义 |
| --- | --- |
| `name` | 小写字母、数字、点、连字符和下划线；卡片和告警群里显示这个名字 |
| `kind` | `webhook`（带签名的 POST）或 `github`（一次 `workflow_dispatch`） |
| `severities` | 哪些级别的告警组会叫醒它；默认 `[critical]` |
| `service` | 匹配整个 `service` 标签的正则表达式；包含多个服务的告警组，只要有一个服务匹配就算 |
| `resolved` | 叫醒过它的告警组恢复时，再调用一次 |
| `resolved_after` | 告警组恢复后，这次调用等多久再发。Agent 第一轮运行在修好后会紧接着写收尾备注，等一会儿再通知，Agent 得知告警结束时，这条备注已经在时间线上了；默认 `1m`，`0` 表示立即发，最多 `5m` |
| `url`、`secret_file` | Webhook：POST 到哪里，以及给每次调用签名的密钥 |
| `headers_file` | Webhook：一个文件，每行一个 `Name: value`，随每次调用发出，给自己校验密钥的接收方用（`Authorization: Bearer …`、`X-Webhook-Secret: …`）；`#` 开头的是注释。`secret_file` 和 `headers_file` 至少给一个，也可以都给 |
| `repository`、`workflow`、`ref`、`token_file` | GitHub：`owner/name`、workflow 的文件名或 id、分支（默认 `main`），以及能触发它的令牌（对这个仓库有 Actions 读写权限的细粒度令牌） |
| `api` | GitHub：默认 `https://api.github.com`；用 GitHub Enterprise 时填它的地址 |

以下情况 `beacon check-config` 会拒绝：配了钩子却没有 `public_url`，webhook 既没有 `secret_file` 也没有 `headers_file`，密钥不足 24 个字符。

**什么时候调用钩子。** 新告警组第一次发出通知时（也就是卡片发出时）调用一次，告警持续期间不再调用。设了 `resolved` 的话，告警组恢复 `resolved_after` 之后再调用一次。告警组恢复后又触发，算新的一轮，会再次叫醒钩子；这时如果还有没发出的恢复调用，就不再发了（审计日志里有记录），因为它说的那一轮已经结束。

**送达告警从不等 Agent。** 这些调用和告警送达各走各的：卡片、加急、Pushover 都不等它们。调用没有应答、超时、返回 429 或 5xx 时，分别在 5 秒、20 秒、1 分钟、3 分钟后重试。其他拒绝（401、404、422）重试也没用，立即算失败。不跟随重定向，所以载荷和签名只会发到配置的地址。服务重启打断的调用，不到 10 分钟的会接着完成。

**结果记在哪里。** 每次调用的结果记在告警组上（`GET /api/v1/alerts/{id}` 和 `get_alert` 里的 `agents`）、审计日志里（`wake_agent`）、卡片上（「10:02 已唤醒 Agent claude-oncall」，或者没能唤醒）、告警群里（失败时附上原因），以及控制台的 **Hook** 页面（`/console/hooks`）。

### Webhook {#the-webhook}

请求头、载荷和接收方怎样校验签名：见[被 webhook 唤醒的 Agent](../integrations/webhooks.md)。

### GitHub workflow {#a-github-workflow}

接收两个输入的 workflow，以及运行 Claude Code 的完整 workflow：见[GitHub Actions](../integrations/github-actions.md)。

## 被叫醒后运行 Agent {#running-an-agent-when-woken}

webhook 的另一头需要有程序来运行 Agent。`beacon-oncall` 是参考实现的接收程序：它接收 webhook、校验签名，然后启动你指定的程序，载荷从标准输入传入，告警信息放在环境变量里；`contrib/oncall` 里有 Claude Code 和 Codex 的包装脚本、给被叫醒的 Agent 用的提示词，以及一个 systemd 单元。这些都在[无头 Agent 与 CI](../integrations/headless.md) 一页里，包括接收程序决定什么、把什么留给 Agent 的预设、所有者的角色和机器（[信任边界](../integrations/webhooks.md#trust)）。

## 证据 {#the-evidence}

`GET /api/v1/alerts/{id}/evidence`，或者 MCP 工具 `get_evidence`（作用域 `status:read`），一次返回 Agent 判断告警所需的全部信息。下面是一个删减过的例子：两个检测点连不上 `git.example.com`；四分钟前这个服务刚做过一次部署；再早一点，部署脚本在这个服务的机器上执行 reload 失败了。

```jsonc
{
  "alert": {"id": "0cdfbada62dd", "status": "firing", "severity": "critical", "title": "git.example.com is down",
            "service": "git.example.com", "labels": {"alertname": "ServiceDown", "failure_domain": "origin-1", "…": "…"},
            "alerts": 1, "starts_at": "2026-11-02T01:58:00Z"},
  "window": {"from": "2026-11-02T01:28:00Z", "to": "2026-11-02T02:03:10Z"},
  "probes": {
    "services": ["git.example.com"], "locations": 2, "failing": ["edge-1", "edge-2"],
    "results": [
      {"location": "edge-1", "instance": "https://git.example.com/", "module": "http_head", "ok": false,
       "failing_since": "2026-11-02T01:54:30Z", "reason": "connect", "reasons": {"connect": 9},
       "duration_seconds": 0.004, "attempts": 1, "last_seen": "2026-11-02T02:03:00Z"},
      {"location": "edge-2", "ok": false, "reason": "connect", "…": "…"}
    ],
    "queries": ["probe_success{service=\"git.example.com\"}", "…"]
  },
  "related": [{"id": "7a01c2e9f3b4", "title": "registry.example.com is down", "severity": "critical",
               "starts_at": "2026-11-02T01:59:00Z", "acknowledged": false, "why": "failure_domain=origin-1"}],
  "changes": [{"time": "2026-11-02T01:53:40Z", "source": "github-actions", "kind": "deploy", "service": "git.example.com",
               "summary": "git v2.4.1", "posted_by": "ci-deploy", "related": "service", "phase": "before", "…": "…"}],
  "done": {
    "silences": [], "notes": [],
    "agents": [{"hook": "claude-oncall", "event": "firing", "status": "ok", "code": 202, "at": "2026-11-02T02:00:31Z", "…": "…"}],
    "machines": ["origin-1"],
    "sessions": [{"id": "20261102-015312-3fa9c1", "agent": "deploy-bot", "via": "ssh", "machine": "origin-1", "kind": "exec",
                  "command": "sudo systemctl reload nginx", "exit": 1, "related": "machine", "phase": "before",
                  "page": "https://status.example.com/console/sessions/20261102-015312-3fa9c1?s=…"}]
  },
  "links": {"runbook": "https://github.com/example/ops/blob/main/runbooks/ServiceDown.md",
            "page": "https://status.example.com/console/alerts/0cdfbada62dd?s=…", "…": "…"},
  "untrusted": {"fields": ["alert.title", "probes.results[].reason", "changes[].summary", "done.sessions[].command", "…"], "note": "…"}
}
```

各部分的含义：

- **probes**：告警组所属服务的检测结果，每个检测点、目标和模块一条：窗口结束时是否正常，从什么时候开始失败（或什么时候恢复），失败原因（`probe_failure_info{reason}`：`dns`、`connect`、`timeout`、`tls`、`http_status` 等），窗口内每种原因各失败了几次，耗时，HTTP 状态码，尝试次数（`beacon_probe_attempts`），证书到期时间，以及这个检测点是否已经没有声音（`stale`，超过三分钟没有结果）。失败的排在前面。`queries` 是用到的 PromQL，想细看的话，可以拿 `metrics:read` 令牌通过网关查询。
- **heartbeats**：告警标签里有 `heartbeat` 时（`HeartbeatStale`、`HeartbeatMissing`）才有：这个心跳在目录里的条目（`name`、`description`、`max_age`、`severity`、`machine`），SSH 通道里有没有这台机器（`enrolled`），它最后一次成功的时间（`last_success`，取自 31 天内的 `beacon_heartbeat_last_success_timestamp_seconds`），以及怎样连上这台机器的 `hint`。
- **related**：同一 `failure_domain` 或同一服务下正在触发的其他告警组，`why` 写明是哪种关联。
- **changes**：窗口内的[变更事件](#change-events)，和本告警组的服务或机器有关的排在前面。
- **done**：认领、静默、备注、叫醒过的 Agent、升级情况，告警涉及的 `machines`（标签 `machine`、`host`、`location`、`failure_domain` 里写的机器，以及目录里为相关心跳写明的机器），以及窗口内这些机器上的会话，或者认领过、写过备注的 Agent 的会话，按时间先后排列。每次会话有 Agent、机器、类型、命令和退出状态；shell 会话附上最后 20 条命令及退出状态（`shell_commands`，最多 10 个 shell，告警前的排在前面；`commands` 是命令总数，其余命令和输出用 `get_session` 查看），每次会话都附上它页面的链接。
- **links**：处理手册和仪表盘，告警及其时间线的接口地址，还有 `page`：给人打开的告警页面的签名链接。

**时间窗口**从告警组开始前 30 分钟开始（`?before=2h` 可以往前推，最多 24 小时），到现在为止；告警组已恢复的，到恢复后 30 分钟为止。所以告警前在相关机器上做过的事也在里面，比如在 `HeartbeatStale` 触发前三分钟停掉了心跳定时器的那次会话。每个变更和会话都有 `phase`：`before` 表示在告警组开始之前（可能就是原因），`during` 表示告警期间，`after` 表示恢复之后。会话超过 50 个时，优先保留告警前在相关机器上的最多 20 个，其余取最新的。连不上 VictoriaMetrics 时，`probes.error` 和 `heartbeats[].error` 会说明，其余部分照常返回。

### 不可信文本 {#untrusted-text}

证据里很多文本来自 Beacon 之外：标签值（由检测程序和规则引擎设置）、由标签生成的注解、被检测服务的应答、变更事件、钩子的应答、备注，以及在机器上执行的命令。其中任何一处都可能藏着写给 Agent 的指令。凡是带这类文本的应答，都有一个 `untrusted` 对象列出这些字段，并提示把它们当作数据。Beacon 也会清理这些文本：去掉终端转义序列、控制字符和不可见字符（双向控制符、零宽字符、Unicode 标签字符），过长的字符串会截断，以 `…` 结尾。要放进查询的值会先检查：服务名有可能改变查询语句的，就不查询。

## 立刻重测 {#probe-again-now}

修完之后，Agent 不必等下一轮检测和规则引擎，就能知道修好没有。`POST /api/v1/probe-now`，或者 MCP 工具 `probe_now`，让所有检测某个目标的检测点立刻重新检测一次，并返回每个检测点的结果。它需要 `alerts:write` 作用域：它会让检测点干活、会往告警群发消息，而且属于处理告警的一部分，这个作用域本来就涵盖这些。

- **检测什么**：`alert_id`（告警组里各告警的目标，取自它们的 `instance` 和 `module` 标签；告警没有这些标签时，取其 `service` 的所有目标），或者 `target` 加可选的 `module`（不给 `module` 就检测这个目标的所有模块），或者 `service`（这个服务的所有目标）。一次最多 20 个目标。
- **`wait`**：等检测点多少秒，默认 45，最多 90。分配了这个目标的检测点都应该回答；全部回答了，或者等待时间到了，调用就返回。
- **返回内容**：`targets`（检测了什么，以及每个目标预期由哪些检测点回答），每个检测点、每个目标一条 `results`（`ok`、`reason`、`duration_seconds`、`http_status`、`attempts`、`cert_expires`、`probed_at`；失败的排在前面），`missing`（没有及时回答的检测点：检测程序挂了，或者版本太旧，不认识这个接口），`complete`，以及一行 `summary`，比如 `7 OK, 2 failing (dns)`。

检测点推送的样本和平时每轮检测完全一样，所以 VictoriaMetrics 立刻有了新结果，告警在规则引擎下一次评估时就恢复，不用再多等一分钟。每次调用都记入审计日志（`probe_now`），并在告警群里回显一行。

为了保护检测点，有几条限制：同一个目标最多每 15 秒按需重测一次，一个令牌同时最多有三个请求在进行（`probe.busy`，429）。目标不存在，或者没有在用的检测点检测它，返回 `probe.not_found`（404）；没有网关的 Beacon 返回 `probe.unavailable`（503）。请求只保存在内存里两分钟，服务重启后不保留。

对检测程序来说，协议新增了两个网关接口，旧接口一点没变。`GET /api/v1/agent/requests?wait=25s`（`probe:write`）立即返回这个检测点待处理的请求，格式和它的检测目标相同；等待时间过了还没有请求，就返回空列表（最多等 55 秒）。`POST /api/v1/agent/requests/{id}` 接收检测结果（请求过期后返回 `request.not_found`）。`beacon-probe agent` 在每轮检测之外，始终挂着一个这样的轮询。遇到旧版网关时，它会收到 404，记一次日志，一小时后再问；旧版检测程序从不轮询，它所在的检测点会出现在 `missing` 里。

## 备注 {#notes}

备注是 Agent 或人告诉其他人「发现了什么、做了什么、接下来做什么」的方式。`POST /api/v1/alerts/{id}/notes`（请求体 `{"text": "…"}`），或者 MCP 工具 `add_note`（作用域 `alerts:write`），在告警组上留一条备注，告警组正在触发或已经恢复都可以。卡片上显示最新的三条，告警群里每条都会出现，审计日志里全部保留。写备注不等于认领告警，认领要用 `ack_alert`。每条备注最多 2000 个字符，每个告警组保留最新的 100 条。以 `public:` 开头的备注，还会去掉前缀，作为事件的进展显示在[状态页](status-pages.md)上。

## 时间线 {#the-timeline}

`GET /api/v1/alerts/{id}/timeline`，或者 `get_timeline`（作用域 `status:read`），按时间顺序讲出告警的经过：`started`、`notified`、`buzzed`、`pushover`、`acknowledged`、`note`、`silenced` 和 `silence_ended`、`agent`（每次调用钩子及其结果）、`change` 和 `session`（与证据里的相同）、`command`（shell 会话 `shell_commands` 里的每条命令，带所属会话的 id 和退出状态）、`heartbeat`（心跳最后一次成功的时间，即使在窗口之前也列出）、`resolved`。它覆盖的时间窗口和证据相同，所以告警前在相关机器上发生的变更和会话排在 `started` 之前；这些事件带着和证据里一样的 `phase`，`heartbeats` 也和证据里一样。控制台的告警页面显示的就是这条时间线。

## 变更事件 {#change-events}

部署、配置修改和 Flux 的同步以变更事件的形式送到 Beacon 之后，告警的证据就能说出故障前刚改了什么。怎样发送、字段有哪些、Flux 怎么配：见[部署与 Flux](../integrations/change-events.md)。

## 自动续期的令牌 {#tokens-that-renew-themselves}

与其让人给无界面的 Agent 一个长期令牌、过期了再发一个新的，不如让令牌在到期前自己续期：见[无头 Agent 与 CI](../integrations/headless.md#tokens-that-renew-themselves)。

## Beacon 在这里不做什么 {#what-beacon-does-not-do-here}

- 它不运行 Agent，不替 Agent 做决定，也不调用任何模型。它只叫醒 Agent、回答 Agent，并记下 Agent 做了什么。
- 它不要求任何人审批。备注、会话和时间线都摆在那里给人看，SSH 通道的暂停开关就是刹车。
