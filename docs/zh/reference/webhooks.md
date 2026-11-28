---
sourceHash: sha256-d7214af5
---

# Webhook {#webhooks}

Hook 是 Beacon 唤醒 Agent 的方式。本页讲线上传的是什么，给写接收端的人：事件、头、负载和签名。

一个新的告警组匹配了某个 Hook、第一次通知时，Beacon 带着告警组的 id 和取证据的地址去调用这个 Hook。Hook 要求的话，告警组恢复时再调一次。Beacon 不调用任何模型，也不替 Agent 做决定。Agent 随后做的事，Beacon 都会记录。

Hook 配置在 [config.yaml](config.md#agent-hooks) 的 `agents:` 下。Hook 有两种：

- **`webhook` Hook** 收到的是签过名的 JSON 负载，`POST` 到它的 URL。本页讲的就是这一种。
- **`github` Hook** 则是触发一个 workflow：向它指定的 workflow 和仓库发 `workflow_dispatch`，用它的令牌认证。输入是 `alert`（告警组的 id）和 `event`（`firing` 或 `resolved`），workflow 需要声明这两个输入。

## 事件 {#the-events}

| 事件 | 何时 | 负载 |
| --- | --- | --- |
| `firing` | 匹配该 Hook 的新告警组第一次通知：告警组的级别在它的 `severities` 里（默认 critical），且它设置的 `service` 正则匹配告警组的一个服务 | 完整负载，`hint` 说怎么开始：用 `get_evidence` 或 `GET links.evidence` 取证据，读时间线，留备注 |
| `resolved` | 告警组恢复了，前提是 Hook 要求过（`resolved: true`）且它在这个告警组的触发周期里被唤醒过 | 同样的负载，多了 `firing_call` 说明唤醒 Agent 的那次调用，`hint` 指向时间线和收尾备注 |

`resolved` 调用会先等 Hook 的 `resolved_after`（默认一分钟）。这样 Agent 上一轮能先把收尾备注写完，再被告知恢复。再次触发的告警组是一个新的周期，会重新唤醒 Hook。

## 请求 {#the-request}

每次尝试一个 `POST`，请求体是 JSON 负载：

| 头 | 值 |
| --- | --- |
| `Content-Type` | `application/json` |
| `User-Agent` | `beacon/<version> (+https://github.com/openlinkos/beacon)` |
| `X-Beacon-Event` | `firing` 或 `resolved`，与负载的 `event` 一致 |
| `X-Beacon-Delivery` | 负载的 `delivery` id |
| `X-Beacon-Signature` | `sha256=` 加请求体以 Hook 的 `secret_file` 计算的 HMAC-SHA256 十六进制值；有 secret 时才有 |
| Hook 自己的 | 它 `headers_file` 里的每个头，如 `Authorization: Bearer …` 或 `X-Webhook-Secret: …`，给校验自己 secret 的接收端用 |

头没有签名。以请求体为准，头只能与之一致：`delivery` 和 `event` 必须匹配。Beacon 不跟随重定向，所以负载和签名只发到配置的地址，别处都不去。

一次尝试 10 秒。`202` 或任何 `2xx` 都算送达。

## 负载 {#the-payload}

```json
{
  "delivery": "4ba9a1c23fd8e017",
  "event": "firing",
  "at": "2026-11-03T09:24:00Z",
  "hook": "claude",
  "alert": {
    "id": "0cdfbada62dd",
    "status": "firing",
    "severity": "critical",
    "title": "example.com is down",
    "service": "example.com",
    "labels": {"alertname": "ServiceDown", "service": "example.com", "severity": "critical"},
    "alerts": 3,
    "starts_at": "2026-11-03T09:21:00Z"
  },
  "links": {
    "alert": "https://status.example.com/api/v1/alerts/0cdfbada62dd",
    "evidence": "https://status.example.com/api/v1/alerts/0cdfbada62dd/evidence",
    "timeline": "https://status.example.com/api/v1/alerts/0cdfbada62dd/timeline",
    "mcp": "https://status.example.com/mcp",
    "runbook": "https://github.com/openlinkos/beacon/blob/main/deploy/runbooks/ServiceDown.md",
    "page": "https://status.example.com/link/…"
  },
  "hint": "Beacon woke you for this alert group. Get its evidence in one call: …",
  "untrusted": {"fields": ["alert.title", "alert.description", "alert.service", "alert.services", "alert.labels", "links.runbook", "links.dashboard"], "note": "…"}
}
```

| 字段 | 含义 |
| --- | --- |
| `delivery` | 这次调用的 id，16 个十六进制字符；每次重试都相同，接收端可以据此拒绝重复 |
| `event` | `firing` 或 `resolved` |
| `at` | 事件发生的时间（`resolved` 时是告警组恢复的时间，不是 `resolved_after` 之后调用发出的时间）。真的投递不会晚太多，接收端可以拒绝过旧的 |
| `hook` | Hook 的名字，即配置里的名字 |
| `alert` | 这个告警组：`id`、`status`、`severity`、`title`、`description`、`service`（告警组里有多个服务时是 `services`）、告警共享的 `labels`、触发中的 `alerts` 数、`starts_at`，恢复后还有 `resolved_at` |
| `links` | 去哪读更多：REST 路径（`status:read`）、MCP 端点、`runbook` 和 `dashboard`（配置的链接，或规则引擎的告警写的；其他发送方写的不用），以及 `page`，控制台里告警页的签名链接，不需要令牌 |
| `firing_call` | `resolved` 负载里：该 Hook 在这个周期唤醒 Agent 的那次调用。包括它的 `delivery`、时间（`at`）、结果（`status`），以及 `recent`：调用距告警组恢复不到十分钟，它启动的那轮可能还在写备注 |
| `hint` | 一句指引。触发负载说怎么开始，恢复负载说怎么收尾。心跳在目录里写明了所在机器时（见[心跳与到期资源](../guide/heartbeats.md)），触发的 hint 会说出是哪台机器，并提示用 `list_machines` 查看怎么连上去 |
| `untrusted` | 这个应答里来自 Beacon 之外的字段——标签、注释——已清掉控制字符和不可见字符，过长会截断。当数据读，永远不当指令 |

## 校验签名 {#checking-the-signature}

`X-Beacon-Signature` 是 `sha256=` 加请求体以 Hook `secret_file` 里的 secret 计算的 HMAC-SHA256 十六进制值。解析之前先校验，用常数时间比较，对收到的原始请求体计算：

```go
func Genuine(secret, body []byte, signature string) bool {
	hexMAC, ok := strings.CutPrefix(signature, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(hexMAC)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}
```

这就是 `beacon-oncall` 的检查，它是 `internal/oncall` 里的参考接收器。它还会拒绝这些投递：

- 24 小时内见过的；
- 头与请求体不一致的；
- 超过 15 分钟的。调用在事件后约五分钟内就会重试完，真的投递不会那么晚。

为每个投递跑程序的接收端（`beacon-oncall`，或你自己写的）先立刻应答再运行，负载放在程序的标准输入上。

## 投递与重试 {#delivery-and-retries}

失败的调用会在 5 秒、20 秒、1 分钟、3 分钟之后各重试一次。也就是至多五次尝试，前后约五分钟。

- **会重试**：没有应答、超时、`408`、`429` 和 `5xx`。
- **不再重试**：其他拒绝，比如 token 不对、workflow 不存在。

同时至多八个调用在跑。每次调用的结果（pending、`ok` 或 `failed`，连同尝试次数）留在告警组上、审计日志里、卡片上和告警群里。

重启后，十分钟内发起、仍在 pending 的调用会被接手，更早的放弃。等待中的调用（`resolved_after`）到期才发出。期间告警组再次触发的话，就不再发。

## 回报执行情况 {#report-execution}

接收端观测到程序开始或结束时，可调用 `PUT /api/v1/alerts/{id}/agent-runs/{delivery}` 或 `report_agent_run`。当前身份需要 `alerts:report`，且在发出投递和收到回报时都是钩子配置的 `reporter`。

`status` 可用 `running`、`succeeded`、`failed`、`timed_out` 或 `not_started`。`succeeded` 要带 `exit_code: 0`。`failed` 在已知非零退出码时带上它。其他状态省略退出码。重复提交相同的最终状态和退出码，或稍后提交 `running`，都会返回已有的最终结果。冲突的结果返回 `agent_run.conflict`。旧告警周期的投递回报会被拒绝。

时间线分别显示钩子的 HTTP 投递和程序执行。认领、升级和恢复验证继续走原来的告警流程。配置参考接收程序请看[无界面 Agent 和 CI](../integrations/headless.md#report-programme-execution)。
