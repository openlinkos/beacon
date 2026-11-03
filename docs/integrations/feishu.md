# Set up the Feishu app

At the end of this page, alerts arrive as cards in a Feishu (or Lark) group. The configuration:

```yaml
feishu:
  app_id_file: /etc/beacon/secrets/feishu-app-id
  app_secret_file: /etc/beacon/secrets/feishu-app-secret
  domain: feishu                       # or lark
  callback: websocket                  # or http, with verification_token_file (and encrypt_key_file)
  chat_id: oc_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
  on_call:                             # only they may act on cards; they are buzzed
    - {open_id: ou_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx, name: Alex}   # the name is shown on cards
  monthly_budget: 10000                # the app's API calls per month (see Quota)
  webhook_url_file: /etc/beacon/secrets/feishu-webhook-url        # the custom bot
  webhook_keyword: Beacon
```

A card says what is wrong, since when, which locations fail and why, and what agents have noted, and it changes in place as the alert does, turning green when it resolves. The people on call acknowledge and silence it with its buttons; a critical alert nobody acknowledges buzzes them in the app after 10 minutes. A custom bot in the same group carries plain messages that cost no API quota. You need to create a self-built app in your organisation, which an administrator approves; the steps take a quarter of an hour, the approval as long as your organisation takes.

Beacon sends cards through a **self-built app** and receives its button clicks. A group's custom bot alone cannot do this: its messages have no callbacks and cannot be updated. It is still useful beside the app, for plain messages: alerts of severity `info`, alerts once the app's monthly budget runs low, and the echoes of what people and agents do.

Create two apps, one for production and one for testing (for example "Beacon" and "Beacon Dev"). Feishu hands each callback to one connection only, so a test instance using the production app would take clicks away from production.

## 1. Create the app

1. In the [Feishu open platform](https://open.feishu.cn/app) (or [Lark's](https://open.larksuite.com/app), and then `feishu.domain: lark`), create a **custom app** for your organisation.
2. Under **Add features**, add **Bot**.
3. Under **Credentials & basic info**, note the **App ID** and **App Secret**. They go into the files that `feishu.app_id_file` and `feishu.app_secret_file` name (step 5).

## 2. Permissions

Under **Permissions & scopes**, enable:

| Permission | For |
| --- | --- |
| Send messages as an app (`im:message:send_as_bot`) | Sending cards to the alert chat |
| Read and update messages sent by the app (`im:message` or `im:message:update`) | Updating a card in place |
| Send urgent messages in app (`im:message.urgent`) | Buzzing the people on call |

If the console names them differently, look for the permissions behind the APIs "send message", "update app message card" and "buzz a message in app".

## 3. Callbacks

Under **Events & callbacks → Callback configuration**, choose how the clicks reach Beacon:

- **Long connection** (recommended; `feishu.callback: websocket`, the default): choose "Receive callbacks through a persistent connection". Beacon connects out to Feishu, so it needs no public URL. Then add the callback **Card action** (`card.action.trigger`).
- **HTTP** (`feishu.callback: http`): set the request URL to `https://<your host>/hooks/feishu`, and put the **Verification Token** (and the **Encrypt Key**, if you turned encryption on) in the files that `feishu.verification_token_file` and `feishu.encrypt_key_file` name. Then add **Card action**.

Add `card.action.trigger` on the **callback** tab, not the event tab: the old card callback does not travel over the long connection.

## 4. Publish

Create a version and publish it; an administrator approves it in the admin console. Permission changes take effect only in a published version, so publish again after changing them.

## 5. The alert chat

1. Create a group (for example "Alerts") and add the people on call.
2. Add the app's bot to the group (group settings → Bots → Add bot).
3. Find the group's `chat_id` (`oc_…`), for example with the API explorer's "get chat list", called as the app.
4. Find each on-call person's `open_id` (`ou_…`) for this app. Only these people can act on cards, and they are the ones buzzed.
5. Add a **custom bot** to the same group. Under its security settings, choose a keyword (`Beacon` unless you set `feishu.webhook_keyword`) and/or a signature (its secret goes in the file `feishu.webhook_secret_file` names). Copy its webhook URL.

Put each secret in its file (in the reference deployment, under `/opt/beacon/secrets/`, which `install.sh` gives owner 65532 and mode 0400), and the rest in Beacon's configuration: the block at the top of this page, with the `chat_id` from step 3 and the `open_id`s from step 4.

`beacon check-config` refuses an app without `chat_id` or without at least one person in `on_call`, an `open_id` that does not start with `ou_`, and an HTTP callback without its verification token.

## 6. Check it

Restart Beacon so that it reads the new configuration (`docker compose restart beacon` in `/opt/beacon` for the reference deployment), then check, in this order:

```bash
docker exec beacon beacon check-config               # configuration OK: Feishu app true, …
docker logs beacon 2>&1 | grep 'feishu long connection'
curl -s http://127.0.0.1:17001/readyz                # on the centre: {"not_ready":null,"ready":true}
docker exec beacon beacon drill fail 15m
```

- The log has `feishu long connection ready` within seconds of the start (with `callback: http` there is no such line). If it is missing, or says `feishu long connection lost` or `error`: check the App ID and App Secret in their files, that the app's version with the bot and the permissions is published and approved, and that the callback is set to the persistent connection.
- `/readyz` answers 503 with the reasons when alerts cannot reach people: "the Feishu long connection is down", "a Feishu API call failed in the last 10 minutes", "the Feishu API budget for this month is used up", or that the rule engine's Watchdog has not arrived. `GET /api/v1/bridge` (MCP `status_summary`) says the same to a token with `status:read`, with the calls used this month.
- The drill makes `/drill/healthz` fail. Once your probe locations see it and the alert fires (4 to 5 minutes with two locations), a card arrives in the group. Acknowledge it, silence it, then run `docker exec beacon beacon drill off` and watch it turn green.
- A click that answers "Only the people on call can act on alerts" came from someone whose `open_id` is not in `feishu.on_call`, or whose `open_id` was taken from another app: an `open_id` is per app. Clicks Beacon never hears of (the card stays as it was, and the audit log has no entry with via `feishu`) are going elsewhere: to a test instance running with the production app, or nowhere, when `card.action.trigger` was added on the event tab instead of the callback tab.

## Quota

On the free plan, all of an organisation's custom apps share a monthly API call limit (10,000 calls when this was written; check your console). Sending a card, updating it through the API and buzzing each cost one call; a card updated by a click costs nothing. A note left through the API or MCP, and an agent woken for the alert, each update the card through the API, until 90% of the budget is used. Set `feishu.monthly_budget` to your limit (the month is counted in the configuration's `timezone`):

- above 70% of it, only critical alerts use the app, and the rest go to the custom bot;
- from 90%, cards are no longer updated through the API, except to show that an alert resolved;
- at 100%, nothing goes through the app until the month ends, and `/readyz` says so.

Phone and SMS buzzes need a verified organisation; Beacon uses only the in-app buzz and escalates to Pushover instead. By default (`escalation.critical`), a critical alert nobody has acknowledged is buzzed after 10 minutes and, once Pushover is configured, sent at Pushover's emergency priority after 15.
