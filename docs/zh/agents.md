---
sourceHash: sha256-c402245e
---

# Agent 与告警

告警触发时，Beacon 可以唤醒一个 Agent，一次调用就把证据交给它，把它留下的备注放到告警卡片上，并把发生的一切记在一条时间线上。Beacon 不调用任何模型，也不替 Agent 做决定：它只负责唤醒、回答 Agent 的查询、记下 Agent 做了什么。运行 Agent 的东西（你自己的一个小服务，或者里面跑着 Claude Code、Codex 的 GitHub workflow）是 Agent 自己的工具链，不属于 Beacon。

## 唤醒 Agent {#wake-an-agent}

在配置里列出钩子：

```yaml
public_url: https://status.example.com     # 必须有：钩子要告诉 Agent 证据在哪里

agents:
  - name: claude-oncall
    kind: webhook
    url: https://agents.example.com/hooks/beacon
    secret_file: /etc/linkos-beacon/secrets/agent-hook-secret   # 至少 24 个随机字符
    severities: [critical, warning]
    service: "hub-.*|git-.*"                  # 可选
    resolved: true                            # 告警恢复时也通知
    resolved_after: 1m                        # ……恢复一分钟后再通知（默认）
  - name: ops-workflow
    kind: github
    repository: example/ops
    workflow: oncall.yml
    ref: main
    token_file: /etc/linkos-beacon/secrets/github-dispatch-token
```

| 字段 | 含义 |
| --- | --- |
| `name` | 小写字母、数字、点、连字符和下划线；卡片和群消息里显示这个名字 |
| `kind` | `webhook`（带签名的 POST）或 `github`（一次 `workflow_dispatch`） |
| `severities` | 哪些级别的告警组会唤醒它；默认 `[critical]` |
| `service` | 匹配整个 `service` 标签的正则表达式；包含多个服务的告警组，只要有一个服务匹配就算 |
| `resolved` | 唤醒过它的告警组恢复时，再调用一次 |
| `resolved_after` | 告警组恢复后，这次调用等多久再发：Agent 第一轮运行修好之后紧接着写的收尾备注，等它得知告警已恢复时已经在时间线上了；默认 `1m`，`0` 表示立即发，最多 `5m` |
| `url`、`secret_file` | Webhook：POST 到哪里，以及给每次调用签名的密钥 |
| `headers_file` | Webhook：一个文件，每行一个 `Name: value`，随每次调用一起发出，给自己校验密钥的接收方用（`Authorization: Bearer …`、`X-Webhook-Secret: …`）；`#` 开头是注释。`secret_file` 和 `headers_file` 至少给一个，也可以都给 |
| `repository`、`workflow`、`ref`、`token_file` | GitHub：`owner/name`、workflow 的文件名或 id、分支（默认 `main`），以及能触发它的令牌（对这个仓库有 Actions 读写权限的细粒度令牌） |
| `api` | GitHub：默认 `https://api.github.com`，GitHub Enterprise 时改掉 |

新告警组第一次发出通知时（也就是卡片发出时）调用一次钩子，告警持续触发期间不再调用；设了 `resolved` 的话，恢复 `resolved_after` 之后再调用一次。告警组恢复后又触发，算新的一轮，会再次唤醒；这时还在等待的恢复调用就不发了（审计日志里有记录），因为它说的那一轮已经结束。

这些调用和告警送达各走各的：卡片、加急、Pushover 从不等它们。没有应答、超时、429 或 5xx 时，分别在 5 秒、20 秒、1 分钟、3 分钟后重试；其他拒绝（401、404、422）重试也没用，立即算失败。不跟随重定向，所以载荷和签名只会发到配置的地址。服务重启打断的调用，不到 10 分钟的会接着做完。

每次调用的结果都记在告警组上（`GET /api/v1/alerts/{id}` 和 `get_alert` 里的 `agents`）、审计日志里（`wake_agent`）、卡片上（「10:02 已唤醒 Agent claude-oncall」，或者没能唤醒），并发到告警群，失败时附上原因。

### Webhook {#the-webhook}

Beacon POST 一段 JSON，带这些请求头：

| 请求头 | 值 |
| --- | --- |
| `X-Beacon-Signature` | `sha256=` 加上用密钥对请求体算出的 HMAC-SHA256（十六进制） |
| `X-Beacon-Event` | `firing` 或 `resolved` |
| `X-Beacon-Delivery` | 这次调用的 id，每次重试都一样 |

