---
sourceHash: sha256-00ad724d
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

Beacon 把一段 JSON POST 到钩子的 `url`：告警组和它的标签，指向证据、时间线和 MCP 端点的链接，以及一句告诉 Agent 从哪里开始的 `hint`。随之有三个请求头：`X-Beacon-Event`（`firing` 或 `resolved`）、`X-Beacon-Delivery`（这次调用的 id，每次重试都一样）和 `X-Beacon-Signature`（用密钥对请求体算出的 HMAC-SHA256）。`resolved` 调用还带 `firing_call`，即这一轮里叫醒 Agent 的那次调用，用来认出还在写备注的那次运行。每个请求头和字段，以及一份完整的载荷，见 [Webhook 参考](../reference/webhooks.md)。

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
