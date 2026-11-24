# Send alerts to a notification webhook

Beacon sends signed notifications to a receiver you run. The receiver can record them or relay them to your team's chat. People sign in to Beacon to acknowledge the incident, leave a note or check the evidence. Agent wake-ups use a [separate integration](webhooks.md).

## Configure a receiver

Use a dedicated test destination first. Put its URL in the file named by `url_file`, and a shared random secret in the file named by `secret_file`. Give the receiver the same secret. Use HTTPS when requests cross a network you do not control. For a receiver on the same machine, loopback HTTP works if Beacon can reach it from its own network.

```yaml
notification_webhook:
  url_file: /etc/beacon/secrets/notification-webhook-url
  secret_file: /etc/beacon/secrets/notification-webhook-secret

escalation:
  critical: {channel: notification_webhook}
  warning: {channel: notification_webhook}
  info: {channel: notification_webhook}
```

Use a test URL such as `https://notifications.example.com/beacon-test`. Check the configuration with `beacon check-config`, which reports `notification webhook configured`, then restart Beacon. If you omit `escalation`, Beacon selects the notification webhook when neither a Feishu app nor a Feishu group bot is configured. Set `channel` explicitly to direct specific severities to the webhook alongside Feishu. You can still schedule phone escalation per severity. [Escalation settings](../reference/config.md#escalation-and-acknowledgements)

## Verify and accept a notification

Beacon posts `application/json` with an `X-Beacon-Signature` header. The header is `sha256=` followed by the hex HMAC-SHA256 of the exact body bytes. Check it in constant time before decoding JSON. Read the shared secret as text and trim surrounding whitespace. Each attempt allows 10 seconds. A redirect counts as a failed submission.

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

- `event` is `firing` or `resolved`, and `at` is an RFC 3339 timestamp. `severity` is `critical`, `warning` or `info`. Render `title` as untrusted plain text.
- Each event has an opaque `delivery` ID. Retries keep that ID. Use it as a unique key in durable storage and record the notification or enqueue the relay in the same transaction. Keep the key for the retry window. If the delivery is already recorded, return success without doing the work again.
- `alert.id` identifies the alert group, and `alert.episode` identifies one occurrence. Beacon queues `firing` before `resolved` for an occurrence and delivers each group's pending notifications in order. A later recurrence of the group gets a new episode. When `public_url` is configured, the optional `alert.url` opens the same incident through sign-in. The signed-in account needs permission to view or act on the alert.
- Return any `2xx` after recording or durably queuing the notification. Beacon usually retries a failed submission after one minute and retains pending deliveries across restarts for up to seven days from queuing. Expired deliveries are recorded in the audit log. A `2xx` records receiver acceptance. To verify human delivery, check the destination chat or device.

## Rehearse delivery

1. Connect a test receiver, or run the isolated check from the repository root with a local Beacon binary that supports this channel. The check listens on loopback and uses temporary credentials. It rejects the first signed request, restarts Beacon, then checks the retry ID, recovery order, signature, duplicate handling and the incident link.

   ```bash
   python3 deploy/smoke-notification-webhook.py /path/to/beacon
   ```

2. With two probe locations reporting on a test Beacon, [run a drill](../guide/drills.md). Check the signed `firing` notification and open the incident link with a test on-call account. After ending the drill, check that a `resolved` notification arrived with the same episode and a different delivery ID.
