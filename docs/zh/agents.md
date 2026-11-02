# Agent 与告警

[English](../agents.md) | 简体中文

告警触发时，Beacon 可以唤醒一个 Agent，一次调用就把证据交给它，把它留下的备注放到告警卡片上，并把发生的一切记在一条时间线上。Beacon 不调用任何模型，也不替 Agent 做决定：它只负责唤醒、回答 Agent 的查询、记下 Agent 做了什么。运行 Agent 的东西（你自己的一个小服务，或者里面跑着 Claude Code、Codex 的 GitHub workflow）是 Agent 自己的工具链，不属于 Beacon。

## 唤醒 Agent

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
| `url`、`secret_file` | Webhook：POST 到哪里，以及给每次调用签名的密钥 |
| `repository`、`workflow`、`ref`、`token_file` | GitHub：`owner/name`、workflow 的文件名或 id、分支（默认 `main`），以及能触发它的令牌（对这个仓库有 Actions 读写权限的细粒度令牌） |
| `api` | GitHub：默认 `https://api.github.com`，GitHub Enterprise 时改掉 |

新告警组第一次发出通知时（也就是卡片发出时）调用一次钩子，告警持续触发期间不再调用；设了 `resolved` 的话，恢复时再调用一次。告警组恢复后又触发，算新的一轮，会再次唤醒。

这些调用和告警送达各走各的：卡片、加急、Pushover 从不等它们。没有应答、超时、429 或 5xx 时，分别在 5 秒、20 秒、1 分钟、3 分钟后重试；其他拒绝（401、404、422）重试也没用，立即算失败。不跟随重定向，所以载荷和签名只会发到配置的地址。服务重启打断的调用，不到 10 分钟的会接着做完。

每次调用的结果都记在告警组上（`GET /api/v1/alerts/{id}` 和 `get_alert` 里的 `agents`）、审计日志里（`wake_agent`）、卡片上（「10:02 已唤醒 Agent claude-oncall」，或者没能唤醒），并发到告警群，失败时附上原因。

### Webhook

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

读请求体之前，先用原始请求体核对签名；`at` 超过 10 分钟的、`delivery` 见过的，一律拒绝：

```python
import hashlib, hmac

def genuine(secret: bytes, body: bytes, signature: str) -> bool:
    return hmac.compare_digest("sha256=" + hmac.new(secret, body, hashlib.sha256).hexdigest(), signature)
```

载荷里没有任何凭据：Agent 用它自己的令牌取证据（`status:read`；要认领、写备注还要 `alerts:write`）。

### GitHub workflow

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

## 证据

`GET /api/v1/alerts/{id}/evidence` 或 MCP 工具 `get_evidence`（作用域 `status:read`）一次给出 Agent 判断这条告警需要的东西：

- **probes**：告警组所属服务的检测结果，每个检测点、目标、模块一条：窗口结束时是否正常，从什么时候开始失败（或什么时候恢复），失败原因（`probe_failure_info{reason}`：`dns`、`connect`、`timeout`、`tls`、`http_status` 等）以及窗口内各原因失败了几次，耗时，HTTP 状态码，尝试次数（`linkos_probe_attempts`），证书到期时间，以及这个检测点是否已经没有数据。失败的排在前面。`queries` 是用到的 PromQL，想细看时可以带 `metrics:read` 令牌经网关再查。
- **related**：同一 `failure_domain` 里、或者同一服务正在触发的其他告警组。
- **changes**：窗口内的变更事件（见下文），和这个告警组的服务或机器有关的排在前面。
- **done**：认领、静默、备注、唤醒过的 Agent、升级情况，以及窗口内的会话：在告警标签（`machine`、`host`、`location`、`failure_domain`）指到的机器上的，或者由认领过它、给它写过备注的 Agent 发起的。
- **links**：处理手册和面板，以及这条告警和它的时间线的地址。

窗口从告警组开始前 30 分钟起（`?before=2h` 可以更长，最多 24 小时），到现在为止；告警组已恢复的，到恢复后 30 分钟为止。连不上 VictoriaMetrics 时，`probes.error` 会说明，其余部分照常返回。

### 不可信的文本

证据里很多文本来自 Beacon 之外：标签值（由检测点和规则引擎设置）、用它们拼出来的注释、被检测服务的应答、变更事件、钩子的应答、备注，以及在机器上执行的命令。其中任何一段都可能藏着写给 Agent 的指令。凡是带这类文本的返回，都有一个 `untrusted` 对象列出这些字段，并提醒把它们当数据读。Beacon 也会先清理：去掉终端转义序列、控制字符和看不见的字符（双向控制符、零宽字符、Unicode 标签字符），过长的字符串截断，以 `…` 结尾。要放进查询的值会先检查：服务名有可能改变查询时，就不查。

## 备注

`POST /api/v1/alerts/{id}/notes`（请求体 `{"text": "…"}`）或 MCP 工具 `add_note`（作用域 `alerts:write`）在告警组上留一条备注，告警在触发或已恢复都可以：发现了什么、做了什么、接下来做什么。卡片显示最新的三条，每条都发到告警群，审计日志全部保留。备注不等于认领，认领用 `ack_alert`。每条备注最多 2000 个字符，每个告警组保留最新的 100 条。

## 时间线

`GET /api/v1/alerts/{id}/timeline` 或 `get_timeline`（作用域 `status:read`）按时间顺序列出这条告警的经过：`started`、`notified`、`buzzed`、`pushover`、`acknowledged`、`note`、`silenced` 和 `silence_ended`、`agent`（调用过的每个钩子及结果）、`change` 和 `session`（与证据里的相同）、`resolved`。窗口和证据一样。

## 变更事件

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

### Flux

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

## 自行续期的令牌

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

## Beacon 在这里不做什么

- 它不运行 Agent，也不决定 Agent 做什么，不调用任何模型。它只唤醒 Agent、回答它的查询、记下它做了什么。
- 它不要求任何人审批：备注、会话和时间线都摆在那里给人看，SSH 通道的暂停开关就是刹车。
