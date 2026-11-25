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
  # webhook_url_file: /etc/beacon/secrets/feishu-webhook-url      # optional custom bot
  # webhook_keyword: Beacon
```

A card says what is wrong and since when, which locations fail and why, and what agents have noted. It changes in place as the alert does, and turns green when it resolves.

Beacon retries a recovery update for up to seven days against the destination that accepted the firing alert. For an app card, the update turns the original card green and counts against the app budget. For a custom bot, the update is a separate message in the original group. If the original destination cannot be verified, Beacon keeps legacy direct card updates by message ID. A bot recovery needs a verified destination. The recovery link opens the incident after sign-in. The incident timeline distinguishes submission failure from acceptance by Feishu.

- The people on call acknowledge and silence it with its buttons.
- A critical alert nobody acknowledges buzzes them in the app after 10 minutes.
- A custom bot in the same group carries plain messages that cost no API quota.

You need a Beacon centre and two reporting probe locations for the drill. In your Feishu or Lark organisation, you need permission to create a self-built app and add it to a test group, and an administrator who can approve app releases. Allow for your organisation’s approval time.

Beacon sends cards through a **self-built app** and receives its button clicks. A group's custom bot alone cannot do this: its messages have no callbacks and cannot be updated. The bot is still useful beside the app, for plain messages:

- alerts of severity `info`;
- alerts once the app's monthly budget runs low;
- the echoes of what people and agents do.

Use a separate app and group for testing when you already have a production app. Feishu delivers each callback to one connection, so a test instance using the production app can receive production card actions.

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
| Obtain group information (`im:chat:readonly`) | Looking up the test group and its members during setup |

If the console names them differently, look for the permissions behind the APIs "send message", "update app message card" and "buzz a message in app".

## 3. Publish the app {#3-publish}

Publish a version with the Bot feature and those permissions. Include the test group’s members in the app’s availability scope. Wait for approval before adding the app to the group. You will publish the callback subscription after Beacon is running.

## 4. Prepare the test group {#4-prepare-the-test-group}

1. Create a group in your organisation, such as “Beacon test alerts”, and add the people who will acknowledge test alerts.
2. In the group’s settings, open **Bots → Add bot** and choose the app you published.
3. In your platform’s API explorer, using the **same test app’s** `tenant_access_token`, call [Obtain groups where the user or bot is a member](https://open.feishu.cn/document/server-docs/group/chat/list) (`GET /im/v1/chats`). Copy the test group’s `chat_id` (`oc_…`).
4. With the same app selected, call [Obtain group member list](https://open.feishu.cn/document/server-docs/group/chat-member/get) (`GET /im/v1/chats/{chat_id}/members`). Set `member_id_type` to `open_id`. Copy each test responder’s `member_id` (`ou_…`) into `feishu.on_call`. Feishu issues these IDs separately for each app. Beacon’s console logins are managed separately under **People**.
5. To exercise plain-text delivery and fallback, add a **custom bot** to the same test group. Set its security keyword to `Beacon` or match it in `feishu.webhook_keyword`. Save its webhook URL in the file named by `feishu.webhook_url_file`. If you enable signing, save its signing secret in `feishu.webhook_secret_file` as well. A first test of app cards can leave these bot settings commented out.

## 5. Start Beacon and subscribe to callbacks {#5-configure-callbacks}

Put the app ID and app secret in the two files named by `app_id_file` and `app_secret_file`. For the reference deployment, these files live under `/opt/beacon/secrets/`. Update the `feishu:` block in `/opt/beacon/config.yaml` with the test group ID and at least one responder’s `open_id`. Use `domain: lark` for Lark.

Choose the callback mode in Beacon’s configuration before loading it:

- **Long connection (recommended).** Use `callback: websocket`. Allow outbound access to the Feishu or Lark platform.
- **HTTP.** Use `callback: http`. Write the app’s **Verification Token** to the file named by `verification_token_file`. If the app encrypts callbacks, write its **Encrypt Key** to the file named by `encrypt_key_file`. Set the corresponding file paths under `feishu:`. Feishu must be able to reach Beacon at `https://status.example.com/hooks/feishu` using your public host name.

