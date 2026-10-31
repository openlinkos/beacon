# Setting up the Feishu app

English | [简体中文](zh/feishu.md)

Beacon sends alert cards through a Feishu (or Lark) **self-built app** and receives button clicks from it. A group's custom bot cannot do this: its messages have no callbacks and cannot be updated. The custom bot is still useful alongside the app, for plain messages that cost no API quota.

Create two apps, one for production and one for testing (for example "Beacon" and "Beacon Dev"): Feishu hands each callback to one connection only, so a test instance using the production app would take clicks away from production.

## 1. Create the app

1. In the [Feishu open platform](https://open.feishu.cn/app) (or [Lark's](https://open.larksuite.com/app)), create a **custom app** for your organisation.
2. Under **Add features**, add **Bot**.
3. Under **Credentials & basic info**, note the **App ID** and **App Secret**. Put them in the files that `feishu.app_id_file` and `feishu.app_secret_file` name.

## 2. Permissions

Under **Permissions & scopes**, enable:

| Permission | For |
| --- | --- |
| Send messages as an app (`im:message:send_as_bot`) | Sending cards to the alert chat |
| Read and update messages sent by the app (`im:message` or `im:message:update`) | Updating a card in place |
| Send urgent messages in app (`im:message.urgent`) | Buzzing the people on call |

If the console names them differently, look for the permissions behind the APIs "send message", "update app message card" and "buzz a message in app".

## 3. Callbacks

Under **Events & callbacks → Callback configuration**:

- **Long connection** (recommended; `feishu.callback: websocket`): choose "Receive callbacks through a persistent connection". The service connects out to Feishu, so no public URL is needed. Then add the callback **Card action** (`card.action.trigger`).
- **HTTP** (`feishu.callback: http`): set the request URL to `https://<your host>/hooks/feishu`, and put the **Verification Token** (and the **Encrypt Key**, if you turned encryption on) in the files `feishu.verification_token_file` and `feishu.encrypt_key_file` name. Then add **Card action**.

Make sure you add `card.action.trigger` on the **callback** tab, not the event tab: the old card callback does not travel over the long connection.

## 4. Publish

Create a version and publish it; an administrator approves it in the admin console. Permission changes take effect only in a published version.

## 5. The alert chat

1. Create a group (for example "LinkOS Alerts") and add the people on call.
2. Add the app's bot to the group (group settings → Bots → Add bot).
3. Find the group's `chat_id` (`oc_…`), for example with the API explorer's "get chat list" as the app, and set `feishu.chat_id`.
4. Find each on-call person's `open_id` (`ou_…`) for this app and list them under `feishu.on_call` with the name shown on cards. Only they can act on cards, and they are the ones buzzed.
5. Add a **custom bot** to the same group. Under its security settings, choose a keyword (set `feishu.webhook_keyword`, `LinkOS` by default) and/or a signature (put the secret in `feishu.webhook_secret_file`). Put its webhook URL in `feishu.webhook_url_file`.

## 6. Check it

Start the service and look for `feishu long connection ready` in the log, then run a drill: `linkos-beacon drill fail 15m` makes `/drill/healthz` fail; once your probes see it and the alert fires, a card arrives. Acknowledge it, silence it, then `linkos-beacon drill off` and watch it turn green.

## Quota

On the free plan, all of an organisation's custom apps share a monthly API call limit (10,000 calls when this was written; check your console). Sending a card, updating it through the API and buzzing each cost one call; a card updated by a click costs nothing. Set `feishu.monthly_budget` to your limit: above 70% of it only critical alerts use the app, and the rest go to the custom bot.

Phone and SMS buzzes need a verified organisation; Beacon uses only the in-app buzz and escalates to Pushover instead.
