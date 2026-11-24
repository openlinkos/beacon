---
sourceHash: sha256-f163b038
---

# 通过通知 Webhook 发送告警

Beacon 将带签名的通知发给你运行的接收端。接收端可以记录通知，也可以转发到团队的聊天群。值班人登录 Beacon 后，可以认领、备注或查看证据。唤醒 Agent 使用[单独的接入方式](webhooks.md)。

## 配置接收端 {#configure-a-receiver}

先使用专用的测试接收端。把接收地址写入 `url_file` 指定的文件，把共享随机密钥写入 `secret_file` 指定的文件。接收端使用同一份密钥。请求经过你无法控制的网络时使用 HTTPS。接收端和 Beacon 在同一台机器时，只要 Beacon 能从自己的网络访问它，也可使用回环地址上的 HTTP。

```yaml
notification_webhook:
  url_file: /etc/beacon/secrets/notification-webhook-url
  secret_file: /etc/beacon/secrets/notification-webhook-secret

escalation:
  critical: {channel: notification_webhook}
  warning: {channel: notification_webhook}
  info: {channel: notification_webhook}
```

测试地址示例为 `https://notifications.example.com/beacon-test`。运行 `beacon check-config`，确认输出 `notification webhook configured`，然后重启 Beacon。如果省略 `escalation`，且没有配置飞书应用或飞书群机器人，Beacon 会选择通知 Webhook。也配置了飞书时，可为特定级别的告警显式指定 `channel`。各级告警的定时手机升级仍可分别设置。[升级配置](../reference/config.md#escalation-and-acknowledgements)

## 验签并接收通知 {#verify-and-accept-a-notification}

Beacon 以 `application/json` 发送 POST 请求，并带有 `X-Beacon-Signature` 请求头。请求头是 `sha256=` 加上对请求体原始字节计算出的十六进制 HMAC-SHA256。先以常量时间校验签名，再解析 JSON。从文件读取共享密钥时，按文本读取并去掉首尾空白。每次尝试最多等待 10 秒，重定向视为提交失败。

```python
import hmac

def valid_signature(secret: bytes, raw_body: bytes, signature: str) -> bool:
    expected = "sha256=" + hmac.digest(secret, raw_body, "sha256").hex()
    return signature.isascii() and hmac.compare_digest(expected, signature)
```

```json
{
  "version": 1,
  "delivery": "4ba9a1c23fd8e017",
  "event": "firing",
  "at": "2026-11-03T09:24:00Z",
  "alert": {
    "id": "0cdfbada62dd",
    "episode": "57ee04bfe414d211",
    "severity": "critical",
    "title": "drill is down",
    "url": "https://status.example.com/console/sign-in?next=%2Fconsole%2Falerts%2F0cdfbada62dd"
  }
}
```

- `event` 为 `firing` 或 `resolved`，`at` 是 RFC 3339 时间戳。 `severity` 为 `critical`、`warning` 或 `info`。将 `title` 作为不可信的纯文本显示。
- 每个事件有一个不透明的 `delivery` ID，重试沿用同一个 ID。将它用作持久化存储的唯一键，并在同一个事务内记录通知或放入转发队列。在重试期间保留此键。如果已有记录，直接返回成功，避免重复处理。
- `alert.id` 标识告警组，`alert.episode` 标识一次告警过程。同一次告警先将 `firing` 入队，再将 `resolved` 入队。同组待发送的通知按顺序投递。同组告警再次触发时会获得新的 `episode`。配置了 `public_url` 时，可选字段 `alert.url` 会在登录后打开同一条告警。账号仍需具备查看或处理告警的权限。
- 记录通知或写入持久队列后，返回任意 `2xx`。提交失败通常在一分钟后重试。待发送通知在 Beacon 重启后也会保留，最长保留至入队后七天，过期记录可在审计日志中查看。`2xx` 证明接收端受理，人工收件须到目标群或设备核验。

## 演练送达 {#rehearse-delivery}

1. 接入测试接收端，或从代码仓库根目录用支持此渠道的本机 Beacon 二进制文件运行隔离验收。脚本只监听回环地址，使用临时凭据。脚本先拒绝第一次带签名的请求，再重启 Beacon，核对重试 ID、恢复顺序、签名、去重处理和告警链接。

   ```bash
   python3 deploy/smoke-notification-webhook.py /path/to/beacon
   ```

2. 测试 Beacon 上至少有两个检测点上报后，[运行一次演练](../guide/drills.md)。检查带签名的 `firing` 通知，再用测试值班账号打开告警链接。结束演练后，确认收到 `resolved`，其 `episode` 相同，`delivery` ID 不同。
