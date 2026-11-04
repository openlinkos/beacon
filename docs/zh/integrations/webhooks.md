---
sourceHash: sha256-b57b780a
---

# 被 webhook 唤醒的 Agent

`kind: webhook` 的钩子是一次带签名的 POST，发到你指定的地址；另一头有程序运行 Agent。把它写进 Beacon 的配置：

```yaml
agents:
  - name: claude-oncall
    kind: webhook
    url: https://agents.example.com/hooks/beacon
    secret_file: /etc/beacon/secrets/agent-hook-secret   # 至少 24 个随机字符
```

钩子什么时候被调用、各字段是什么含义、怎样重试、每次调用的结果记在哪里，见[让 Agent 值班](../guide/on-call.md#wake-an-agent)。另一头的 `beacon-oncall` 接收 webhook、校验签名、启动 Agent（[无头 Agent 与 CI](headless.md#beacon-oncall-the-reference-harness)）。

## 调用 {#the-call}

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

如果告警是关于一个心跳的，而目录里这个心跳写明了它所在的机器（见[心跳](../guide/monitoring.md#heartbeats)），`firing` 调用的 `hint` 会说出是哪台机器，并提示用 `list_machines` 查看怎么连上去。

`resolved` 的载荷字段相同，其中 `at` 是告警组恢复的时间（不是 `resolved_after` 之后调用发出的时间），另外多一个字段 `firing_call`：这一轮里叫醒 Agent 的那次调用，用来认出那次调用启动的运行，并说明那次运行是否可能还没结束：

```json
"firing_call": {"delivery": "9f3c2a1b7e4d5c60", "at": "2026-11-02T02:00:31Z", "status": "ok", "recent": true}
```

触发调用发生在告警组恢复前不到十分钟时，`recent` 为 true。这时 hint 会提醒：那次调用启动的运行可能还在写备注，先读 `get_timeline`（包括其中的备注），再写自己的收尾备注。

## 校验签名 {#check-the-signature}

自己写接收程序的话，先用原始请求体校验签名，再解析内容；`at` 超过十分钟的、`delivery` 已经见过的，一律拒绝：

```python
import hashlib, hmac

def genuine(secret: bytes, body: bytes, signature: str) -> bool:
    return hmac.compare_digest("sha256=" + hmac.new(secret, body, hashlib.sha256).hexdigest(), signature)
```

载荷里没有任何凭据：Agent 用自己的令牌查询证据。这个令牌属于一个预设为**值班**的 Agent，能读证据、认领告警、写备注（[无头 Agent 与 CI](headless.md#make-an-agent-and-its-token)）。

## 信任边界 {#trust}

接收程序只校验签名、启动程序，它决定的就这么多。Agent 之后能做什么，取决于它的预设、它所有者的角色，以及它能连哪些机器。每次调用、每条命令、每次会话都经过 Beacon，Beacon 把它们记下来，在卡片和告警群里给人看；暂停开关能让它停下。载荷里没有凭据；接收程序和包装脚本只从文件读取密钥，既不记录密钥，也不记录载荷。提示词要求 Agent 把来自 Beacon 之外的内容当作数据，Beacon 也把这些字段标为 `untrusted`。