```json
{
  "delivery": "9f3c2a1b7e4d5c60",
  "event": "firing",
  "at": "2026-11-02T02:00:31Z",
  "hook": "claude-oncall",
  "alert": {
    "id": "0cdfbada62dd", "status": "firing", "severity": "critical",
    "title": "git.example.com is down", "description": "No answer from 2 locations",
    "service": "git-example-com",
    "labels": {"alertname": "ServiceDown", "service": "git-example-com", "severity": "critical", "failure_domain": "site-a"},
    "alerts": 1, "starts_at": "2026-11-02T01:58:00Z"
  },
  "links": {
    "alert": "https://status.example.com/api/v1/alerts/0cdfbada62dd",
    "evidence": "https://status.example.com/api/v1/alerts/0cdfbada62dd/evidence",
    "timeline": "https://status.example.com/api/v1/alerts/0cdfbada62dd/timeline",
    "mcp": "https://status.example.com/mcp",
    "runbook": "https://github.com/example/ops/blob/main/runbooks/ServiceDown.md"
  },
  "hint": "Beacon woke you for this alert group. Get its evidence in one call: …",
  "untrusted": {"fields": ["alert.title", "alert.description", "alert.service", "alert.services", "alert.labels"], "note": "…"}
}
```

告警说的是一个心跳、而目录里这个心跳写了所在机器（[catalog.md](catalog.md#心跳)）时，`firing` 调用的 `hint` 会说是哪台机器，并提示用 `list_machines` 查怎么连上它。

`resolved` 载荷的字段相同，其中 `at` 是告警组恢复的时间（不是 `resolved_after` 之后真正发出的时间），另外多一个 `firing_call`：这一轮唤醒 Agent 的那次调用，好让它启动的那次运行认出自己，并说明那次运行是否可能还没结束：

```json
"firing_call": {"delivery": "9f3c2a1b7e4d5c60", "at": "2026-11-02T02:00:31Z", "status": "ok", "recent": true}
```

触发调用发生在告警组恢复前不到 10 分钟时，`recent` 为 true。这时 hint 会说，那次调用启动的运行可能还在写备注，请先读 `get_timeline`（包括备注），再决定要不要自己写收尾备注。

读请求体之前，先用原始请求体核对签名；`at` 超过 10 分钟的、`delivery` 见过的，一律拒绝：

```python
import hashlib, hmac

def genuine(secret: bytes, body: bytes, signature: str) -> bool:
    return hmac.compare_digest("sha256=" + hmac.new(secret, body, hashlib.sha256).hexdigest(), signature)
```

载荷里没有任何凭据：Agent 用它自己的令牌取证据（`status:read`；要认领、写备注还要 `alerts:write`）。

### GitHub workflow {#a-github-workflow}

Beacon 触发 workflow 时带两个输入：`alert`（告警组 id）和 `event`（`firing` 或 `resolved`），workflow 里必须声明它们：

```yaml
on:
  workflow_dispatch:
    inputs:
      alert: {description: Beacon alert group id, required: true}
      event: {description: firing or resolved, required: false, default: firing}

jobs:
  oncall:
    if: inputs.event == 'firing'
    runs-on: ubuntu-latest
    steps:
      - name: Evidence
        env:
          ALERT: ${{ inputs.alert }}
          BEACON_TOKEN: ${{ secrets.BEACON_TOKEN }}
        run: |
          curl -fsS -H "Authorization: Bearer $BEACON_TOKEN" \
            "https://status.example.com/api/v1/alerts/$ALERT/evidence" > evidence.json
      # 接下来是 Agent 自己的步骤，用 Beacon 的 MCP 服务和它自己的令牌
```

像上面这样经 `env` 传入输入，不要直接写进脚本：能触发这个 workflow 的人都能决定输入的内容。

## 被唤醒后运行 Agent {#running-an-agent-when-woken}

钩子的另一端得有个东西来运行 Agent。`linkos-oncall` 是参考实现，发行版里 `linkos-beacon`、`linkos-probe` 之外的第三个二进制：它接收 webhook，核对签名，然后启动你指定的程序，把载荷送到程序的标准输入，把告警放进它的环境变量。它不做任何决定，而且有意做得这么薄：Claude Code、Codex，或者任何会读环境变量的程序都能接在它后面。`contrib/oncall` 里有前两者的封装脚本、给被唤醒的 Agent 的提示词，以及一个 systemd 单元。

```bash
linkos-oncall -secret-file /etc/linkos-oncall/hook-secret \
  -run /usr/local/lib/linkos-oncall/claude-code.sh -log-dir /var/lib/linkos-oncall/logs
```

| 参数 | 含义 |
| --- | --- |
| `-listen` | 在哪里接收 webhook；默认 `:17007`。Beacon 里钩子的 `url` 指到这里，除非两者在同一个私有网络，否则经过你的 TLS 反向代理 |
| `-secret-file` | 钩子的密钥：内容和 Beacon 里该钩子的 `secret_file` 一样 |
| `-run` | `firing` 事件运行的程序，经 `/bin/sh -c` 执行 |
| `-run-resolved` | `resolved` 事件运行的程序；不设的话，恢复事件只确认收到，什么都不运行 |
| `-concurrency` | 同时运行的程序数；默认 `2`。最多再排队 32 个；超出的回答 503，Beacon 稍后重试 |
| `-timeout` | 每个程序能跑多久；默认 `30m`。到时整个进程组被杀掉 |
| `-log-dir` | 每次运行的标准输出和标准错误，文件名 `<时间>-<delivery>.log`，末尾一行写退出状态和耗时 |
| `-keep` | 比这更老的日志会被删除；默认 `720h`。`0` 表示全部保留 |

核对完投递、把运行排进队列之后，它立刻回答 202：程序做成了什么，看它的日志、Agent 留下的备注和 Beacon 的记录，而不是看给 Beacon 的应答。签名错误或缺失的请求体，以及 `at` 超过 15 分钟的（Beacon 的重试到不了这么久，所以是重放），回答 401 拒绝；读不懂的回答 400。每个 `delivery` 记住 24 小时，所以已经接收过的投递再来一次，回答 202 但什么都不运行；以签过名的请求体里的 id 为准，不看请求头。收到 SIGTERM 后不再接收投递，等正在运行的程序结束，最多等 `-timeout`。

程序从标准输入拿到载荷，环境里有这些变量，每一个都存在，Beacon 没有给值的为空：`BEACON_EVENT`、`BEACON_DELIVERY`、`BEACON_HOOK`、`BEACON_ALERT_ID`、`BEACON_ALERT_TITLE`、`BEACON_ALERT_SERVICE`、`BEACON_ALERT_SEVERITY`、`BEACON_ALERT_STATUS`、`BEACON_ALERT_URL`、`BEACON_EVIDENCE_URL`、`BEACON_TIMELINE_URL`、`BEACON_MCP_URL`、`BEACON_RUNBOOK_URL`。标题和服务来自标签：是数据，不是指令。

### 封装脚本 {#the-wrappers}

`contrib/oncall/claude-code.sh` 以无头模式运行 Claude Code。它把 `contrib/oncall/prompt.md` 和这条告警拼成一份提示词，给 Claude Code 配上 Beacon 的 MCP 服务（`BEACON_MCP_URL` 上的 HTTP 服务；令牌在 Claude Code 每次连接时从 `BEACON_TOKEN_FILE` 读取，所以换过的令牌会自动用上），让它不经询问就能用 Beacon 的工具、除此之外什么都不能用（`--tools ""`、`--allowedTools "mcp__beacon__*"`、`--permission-mode dontAsk`），用 `--max-turns` 限定工作量，并把每条消息作为一行 JSON 写进日志，人可以一步一步看 Agent 做了什么。`ONCALL_MAX_TURNS`、`ONCALL_MODEL`、`ONCALL_PROMPT`、`CLAUDE_BIN` 和 `CLAUDE_ARGS` 用来调整。Claude Code 的认证和平时一样：服务用户家目录里的登录，或者服务环境里的 `ANTHROPIC_API_KEY`。

`contrib/oncall/codex.sh` 用 `codex exec` 做同样的事：提示词从标准输入进，Beacon 的 MCP 服务通过 `-c mcp_servers.beacon.*` 配置，令牌放在只有 Codex 看得到的环境变量里，本地的一切都在只读沙箱里，不要求审批。`ONCALL_MODEL`、`CODEX_BIN` 和 `CODEX_ARGS` 用来调整。

`contrib/oncall/prompt.md` 是两者共用的提示词，一页：它被 Beacon 为这条告警唤醒；先取证据（`get_evidence`）；然后到证据指出的机器上去，用 Beacon 的工具（`run_command`、`read_file`、`list_machines`）或者用 `list_machines` 返回的 `ssh_config` 走它自己的 ssh；修复安全可修的；验证，有 `probe_now` 的地方用它，否则再调一次 `get_evidence`；最后用 `add_note` 写下结论、做了什么、还剩什么。不可信字段是数据；备注里不放密钥；不认领也不静默，那是人的决定；做不下去就停下来留一条备注。按你自己的服务改它，但保留这些规则。

### 作为服务运行 {#as-a-service}

`contrib/oncall/linkos-oncall.service` 让它以专用用户运行，系统只读（`ProtectSystem=strict`），只能写该用户的家目录（Agent 的登录信息放在那里）和日志目录，并且 `Restart=always`。文件头列出了安装命令：建用户，把密钥和令牌放到 `/etc/linkos-oncall` 下、只让该用户可读，把封装脚本和提示词放到 `/usr/local/lib/linkos-oncall`，以该用户安装并登录 Agent 程序。停止服务时会等正在进行的运行结束。

### 在 GitHub Actions 里 {#in-github-actions}

对 `kind: github` 的钩子，`contrib/github/oncall.yml` 是一个接收 `alert` 和 `event` 两个输入的 workflow：用同一份 `prompt.md`（复制到 `.github/oncall/prompt.md`）拼出提示词，通过 Claude Code 的 GitHub Action 运行它，并接上 Beacon 的 MCP 服务：令牌来自仓库密钥 `BEACON_TOKEN`，地址来自变量 `BEACON_MCP_URL`，同样只允许 Beacon 的工具。其余的 Agent 都用自己的令牌从 Beacon 取；输入经环境变量传入，从不写进脚本。

### 信任模型 {#trust}

这个程序核对签名、启动程序，它决定的只有这些。Agent 接下来做什么，受它令牌的作用域和该令牌能到达的机器限制，而且每次调用、每条命令、每个会话都经过 Beacon，由 Beacon 记录并在卡片和群里展示给人；暂停开关能让它停下。载荷里没有任何凭据；harness 和封装脚本只从文件读密钥，不记录密钥，也不记录载荷。提示词要求 Agent 把 Beacon 之外来的内容当数据读，Beacon 也把这些字段标为 `untrusted`。

## 证据 {#the-evidence}

`GET /api/v1/alerts/{id}/evidence` 或 MCP 工具 `get_evidence`（作用域 `status:read`）一次给出 Agent 判断这条告警需要的东西：

- **probes**：告警组所属服务的检测结果，每个检测点、目标、模块一条：窗口结束时是否正常，从什么时候开始失败（或什么时候恢复），失败原因（`probe_failure_info{reason}`：`dns`、`connect`、`timeout`、`tls`、`http_status` 等）以及窗口内各原因失败了几次，耗时，HTTP 状态码，尝试次数（`linkos_probe_attempts`），证书到期时间，以及这个检测点是否已经没有数据。失败的排在前面。`queries` 是用到的 PromQL，想细看时可以带 `metrics:read` 令牌经网关再查。
- **heartbeats**：告警标签里有 `heartbeat` 的（`HeartbeatStale`、`HeartbeatMissing`），给出这个心跳在目录里的条目（`name`、`description`、`max_age`、`severity`、`machine`），SSH 通道里有没有这台机器（`enrolled`），最近一次成功的时间（`last_success`，取自 31 天内的 `linkos_heartbeat_last_success_timestamp_seconds`），以及怎么连上这台机器的 `hint`。其他告警没有这一项。
- **related**：同一 `failure_domain` 里、或者同一服务正在触发的其他告警组。
- **changes**：窗口内的变更事件（见下文），和这个告警组的服务或机器有关的排在前面。
- **done**：认领、静默、备注、唤醒过的 Agent、升级情况，告警涉及的机器 `machines`（告警标签 `machine`、`host`、`location`、`failure_domain` 指到的，以及目录里为它的心跳写的机器），以及窗口内的会话：在这些机器上的，或者由认领过它、给它写过备注的 Agent 发起的，按时间先后。每个会话给出 Agent、机器、类型、命令和退出码；交互式 shell 还给出最后 20 条命令及各自的退出码（`shell_commands`，最多十个 shell，告警之前的优先；`commands` 是总条数，其余的连同输出用 `get_session` 取），以及会话页面的链接。
- **links**：处理手册和面板，以及这条告警和它的时间线的地址。

窗口从告警组开始前 30 分钟起（`?before=2h` 可以更长，最多 24 小时），到现在为止；告警组已恢复的，到恢复后 30 分钟为止。所以告警之前刚在它的机器上做过的事也在里面，比如在 `HeartbeatStale` 触发前三分钟停掉心跳定时器的那个会话。每个变更和会话都有 `phase`：`before` 是告警组开始之前（可能就是原因），`during` 是触发期间，`after` 是恢复之后。会话超过 50 个时，先保留告警开始前在它的机器上的会话（最多 20 个），再保留最新的。连不上 VictoriaMetrics 时，`probes.error` 和 `heartbeats[].error` 会说明，其余部分照常返回。

### 不可信的文本 {#untrusted-text}

证据里很多文本来自 Beacon 之外：标签值（由检测点和规则引擎设置）、用它们拼出来的注释、被检测服务的应答、变更事件、钩子的应答、备注，以及在机器上执行的命令。其中任何一段都可能藏着写给 Agent 的指令。凡是带这类文本的返回，都有一个 `untrusted` 对象列出这些字段，并提醒把它们当数据读。Beacon 也会先清理：去掉终端转义序列、控制字符和看不见的字符（双向控制符、零宽字符、Unicode 标签字符），过长的字符串截断，以 `…` 结尾。要放进查询的值会先检查：服务名有可能改变查询时，就不查。

## 立即重新检测 {#probe-again-now}

修完之后，Agent 不该等下一轮检测和规则引擎才知道是否生效。`POST /api/v1/probe-now` 或 MCP 工具 `probe_now`（作用域 `alerts:write`：它让检测点干活并发到群里，属于处理告警的动作，这个作用域本来就覆盖）让每个检测这个目标的检测点立刻再检测一次，并返回每个检测点的结果：

- **检测什么**：`alert_id`（告警组里各条告警的目标，取自它们的 `instance` 和 `module` 标签；告警没带这些标签时，取其 `service` 的全部目标），或者 `target` 加可选的 `module`（不给 module 时，这个目标有几个模块就检测几个），或者 `service`（这个服务的全部目标）。一次请求最多 20 个目标。
- **wait**：等检测点多少秒，默认 45，最多 90。分配了这个目标的检测点都在等待之列；全部应答或者等待到期，调用就返回。
- **返回**：`targets`（检测了什么，每个带上应检测它的检测点），`results` 按检测点和目标各一条（`ok`、`reason`、`duration_seconds`、`http_status`、`attempts`、`cert_expires`、`probed_at`，失败的排前面），`missing`（没有按时应答的检测点：探针挂了，或者版本太老不认识这条路由），`complete`，以及一行 `summary`，比如 `7 OK, 2 failing (dns)`。

检测点推送的样本和周期检测的一样，所以 VictoriaMetrics 马上有新结果，告警在规则引擎下一次评估就能恢复，不用再等一分钟。调用会记入审计（`probe_now`），并用一行话回显到群里。有几条限制保护检测点：同一目标每 15 秒最多按需检测一次，一个令牌最多同时有三个请求在进行（`probe.busy`，429）；目标不存在，或者没有活跃的检测点检测它，是 `probe.not_found`（404）；没有网关的 Beacon 返回 `probe.unavailable`（503）。请求放在内存里，两分钟后失效，重启后不保留。

对探针来说，协议多了两条网关路由，老探针不受影响：`GET /api/v1/agent/requests?wait=25s`（`probe:write`）有属于这个检测点的待处理请求时立刻返回，以它的检测目标的形式给出，否则等待到期（最多 55 秒）返回空列表；`POST /api/v1/agent/requests/{id}` 接收探针的结果（请求已过期时为 `request.not_found`）。`linkos-probe agent` 在周期检测之外一直挂着一个这样的轮询。面对老网关它会收到 404，说明一次后一小时再问；老探针从不问，它的检测点会出现在 `missing` 里。

## 备注 {#notes}

`POST /api/v1/alerts/{id}/notes`（请求体 `{"text": "…"}`）或 MCP 工具 `add_note`（作用域 `alerts:write`）在告警组上留一条备注，告警在触发或已恢复都可以：发现了什么、做了什么、接下来做什么。卡片显示最新的三条，每条都发到告警群，审计日志全部保留。备注不等于认领，认领用 `ack_alert`。每条备注最多 2000 个字符，每个告警组保留最新的 100 条。

## 时间线 {#the-timeline}

`GET /api/v1/alerts/{id}/timeline` 或 `get_timeline`（作用域 `status:read`）按时间顺序列出这条告警的经过：`started`、`notified`、`buzzed`、`pushover`、`acknowledged`、`note`、`silenced` 和 `silence_ended`、`agent`（调用过的每个钩子及结果）、`change` 和 `session`（与证据里的相同）、`command`（shell 的每条 `shell_commands`，带所属会话的 id 和退出码）、`heartbeat`（心跳最近一次成功，早于窗口也列出）、`resolved`。窗口和证据一样，所以告警之前在它的机器上的变更和会话排在 `started` 前面；这些事件带着证据里的 `phase`，`heartbeats` 也和证据里一样。

## 变更事件 {#change-events}

部署、配置变更和 Flux 的同步以变更事件的形式送到 Beacon，告警的证据里就能看出它之前刚改过什么。发送方用带 `changes:write` 作用域的令牌发到网关（17006 端口）：

```bash
docker exec linkos-beacon linkos-beacon token create ci-deploy -scopes changes:write -expires 365d
```

```bash
curl -fsS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"source": "github-actions", "kind": "deploy", "service": "hub-example-com", "summary": "hub v2.1",
       "url": "https://github.com/example/hub/actions/runs/1"}' \
  https://metrics.example.com/api/v1/changes
```

| 字段 | 含义 |
| --- | --- |
| `source` | 必填：发送方，比如 `github-actions`、`deploy-script` |
| `kind` | `deploy`、`config`、`rollback` 或别的词；默认 `change` |
| `summary` | 必填：改了什么 |
| `service`、`machine` | 涉及什么，写法和告警的 `service` 标签、机器名一致 |
| `url`、`revision` | 详情的链接；提交或版本 |
| `time` | RFC 3339；默认为现在；最多比现在晚 1 小时，也不能早于变更的保留期 |

不认识的字段会被拒绝，写错字段名不会悄悄被忽略。Beacon 会把令牌名和每条变更一起保存。

### Flux {#flux}

Flux 的 notification-controller 把事件发给 `generic` 类型的 Provider。让它指向 `/api/v1/changes/flux`，令牌放在请求头里：

```yaml
apiVersion: v1
kind: Secret
metadata: {name: beacon-changes, namespace: flux-system}
stringData:
  address: https://metrics.example.com/api/v1/changes/flux
  headers: |
    Authorization: Bearer lbt_…
---
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Provider
metadata: {name: beacon-changes, namespace: flux-system}
spec:
  type: generic
  secretRef: {name: beacon-changes}
---
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Alert
metadata: {name: beacon-changes, namespace: flux-system}
spec:
  providerRef: {name: beacon-changes}
  eventSeverity: info
  eventSources:
    - {kind: Kustomization, name: "*"}
    - {kind: HelmRelease, name: "*"}
  eventMetadata:
    service: hub-example-com    # 这些对象跑的是什么服务
```

| Flux 事件 | 变更 |
| --- | --- |
| `reason` | `kind`，比如 `ReconciliationSucceeded`、`UpgradeFailed` |
| `involvedObject` | `object`：`Kustomization/flux-system/apps` |
| `message`（Alert 设了 `summary` 时接在它后面） | `summary` |
| `metadata`：`…/revision` | `revision` |
| `severity`、`timestamp` | `severity`、`time` |
| `metadata`：`service`、`machine`（Alert 的 `eventMetadata`），或地址里的 `?service=…&machine=…` | `service`、`machine` |

`source` 为 `flux`。需要叫人的 Flux 告警照旧发到 `POST /api/v2/alerts`；变更事件从不叫人，也不发到告警群。

变更事件保留 `changes.keep`（默认 `720h`，即 30 天），最多 50,000 条。`GET /api/v1/changes` 和 MCP 工具 `list_changes`（作用域 `status:read`）按时间倒序列出，可按 `service`、`machine`、`source` 和 `since`（`2h`、`7d` 或一个时间；默认最近一天）筛选。

一个令牌每分钟最多发 60 条变更事件；超出的会被拒绝，返回 `429` 和 `changes.too_many`，到下一分钟才能再发，并计入 `linkos_gateway_changes_limited_total{token}`。把事件合并发送、或者分散开发的发送方，碰不到这个限制。

## 自行续期的令牌 {#tokens-that-renew-themselves}

Agent 的令牌就是它在 REST API、MCP 服务和 SSH 通道上的身份。与其让人发一个长期令牌、到期再发一个，不如让令牌自己续期：到期之前，持有者申请一个新密钥，拿到的令牌名字、作用域、subject、机器和 SSH 公钥都不变，从现在起再有效一个生存期（用 `-expires 90d` 创建的令牌，每次续期都再给 90 天）。除了令牌本身什么都不需要：这两个接口接受任何作用域。

| 路径、工具 | 作用 |
| --- | --- |
| `GET /api/v1/token`、`whoami` | 令牌在持有者眼里的样子：`name`、`scopes`、`machines`、`ssh_keys`（指纹）、`created`、`expires`、`lifetime`、`renewed`（上次续期）、`renewable` 和 `renewable_at`；从不包含密钥 |
| `POST /api/v1/token/renew`、`renew_token` | 新密钥（`secret`，只显示这一次）和令牌；`old_secret_valid_until` 说明被替换的旧密钥还能用到什么时候 |

续期后旧密钥还能用十分钟，客户端换令牌文件时不会有竞争，刚读过文件的进程也照常工作。一个令牌每 24 小时最多续期一次（`token.too_soon`，`429`）。已登记机器的令牌（probe、host、heartbeat）从不自行续期：用 `linkos-beacon enroll … -rotate` 替换它们（`token.not_renewable`，`403`）。每次续期都记进审计日志（`token_renew`）并回显到告警群（「令牌 claude-code 自行续期了，到期日 2027-01-31」），`linkos-beacon token list` 显示每个令牌的生存期和上次续期时间。SSH 登录凭公钥，续期不影响它：令牌的身份不变，变的只有密钥和到期时间。

Agent 的工具链每天跑一次这样的脚本（cron 或 systemd timer），Agent 每次调用时读令牌文件：

```bash
#!/bin/sh
# Renew the Beacon token in $TOKEN_FILE and swap the file atomically.
set -eu
TOKEN_FILE=${TOKEN_FILE:-/etc/linkos-oncall/beacon-token}
BEACON=${BEACON:-https://status.example.com}
resp=$(curl -sS -w '\n%{http_code}' -X POST -H "Authorization: Bearer $(cat "$TOKEN_FILE")" "$BEACON/api/v1/token/renew")
code=$(printf '%s\n' "$resp" | tail -n 1)
case $code in
  200) ;;
  429) exit 0 ;;     # renewed less than a day ago: the token is still good
  *) echo "renewing the Beacon token failed: HTTP $code" >&2; exit 1 ;;
esac
umask 077
printf '%s\n' "$resp" | sed '$d' | jq -r .secret > "$TOKEN_FILE.tmp"
mv -f "$TOKEN_FILE.tmp" "$TOKEN_FILE"     # readers see the old file or the new one, never half of either
```

令牌的生存期要比续期间隔长几倍（每天续期就用 `-expires 30d`），工具链停一阵子也不至于丢了令牌；真丢了，照旧由人用 `linkos-beacon token create` 新建一个。泄露的令牌会一直续期下去，直到被撤销（`linkos-beacon token revoke NAME`）：群里能看到每一次续期，`token list` 显示最近一次。

## Beacon 在这里不做什么 {#what-beacon-does-not-do-here}

- 它不运行 Agent，也不决定 Agent 做什么，不调用任何模型。它只唤醒 Agent、回答它的查询、记下它做了什么。
- 它不要求任何人审批：备注、会话和时间线都摆在那里给人看，SSH 通道的暂停开关就是刹车。
