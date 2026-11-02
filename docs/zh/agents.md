---
sourceHash: sha256-24db1f6e
---

# 让 Agent 值班

服务出问题时，Beacon 可以叫醒你指定的 Agent，一次调用就把证据交给它，让它登上机器处理，修完让每个检测点重新检测，并把发生的一切记在一条时间线上，人随时可以看。这一页写给负责搭建这套流程的人：你已经装好 Beacon，Agent 也已经通过 MCP 接入，也就是做完了[30 分钟装好](install.md)的第 1 到 4 步。下面依次讲 Agent 怎样得知告警、拿到什么、怎样处理告警，以及它的令牌怎样一直有效、不用人再发新的。

Beacon 不调用任何模型，也不替 Agent 做决定。它只负责叫醒 Agent、回答 Agent 的查询、记下 Agent 做了什么。告警怎么处理由 Agent 决定，决定发生在运行 Agent 的程序里（下文称为 **harness**），比如下面的 `beacon-oncall`、一个 GitHub workflow，或者你自己写的程序。harness 不属于 Beacon。

## 告警怎样传到 Agent {#how-an-alert-reaches-an-agent}

1. **检测失败，规则触发。** 属于同一件事的告警（默认是名称和服务都相同的告警）合成一个**告警组**：一张卡片、一次钩子调用、一个 id。
2. **Beacon 调用你配置的钩子。** 钩子是配置里列出的一个带签名的 webhook，或一次 GitHub workflow 触发。调用里写明是哪个告警组、证据在哪里，不带任何凭据。
3. **harness 启动 Agent**，把调用的内容交给它。
4. **Agent 用自己的凭据查询证据**（`get_evidence`）：每个检测点看到了什么、为什么失败，同一故障域里还有哪些告警，故障前刚改了什么，已经有人做了什么。
5. **它到证据指向的机器上排查**：通过 [SSH 通道](ssh.md)，用它自己的 `ssh`，或者用 Beacon 的工具（`run_command`、`read_file`、`edit_file`）。每条命令都有记录。
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
    "title": "git.example.com is down", "description": "git.example.com has failed for 3 minutes from at least half of the probe locations …",
    "service": "git.example.com",
    "labels": {"alertname": "ServiceDown", "service": "git.example.com", "severity": "critical", "failure_domain": "origin-1"},
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