In the reference deployment, apply the change as follows. `install.sh` sets the secret files to owner 65532 and mode 0400, validates the configuration, and restarts Beacon when its configuration or secrets have changed.

```bash
cd /opt/beacon
sudo ./install.sh
docker exec beacon beacon check-config -config /etc/beacon/config.yaml
```

The configuration check should say `Feishu app true`. Then go to **Events & callbacks → Callback configuration** in the developer console:

1. **For a long connection**, wait for `feishu long connection ready` in `docker compose logs beacon`. Then select **Receive callbacks through a persistent connection** and save. Feishu requires the client to be online before it saves the setting. [Feishu’s persistent connection guide](https://open.feishu.cn/document/event-subscription-guide/callback-subscription/step-1-choose-a-subscription-mode/configure-callback-request-address)
2. **For HTTP**, select the developer-server callback mode and save the HTTPS callback URL you configured above. Beacon must be running to answer Feishu’s verification request.
3. Add **Card action** (`card.action.trigger`) in **Callback configuration**. Publish the new version and wait for approval before testing card actions. [Feishu’s callback subscription guide](https://open.feishu.cn/document/event-subscription-guide/callback-subscription/callback-overview)

## 6. Check it {#6-check-it}

On the centre, check readiness.

```bash
curl -sS -i http://127.0.0.1:17001/readyz
```

When readiness returns HTTP 200 and both locations are reporting, start the drill.

```bash
docker exec beacon beacon drill fail 15m
```

1. In the **test group**, wait for the critical `ServiceDown` card for `drill`. Allow approximately five minutes from starting the drill. Open **Timeline**, sign in to the Beacon console, and confirm it shows the same alert.
2. From a Feishu account listed in `feishu.on_call`, use **Acknowledge with note** on the card. Confirm the note and the responder appear on the same incident’s timeline.
3. Run `docker exec beacon beacon drill off`. Confirm the probes recover and the same card turns green. For scheduled phone delivery or group bot fallback, follow the [separate notification drill](../guide/drills.md#verify-phone-delivery-and-handover).

If something does not work:

- **The long connection never gets ready.** Check the App ID, App Secret, domain and outbound access. With HTTP callbacks there is no long-connection log.
- **`/readyz` returns 503.** Read its `not_ready` reasons. They can include the long connection, a recent Feishu API failure, monthly quota, or a missing rule-engine Watchdog. A token with `status:read` can also read the reasons and API usage in `GET /api/v1/bridge` or MCP `status_summary`. The card’s receipt and acknowledgement verify delivery in your group.
- **A card arrives but the acknowledgement fails.** Check that `card.action.trigger` is in the published callback subscription and that the responder’s `ou_…` came from this app’s member lookup. If another instance uses the same app, it can receive the click instead.

## Quota

On the free plan, all of an organisation's custom apps share a monthly API call limit. It was 10,000 calls when this was written; check your console.

Sending a card, updating it through the API and buzzing each cost one call. A card updated by a click costs nothing. A note left through the API or MCP, and an agent woken for the alert, each update the card through the API, until 90% of the budget is used.

Set `feishu.monthly_budget` to your limit. The month is counted in the configuration's `timezone`. As the budget is used:

- above 70% of it, only critical alerts use the app, and the rest go to the custom bot;
- from 90%, cards are no longer updated through the API, except to show that an alert resolved;
- at 100%, nothing goes through the app until the month ends, and `/readyz` says so.

Phone and SMS buzzes need a verified organisation. Beacon uses only the in-app buzz, and escalates to Pushover instead. By default (`escalation.critical`), a critical alert nobody has acknowledged is buzzed after 10 minutes. Once Pushover is configured, it is sent at Pushover's emergency priority after 15.
