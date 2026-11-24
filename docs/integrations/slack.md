# Send alerts to Slack

Beacon posts firing and recovery messages to a Slack channel through an incoming webhook. With `public_url` set, the message opens the same incident in the console after sign-in. A responder can claim it, examine the evidence and hand it over with their Beacon account.

## Set up the channel

1. [Enable Incoming Webhooks in a Slack app](https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks/) and authorise the app for the channel that should receive alerts. Use a test channel for the first drill.
2. Save the webhook URL in a file readable by Beacon. The URL contains a credential. Keep it outside version control and mount it with the deployment's other secret files.
3. Set `public_url` to the console address where recipients sign in. Select `slack` for each severity to send there. For example:

```yaml
public_url: https://beacon.example.com
slack:
  webhook_url_file: /etc/beacon/secrets/slack-webhook-url
escalation:
  critical: {channel: slack}
  warning: {channel: slack}
  info: {channel: slack}
```

Run `beacon check-config` and look for `Slack webhook configured`, then restart Beacon. When the whole `escalation` section is omitted, Slack is the default if neither Feishu route nor the signed notification webhook is configured. A configured Slack route can also be used with separate [phone escalation](../reference/config.md#escalation-and-acknowledgements).

In an HA deployment, upgrade both centres before adding the Slack settings. If rolling back to an earlier release, restore its configuration before running its installer.

## Verify delivery and handover

Run a [drill](../guide/drills.md) and check the firing and recovery messages in your test channel. Open the alert from the message, sign in with an on-call account and have a second responder confirm they can see the handover on the same incident. The **Critical notifications** panel and incident timeline record Slack's acceptance or the last failure. Verify delivery to the actual recipients in Slack.

Beacon queues Slack messages on disk and retries failed submissions. For the same incident and channel, firing is submitted before recovery, including after a restart. Retries start after one minute or the longer interval requested by Slack. Pending messages expire after seven days. Slack incoming webhooks have no delivery ID for deduplication, so Slack may show a duplicate if it already accepted a message whose response Beacon missed. Both copies link to the same incident.
