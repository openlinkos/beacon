---
sourceHash: sha256-90bd204a
---

# Webhook {#webhooks}

Hook 是 Beacon 唤醒 Agent 的方式：一个新的告警群匹配了某个 Hook 并第一次通知时，Beacon 带着群的 id 和取证据的地址去调它；Hook 要求的话，群恢复时再调一次。Beacon 不调用任何模型，也不替 Agent 做决定；Agent 随后做的事都会被记录。Hook 配置在 [config.yaml](config.md#agent-hooks) 的 `agents:` 下；本页讲线上跑的是什么，给写接收端的人：事件、头、负载和签名。

两种 Hook。`webhook` 收到的是签过名的 JSON 负载，`POST` 到它的 URL——本页讲的就是它。`github` 则是触发一个 workflow：向它指定的 workflow 和仓库发 `workflow_dispatch`，带上输入 `alert`（群的 id）和 `event`（`firing` 或 `resolved`），用它的令牌认证；workflow 需要声明这两个输入。

## 事件 {#the-events}

| 事件 | 何时 | 负载 |
| --- | --- | --- |
| `firing` | 匹配该 Hook 的新告警群第一次通知：群的级别在它的 `severities` 里（默认 critical），且它设置的 `service` 正则匹配群的一个服务 | 完整负载，`hint` 说怎么开始：用 `get_evidence` 或 `GET links.evidence` 取证据，读时间线，留备注 |
| `resolved` | 群恢复了，前提是 Hook 要求过（`resolved: true`）且它在这个群的触发周期里被唤醒过 | 同样的负载，多了 `firing_call` 说明唤醒 Agent 的那次调用，`hint` 指向时间线和收尾备注 |

`resolved` 调用会先等 Hook 的 `resolved_after`（默认一分钟），让 Agent 上一轮先把收尾备注写完再被告知恢复。再次触发的群是一个新的周期，会重新唤醒 Hook。

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

头没有签名；以请求体为准，头只能与之一致（`delivery` 和 `event` 必须匹配）。Beacon 不跟随重定向：负载和签名只去配置的地址，哪儿也不去。一次尝试 10 秒；`202` 或任何 `2xx` 都算送达。

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
  "untrusted": {"fields": ["alert.title", "alert.description", "alert.service", "alert.services", "alert.labels"], "note": "…"}
}
```

| 字段 | 含义 |
| --- | --- |
| `delivery` | 这次调用的 id，16 个十六进制字符；每次重试都相同，接收端可以据此拒绝重复 |
| `event` | `firing` 或 `resolved` |
| `at` | 事件发生的时间；真的投递不会晚太多，接收端可以拒绝过旧的 |
| `hook` | Hook 的名字，即配置里的名字 |
| `alert` | 这个群：`id`、`status`、`severity`、`title`、`description`、`service`——群里有多个服务时是 `services`——告警共享的 `labels`、触发中的 `alerts` 数、`starts_at`，恢复后还有 `resolved_at` |
| `links` | 去哪读更多：REST 路径（`status:read`）、MCP 端点、规则写的 `runbook` 和 `dashboard`，以及 `page`，控制台里告警页的签名链接，不需要令牌 |
| `firing_call` | `resolved` 负载里：该 Hook 在这个周期唤醒 Agent 的那次调用——`delivery`、时间（`at`）、结果（`status`），以及 `recent`：调用距群恢复不到十分钟，它启动的那轮可能还在写备注 |
| `hint` | 一句指引；触发负载说怎么开始，恢复负载说怎么收尾 |
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

这就是 `beacon-oncall`（`internal/oncall` 里的参考接收器）的检查；它还会拒绝 24 小时内见过的 delivery、头与请求体不一致的、以及超过 15 分钟的——调用在事件后约五分钟内就会重试完，真的投递不会那么晚。为每个投递跑程序的接收端（`beacon-oncall`，或你自己写的）先立刻应答再运行，负载放在程序的标准输入上。

## 投递与重试 {#delivery-and-retries}

失败的调用会重试：5 秒、20 秒、1 分钟、3 分钟之后各一次——至多五次尝试，前后约五分钟。值得重试的是：没有应答、超时、`408`、`429` 和 `5xx`；其他拒绝（token 不对、workflow 不存在）是终局。同时至多八个调用在跑。每次调用的结果——pending、`ok` 或 `failed`，连同尝试次数——留在告警群上、审计日志里、卡片上和告警群里。重启会接手十分钟内仍在 pending 的调用，更早的放弃；等待中的调用（`resolved_after`）到期才发出，期间群再次触发的话就不再发。