如果告警是关于一个心跳的，而目录里这个心跳写明了它所在的机器（见[心跳](catalog.md#heartbeats)），`firing` 调用的 `hint` 会说出是哪台机器，并提示用 `list_machines` 查看怎么连上去。

`resolved` 的载荷字段相同，其中 `at` 是告警组恢复的时间（不是 `resolved_after` 之后调用发出的时间），另外多一个字段 `firing_call`：这一轮里叫醒 Agent 的那次调用，用来认出那次调用启动的运行，并说明那次运行是否可能还没结束：

```json
"firing_call": {"delivery": "9f3c2a1b7e4d5c60", "at": "2026-11-02T02:00:31Z", "status": "ok", "recent": true}
```

触发调用发生在告警组恢复前不到十分钟时，`recent` 为 true。这时 hint 会提醒：那次调用启动的运行可能还在写备注，先读 `get_timeline`（包括其中的备注），再写自己的收尾备注。

自己写接收程序的话，先用原始请求体校验签名，再解析内容；`at` 超过十分钟的、`delivery` 已经见过的，一律拒绝：

```python
import hashlib, hmac

def genuine(secret: bytes, body: bytes, signature: str) -> bool:
    return hmac.compare_digest("sha256=" + hmac.new(secret, body, hashlib.sha256).hexdigest(), signature)
```

载荷里没有任何凭据：Agent 用自己的令牌查询证据。这个令牌属于一个预设为**值班**的 Agent，能读证据、认领告警、写备注（[无界面的 Agent 和 CI](accounts.md#headless-agents-and-ci)）。

### GitHub workflow {#a-github-workflow}

Beacon 触发 workflow 时带两个输入：`alert`（告警组 id）和 `event`（`firing` 或 `resolved`），workflow 必须声明它们：

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
      # 接下来是 Agent 自己的步骤，带上 Beacon 的 MCP server 和它自己的令牌
```

像上面这样通过 `env` 传入输入值，不要直接写进脚本：能触发这个 workflow 的人都能决定输入的内容。运行 Claude Code 的完整 workflow 见 [GitHub Actions](#in-github-actions)。

## 被叫醒后运行 Agent {#running-an-agent-when-woken}

webhook 的另一头需要有程序来运行 Agent。`beacon-oncall` 是参考实现的接收程序，和 `beacon`、`beacon-probe` 一起发布。它接收 webhook、校验签名，然后启动你指定的程序：载荷从标准输入传给程序，告警信息放在环境变量里。它不做任何决定，这么薄是有意的：Claude Code、Codex 或任何读环境变量的程序都可以跑在它后面。`contrib/oncall` 里有前两者的包装脚本、给被叫醒的 Agent 用的提示词，以及一个 systemd 单元。

```bash
beacon-oncall -secret-file /etc/beacon-oncall/hook-secret \
  -run /usr/local/lib/beacon-oncall/claude-code.sh -log-dir /var/lib/beacon-oncall/logs
```

| 参数 | 含义 |
| --- | --- |
| `-listen` | 在哪里接收 webhook；默认 `:17007`。Beacon 里钩子的 `url` 指向这里；除非两者在同一个内网，中间要经过你的 TLS 反向代理 |
| `-secret-file` | 钩子的密钥：内容和 Beacon 里钩子的 `secret_file` 相同 |
| `-run` | 处理 `firing` 事件的程序，通过 `/bin/sh -c` 运行 |
| `-run-resolved` | 处理 `resolved` 事件的程序；不设的话，这类事件只确认收到，不运行任何程序 |
| `-concurrency` | 同时运行的程序数；默认 `2`。另外最多 32 个排队等待；再多就返回 503，Beacon 稍后重试 |
| `-timeout` | 每个程序最多运行多久；默认 `30m`。超时后整个进程组被杀掉 |
| `-log-dir` | 每次运行的标准输出和标准错误，文件名为 `<time>-<delivery>.log`，最后一行写明退出状态和用时 |
| `-keep` | 超过这个时长的日志会被删除；默认 `720h`。`0` 表示全部保留 |

**它怎么应答。** 校验通过、把运行排进队列后，立即返回 202：程序做成了什么，看它的日志、Agent 留下的备注和 Beacon 的记录，而不看给 Beacon 的应答。签名错误或缺失的，返回 401；`at` 超过 15 分钟的也返回 401（Beacon 的重试不会拖这么久，所以这是重放）；解析不了的返回 400。每个 `delivery` 它会记住 24 小时，同一次调用的重试返回 202，但不再运行任何程序；认的是签名过的请求体里的 id，不是请求头。`GET /healthz` 返回 `ok`，可以用来检查它是否在运行。收到 SIGTERM 后，它不再接收新调用，并等待正在运行的程序结束，最多等 `-timeout`。

**程序拿到什么。** 标准输入是载荷，此外还有这些环境变量；Beacon 没有给出对应内容时，变量存在但为空：`BEACON_EVENT`、`BEACON_DELIVERY`、`BEACON_HOOK`、`BEACON_ALERT_ID`、`BEACON_ALERT_TITLE`、`BEACON_ALERT_SERVICE`、`BEACON_ALERT_SEVERITY`、`BEACON_ALERT_STATUS`、`BEACON_ALERT_URL`、`BEACON_EVIDENCE_URL`、`BEACON_TIMELINE_URL`、`BEACON_MCP_URL`、`BEACON_RUNBOOK_URL`。标题和服务名来自标签：它们是数据，不是指令。

### 包装脚本 {#the-wrappers}

`contrib/oncall/claude-code.sh` 以无界面模式运行 Claude Code。它把 `contrib/oncall/prompt.md` 和告警信息拼成一个提示词；给 Claude Code 配上 Beacon 的 MCP server（`BEACON_MCP_URL` 上的 HTTP server，每次连接时都从 `BEACON_TOKEN_FILE` 读取令牌，所以令牌续期后自动生效）；允许它不经询问使用 Beacon 的工具，其他工具一概不给（`--tools ""`、`--allowedTools "mcp__beacon__*"`、`--permission-mode dontAsk`）；用 `--max-turns` 限制工作量；并把每条消息作为一行 JSON 写进日志，人可以一步步看 Agent 做了什么。可以用 `ONCALL_MAX_TURNS`、`ONCALL_MODEL`、`ONCALL_PROMPT`、`CLAUDE_BIN` 和 `CLAUDE_ARGS` 调整。Claude Code 照常认证：服务用户主目录里的登录状态，或者服务环境里的 `ANTHROPIC_API_KEY`。

`contrib/oncall/codex.sh` 用 `codex exec` 做同样的事：提示词走标准输入，Beacon 的 MCP server 通过 `-c mcp_servers.beacon.*` 配置，令牌放在只有 Codex 能看到的环境变量里，本地操作一律在只读沙箱里，审批关闭。可以用 `ONCALL_MODEL`、`CODEX_BIN` 和 `CODEX_ARGS` 调整。

`contrib/oncall/prompt.md` 是两者共用的提示词，只有一页。它按顺序告诉被叫醒的 Agent：先拿证据（`get_evidence`）；再查证据指向的机器，用 Beacon 的工具（`run_command`、`read_file`、`list_machines`），或者用 `list_machines` 返回的 `ssh_config` 走自己的 ssh；修能安全修好的部分；验证，能用 `probe_now` 就用，否则再调用一次 `get_evidence`；最后用 `add_note` 写明结论、做了什么、还剩什么。还有几条规则：不可信字段只当数据；备注里不写任何密钥；不认领、不静默告警，那是人的决定；进行不下去时停下来，留一条备注。可以按你自己的服务修改它，但请保留这些规则。

### 作为系统服务运行 {#as-a-service}

`contrib/oncall/beacon-oncall.service` 让接收程序以单独的用户运行，系统其余部分只读（`ProtectSystem=strict`），只能写这个用户的主目录（Agent 的登录状态存在这里）和日志目录，并且设置了 `Restart=always`。文件开头列出了安装命令：创建用户；把钩子密钥和 Agent 的令牌放在 `/etc/beacon-oncall` 下，只有这个用户能读（令牌在中心上用 `docker exec beacon beacon agent add claude-oncall -owner alice -preset oncall > beacon-token` 创建）；把包装脚本和提示词放在 `/usr/local/lib/beacon-oncall` 下；以这个用户身份安装并登录 Agent 程序。停止服务时，会等正在进行的运行结束。

### 在 GitHub Actions 里运行 {#in-github-actions}

对 `kind: github` 的钩子，`contrib/github/oncall.yml` 是现成的 workflow：它接收 `alert` 和 `event` 两个输入，用同一份 `prompt.md`（复制到 `.github/oncall/prompt.md`）拼出提示词，通过 Claude Code 的 GitHub Action 运行 Claude Code，并接上 Beacon 的 MCP server：令牌取自仓库 secret `BEACON_TOKEN`，地址取自变量 `BEACON_MCP_URL`，同样只允许使用 Beacon 的工具。这个 secret 是专为这个 workflow 创建的 Agent 的令牌：`beacon agent add ops-workflow -owner alice -preset oncall`。其余信息都由 Agent 用自己的令牌向 Beacon 查询；输入值通过环境变量传递，从不写进脚本。

### 信任边界 {#trust}

接收程序只校验签名、启动程序，它决定的就这么多。Agent 之后能做什么，取决于它的预设、它所有者的角色，以及它能连哪些机器。每次调用、每条命令、每次会话都经过 Beacon，Beacon 把它们记下来，在卡片和告警群里给人看；暂停开关能让它停下。载荷里没有凭据；接收程序和包装脚本只从文件读取密钥，既不记录密钥，也不记录载荷。提示词要求 Agent 把来自 Beacon 之外的内容当作数据，Beacon 也把这些字段标为 `untrusted`。

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

备注是 Agent 或人告诉其他人「发现了什么、做了什么、接下来做什么」的方式。`POST /api/v1/alerts/{id}/notes`（请求体 `{"text": "…"}`），或者 MCP 工具 `add_note`（作用域 `alerts:write`），在告警组上留一条备注，告警组正在触发或已经恢复都可以。卡片上显示最新的三条，告警群里每条都会出现，审计日志里全部保留。写备注不等于认领告警，认领要用 `ack_alert`。每条备注最多 2000 个字符，每个告警组保留最新的 100 条。以 `public:` 开头的备注，还会去掉前缀，作为事件的进展显示在[状态页](status.md)上。

## 时间线 {#the-timeline}

`GET /api/v1/alerts/{id}/timeline`，或者 `get_timeline`（作用域 `status:read`），按时间顺序讲出告警的经过：`started`、`notified`、`buzzed`、`pushover`、`acknowledged`、`note`、`silenced` 和 `silence_ended`、`agent`（每次调用钩子及其结果）、`change` 和 `session`（与证据里的相同）、`command`（shell 会话 `shell_commands` 里的每条命令，带所属会话的 id 和退出状态）、`heartbeat`（心跳最后一次成功的时间，即使在窗口之前也列出）、`resolved`。它覆盖的时间窗口和证据相同，所以告警前在相关机器上发生的变更和会话排在 `started` 之前；这些事件带着和证据里一样的 `phase`，`heartbeats` 也和证据里一样。控制台的告警页面显示的就是这条时间线。

## 变更事件 {#change-events}

部署、配置修改和 Flux 的同步以变更事件的形式送到 Beacon 之后，告警的证据就能说出故障前刚改了什么。发送方用带 `changes:write` 作用域的令牌，把事件发到网关（17006 端口）：

```bash
docker exec beacon beacon token create ci-deploy -scopes changes:write -expires 365d
```

```bash
curl -fsS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"source": "github-actions", "kind": "deploy", "service": "hub-example-com", "summary": "hub v2.1",
       "url": "https://github.com/example/hub/actions/runs/1"}' \
  https://metrics.example.com/api/v1/changes
```

| 字段 | 含义 |
| --- | --- |
| `source` | 必填：发送方，比如 `github-actions` 或 `deploy-script` |
| `kind` | `deploy`、`config`、`rollback` 或其他词；默认 `change` |
| `summary` | 必填：改了什么 |
| `service`、`machine` | 和什么有关，写法与告警的 `service` 标签、机器名一致 |
| `url`、`revision` | 详情链接；提交号或版本号 |
| `time` | RFC 3339；默认为当前时间；最多比现在晚一小时，也不能早于变更的保留期限 |

不认识的字段会被拒绝，拼错了不会悄悄被忽略。Beacon 会把令牌的名字和每个变更一起记下。

### Flux {#flux}

Flux 的 notification-controller 把事件发给类型为 `generic` 的 Provider。让它指向 `/api/v1/changes/flux`，令牌放在请求头里：

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
    service: hub-example-com    # 这些对象运行的是什么服务
```

| Flux 事件 | 变更事件 |
| --- | --- |
| `reason` | `kind`，比如 `ReconciliationSucceeded` 或 `UpgradeFailed` |
| `involvedObject` | `object`：`Kustomization/flux-system/apps` |
| `message`（有 Alert 的 `summary` 时接在它后面） | `summary` |
| `metadata`：`…/revision` | `revision` |
| `severity`、`timestamp` | `severity`、`time` |
| `metadata`：`service`、`machine`（Alert 的 `eventMetadata`），或地址里的 `?service=…&machine=…` | `service`、`machine` |

`source` 为 `flux`。需要通知人的 Flux 告警，仍然照旧发到 `POST /api/v2/alerts`；变更事件从不通知任何人，也不回显到告警群。

**保留和查询。** 变更事件保留 `changes.keep`（默认 `720h`，即 30 天），最多 50,000 条。`GET /api/v1/changes` 和 MCP 工具 `list_changes`（作用域 `status:read`）按从新到旧列出，可以按 `service`、`machine`、`source` 和 `since`（`2h`、`7d` 或一个时间点；默认最近一天）筛选。

**频率限制。** 每个令牌每分钟最多发 60 个变更事件；超出的返回 `429` 和 `changes.too_many`，直到这一分钟结束，并计入 `beacon_gateway_changes_limited_total{token}`。批量发送或者分散发送，就不会碰到这个限制。

## 自动续期的令牌 {#tokens-that-renew-themselves}

无界面的 Agent，比如被 Hook 唤醒的 Agent 或 CI 任务，用令牌登录 REST API 和 MCP server：令牌是它所属 Agent 的一个凭据，在控制台的 **Agent** 页面或用 `beacon agent add` 创建（[无界面的 Agent 和 CI](accounts.md#headless-agents-and-ci)）。与其让人给它一个长期令牌、过期了再发一个新的，不如让令牌自己续期：快到期前，持有者申请一个新的密钥，拿到的令牌名称、所属 Agent、作用域、主体、机器和 SSH 密钥都不变，有效期从现在起重新计算（用 `-expires 90d` 创建的令牌，每次都再得到 90 天）。除了令牌本身，什么都不需要：两个接口接受任何作用域。Claude Code 这样的交互式客户端改用 OAuth 登录，由它自己刷新令牌。

| 接口、工具 | 内容 |
| --- | --- |
| `GET /api/v1/token`、`whoami` | 调用者是谁，以及持有者看到的凭据信息：`name`、`actor`（审计日志里的称呼，如 `alice / claude-oncall`）、`user`、`agent`、`preset`、`credential`（`token` 或 `oauth`，后者带 `client`）、`scopes`（现在能做的，不超出所有者的角色）、`machines`、`ssh_keys`（指纹）、`created`、`expires`、`lifetime`、`renewed`（上次续期时间）、`renewable` 和 `renewable_at`；从不包含密钥本身 |
| `POST /api/v1/token/renew`、`renew_token` | 新的密钥（`secret`，只显示这一次）和令牌信息；`old_secret_valid_until` 说明被替换的旧密钥还能用到什么时候 |

- **没有竞争问题。** 续期后旧密钥还能用十分钟，客户端可以从容替换令牌文件，刚读过旧文件的进程也不受影响。
- **每天最多一次。** 一个令牌每 24 小时最多续期一次（`token.too_soon`，`429`）。
- **机器的令牌和 OAuth 不续期。** 登记过的机器（检测点、主机、心跳）的令牌从不自动续期，要用 `beacon enroll … -rotate` 替换（`token.not_renewable`，`403`）。OAuth 访问令牌也不续期，由它的客户端刷新。
- **都有记录。** 每次续期都记入审计日志（`token_renew`），并回显到告警群（「令牌 claude-oncall 自行续期了，到期日 2027-01-31」）；`beacon token list` 显示每个令牌的有效期和上次续期时间。
- **SSH 登录靠密钥**，续期不影响它：令牌的身份不变，变的只有密钥和到期时间。

harness 每天运行一次类似下面的脚本（cron 或 systemd 定时器），Agent 每次调用都重新读取令牌文件：

```bash
#!/bin/sh
# 续期 $TOKEN_FILE 里的 Beacon 令牌，并原子地替换这个文件。
set -eu
TOKEN_FILE=${TOKEN_FILE:-/etc/beacon-oncall/beacon-token}
BEACON=${BEACON:-https://status.example.com}
resp=$(curl -sS -w '\n%{http_code}' -X POST -H "Authorization: Bearer $(cat "$TOKEN_FILE")" "$BEACON/api/v1/token/renew")
code=$(printf '%s\n' "$resp" | tail -n 1)
case $code in
  200) ;;
  429) exit 0 ;;     # 一天内已经续期过：令牌仍然有效
  *) echo "renewing the Beacon token failed: HTTP $code" >&2; exit 1 ;;
esac
umask 077
printf '%s\n' "$resp" | sed '$d' | jq -r .secret > "$TOKEN_FILE.tmp"
mv -f "$TOKEN_FILE.tmp" "$TOKEN_FILE"     # 读取方看到的要么是旧文件，要么是新文件，不会是半个
```

令牌的有效期要比续期间隔长好几倍（每天续期的话，用 `-expires 30d`），这样 harness 停机一段时间也不会丢掉令牌。真丢了，由 Agent 的所有者在 Agent 页面上点**新令牌**再发一个。泄露的令牌会一直自己续期，直到被吊销（Agent 页面上的**吊销**，或 `beacon token revoke NAME`），吊销同时会立即关闭它在机器上打开着的一切。告警群能看到每次续期，`token list` 能看到最近一次。

## Beacon 在这里不做什么 {#what-beacon-does-not-do-here}

- 它不运行 Agent，不替 Agent 做决定，也不调用任何模型。它只叫醒 Agent、回答 Agent，并记下 Agent 做了什么。
- 它不要求任何人审批。备注、会话和时间线都摆在那里给人看，SSH 通道的暂停开关就是刹车。
