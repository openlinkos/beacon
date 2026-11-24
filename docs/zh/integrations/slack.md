---
sourceHash: sha256-d79807bf
---

# 将告警发送到 Slack

Beacon 通过传入 Webhook 将告警触发和恢复消息发送到 Slack 频道。设置 `public_url` 后，用户从消息登录控制台即可打开同一事件。值班人员可以用自己的 Beacon 账号认领、查看证据和交接。

## 设置频道 {#set-up-the-channel}

1. 在 Slack 应用中[启用传入 Webhook](https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks/)，并为接收告警的频道授权。首次演练用测试频道。
2. 把 Webhook URL 存到 Beacon 可读的文件中。URL 内含凭据，需要放在版本控制之外，和部署的其他密钥文件一起挂载。
3. 设置 `public_url` 为收件人登录控制台时使用的地址，并把需要发送到 Slack 的告警级别设为 `slack`。例如：

```yaml
public_url: https://beacon.example.com
slack:
  webhook_url_file: /etc/beacon/secrets/slack-webhook-url
escalation:
  critical: {channel: slack}
  warning: {channel: slack}
  info: {channel: slack}
```

运行 `beacon check-config`，确认出现 `Slack webhook configured` 后重启 Beacon。省略整个 `escalation` 段，且未配置飞书渠道和带签名的通知 Webhook 时，默认使用 Slack。Slack 渠道也可与单独的[手机升级](../reference/config.md#escalation-and-acknowledgements)一起配置。

在 HA 部署中，先升级两个中心再加入 Slack 设置。回退到较早版本时，先恢复对应的配置，再运行旧版安装程序。

## 验证送达与交接 {#verify-delivery-and-handover}

运行一次[演练](../guide/drills.md)，确认测试频道收到触发与恢复消息。从消息打开告警，用值班账号登录，并让接班人在同一事件确认看到交接。**严重告警通知**和事件时间线会记录 Slack 受理或最近一次失败。实际送达需要在收件人的 Slack 中核对。

Beacon 将 Slack 消息持久化排队，提交失败时重试。同一事件同一渠道的触发消息先于恢复提交，重启后也按此顺序。重试至少等一分钟，Slack 要求更长间隔时以它为准。未送达的消息保留七天。Slack 传入 Webhook 没有用于去重的投递编号。如果 Slack 已接收消息而 Beacon 未收到应答，重试可能显示两条消息，两条都会指向同一事件。
