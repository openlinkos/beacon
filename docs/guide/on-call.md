# Put an agent on call

With a hook in place, a service that breaks wakes the agent you choose. The agent then:

- reads the evidence in one call;
- works the machines through the SSH channel;
- has every probe location check its fix;
- leaves a note that people read on the card and in the console.

Beacon calls no model and asks nobody for approval. The harness that runs the agent decides what it does, and everything the agent does is on record.

You need:

- Beacon running ([On a server](../self-hosting/install.md), steps 1 to 4).
- An agent connected over MCP: [Claude Code](../integrations/claude-code.md), [Codex](../integrations/codex.md) or [a token](../integrations/headless.md).
- An address the hook can reach: a server running `beacon-oncall`, a receiver of your own, or a GitHub workflow.

## Try the loop by hand

Before wiring the hook, walk the same loop yourself, in the hook's place:

- [Rehearse an outage](drills.md) starts a drill and says when `ServiceDown` fires.
- [Walk the loop](drills.md#walk-the-loop) has the question to ask your agent, and what it does with it.

A woken agent works the same way. The hook only takes the place of the person who asks.

## Wake an agent {#wake-an-agent}

1. **List the hook in the configuration.** Add the hook under `agents` in `config.yaml`. Keep your deployment's existing `public_url` so the hook can find the evidence.

   ```yaml
   agents:
     - name: claude-oncall
       kind: webhook
       url: https://agents.example.com/hooks/beacon
       secret_file: /etc/beacon/secrets/agent-hook-secret   # at least 24 random characters
       service: '^drill$'
       resolved: true                            # also say when the alert resolves
   ```

   `severities` says which alert groups wake it (`[critical]` by default). An optional `service` regular expression narrows them further. A hook of `kind: github` dispatches a workflow instead. [GitHub Actions](../integrations/github-actions.md) has that block and what each field means.

2. **Check the configuration and restart.** On the reference deployment, run `sudo ./install.sh` from `/opt/beacon` to set the new secret file's permissions, validate and restart. On other installations, run `beacon check-config` with the deployment's configuration, then restart the service.

   `beacon check-config` refuses:

   - a hook without `public_url`;
   - a webhook without `secret_file` (or `headers_file`, for a receiver that checks a secret of its own);
   - a secret shorter than 24 characters.

3. **Stand up the other end.** A webhook needs something that runs the agent. `beacon-oncall` is the reference harness. It checks the signature and starts the program you name, with the payload on its standard input ([Headless agents and CI](../integrations/headless.md)). The headers and the payload are in [Agents woken by a webhook](../integrations/webhooks.md).

4. **Check the routes, then run the drill.** Use the [private receiver setup](#a-private-receiver-on-the-centre) when the harness runs on the Linux centre, then [check your routes](#check-the-routes-before-the-drill). Start a drill and open its incident in the console. Look for the hook acceptance in the timeline:

   ```
   Alert delivered to hook claude-oncall at 10:02
   ```

   Each call's outcome is also in the audit log (`wake_agent`), in the alert chat and on the console's **Hooks** page.

   Delivery means the hook returned a successful HTTP response. For example, the reference receiver can return 202 as soon as it queues the alert. Set up [execution reports](../integrations/headless.md#report-programme-execution) to see programme starts and exits on the alert page. Use probe results and rule status to verify recovery.

### A private receiver on the centre {#a-private-receiver-on-the-centre}

On the reference Linux deployment, `beacon` runs on its Docker bridge. For a first drill with `beacon-oncall` on the same host, use the bridge gateway as the private address. A request to the stock public `/hooks/beacon` on `status.example.com` returns HTTP 404.

From `/opt/beacon`, find the gateway address and its interface. Apply an inbound firewall rule for TCP 17007 that drops traffic to that address from outside the bridge. The commands below use `iptables`. If you use a firewall manager, put the equivalent rule before broad accept rules.

```sh
beacon_gateway=$(docker network inspect beacon --format '{{(index .IPAM.Config 0).Gateway}}')
beacon_bridge=$(ip -o -4 address show | awk -v gateway="$beacon_gateway" '{split($4, address, "/"); if (address[1] == gateway) print $2}')
test -n "$beacon_bridge" && {
  sudo iptables -C INPUT ! -i "$beacon_bridge" -d "$beacon_gateway/32" -p tcp --dport 17007 -j DROP ||
    sudo iptables -I INPUT 1 ! -i "$beacon_bridge" -d "$beacon_gateway/32" -p tcp --dport 17007 -j DROP
} && printf 'http://%s:17007/beacon\n' "$beacon_gateway"
```

Use the printed URL in the hook's `url` field. Set `service: '^drill$'` for the first test, and run `sudo ./install.sh` to apply the centre configuration. Keep the firewall rule in place while the harness listens there.

After [installing the harness, wrapper and agent account](../integrations/headless.md#as-a-service), leave the systemd unit stopped for the first foreground run. In a separate terminal, set `beacon_gateway` to the address you found above and run the following. This example uses the Claude Code wrapper and the same signing secret on both sides.

```sh
sudo -u beacon-oncall -H env \
  PATH=/var/lib/beacon-oncall/.local/bin:/usr/local/bin:/usr/bin:/bin \
  BEACON_TOKEN_FILE=/etc/beacon-oncall/beacon-token \
  /usr/local/bin/beacon-oncall -listen "$beacon_gateway:17007" \
  -secret-file /etc/beacon-oncall/hook-secret \
  -run /usr/local/lib/beacon-oncall/claude-code.sh \
  -log-dir /var/lib/beacon-oncall/logs
```

Back in `/opt/beacon`, check health from the same Docker network as Beacon. You should see `ok`.

```sh
docker compose exec -T caddy wget -q -O - "http://$beacon_gateway:17007/healthz"
```

For a notification receiver on the same host, use the same bridge address and an ingress rule for its own port. Use the receiver's health path when it has one. For receivers on a different machine, use the private network shared by the two hosts or an HTTPS endpoint that Beacon can reach.

### Check the routes before the drill {#check-the-routes-before-the-drill}

- **Configuration.** `beacon check-config` validates settings and secret files. **Overview → Critical notifications** shows the selected route. Both can be correct before either receiver has heard from Beacon.
- **Reachability.** Check the harness and any receiver health endpoint from the centre's network. If the network check fails, fix the bind address or firewall before starting the drill.
- **Delivery.** During the drill, check the timeline and receiver together. HTTP 401 from a signed receiver calls for checking the shared secret and the signature on the request. HTTP 202 from `beacon-oncall` confirms the queue accepted the wake. Check the programme's result separately. For a chat or phone notification, check that it actually appears in the intended test channel or device.

### When a hook is called

- **Once per alert group**, when a new group is first notified. It is not called again while the group keeps firing.
- **Once more when the group resolves**, if `resolved` is set: `resolved_after` (a minute by default) after it resolves.
- **Again after a failure.** A call that gets no answer, a timeout, a 429 or a 5xx is tried again after 5 seconds, 20 seconds, 1 minute and 3 minutes.

Delivery of the alert itself never waits for the hook.

## Take over an alert

Read the alert page's response panel for the hook and notification results. A hook can accept an alert even if the Agent cannot start or has left no work to review. The configured human escalation continues while nobody has acknowledged the alert as a person. An acknowledgement by an identified Agent leaves that escalation in effect. An explicit silence still pauses it.

An earlier Pushover emergency can repeat while cancellation is pending. The response panel shows when Pushover confirms cancellation or when the retry window expires. Beacon requests cancellation for the original [emergency receipt](https://pushover.net/api/receipts). A separate emergency on a later incident has its own receipt. An older receipt with an unverified provider route appears as unconfirmed.

As the person on call, open the alert page and select **Acknowledge**. Use **Take over** if a previous shift or an Agent has already acknowledged it. Add a brief note about the next action. The timeline keeps up to 20 previous acknowledgements and their notes, and the audit log records each acknowledgement. Check the probes and the alert state to verify recovery.

Use a drill to [verify real phone delivery and a two-person handover](drills.md#verify-phone-delivery-and-handover) before putting the routes on call.

## The evidence {#the-evidence}

One call gathers what an agent needs to judge an alert: `GET /api/v1/alerts/{id}/evidence`, or the MCP tool `get_evidence` (scope `status:read`).

Here is one, trimmed. Two locations cannot connect to `git.example.com`. A deployment went out four minutes before. Just before that, a deploy script's reload on the service's machine failed.

```jsonc
{
  "alert": {"id": "0cdfbada62dd", "status": "firing", "severity": "critical", "title": "git.example.com is down",
            "service": "git.example.com", "labels": {"alertname": "ServiceDown", "failure_domain": "origin-1"}},
  "window": {"from": "2026-11-02T01:28:00Z", "to": "2026-11-02T02:03:10Z"},
  "probes": {"services": ["git.example.com"], "locations": 2, "failing": ["edge-1", "edge-2"],
             "results": [{"location": "edge-1", "instance": "https://git.example.com/", "module": "http_head",
                          "ok": false, "failing_since": "2026-11-02T01:54:30Z", "reason": "connect"}]},
  "related": [{"id": "7a01c2e9f3b4", "title": "registry.example.com is down", "why": "failure_domain=origin-1"}],
  "changes": [{"time": "2026-11-02T01:53:40Z", "kind": "deploy", "service": "git.example.com", "summary": "git v2.4.1"}],
  "done": {"machines": ["origin-1"],
           "sessions": [{"id": "20261102-015312-3fa9c1", "machine": "origin-1", "command": "sudo systemctl reload nginx",
                         "exit": 1, "phase": "before"}]},
  "links": {"runbook": "https://github.com/example/ops/blob/main/runbooks/ServiceDown.md"}
}
```

- **probes** is one result per location for the group's service: whether it works, since when it fails, and why (`dns`, `connect`, `timeout`, `tls`, …). For a heartbeat's alert it adds the heartbeat's entry and the machine to reach ([Heartbeats](heartbeats.md)).
- **related** are the other groups firing in the same failure domain.
- **changes** are the [change events](#change-events) in the window.
- **done** is what people and agents have already done, the sessions on the alert's machines included. Each is marked `before`, `during` or `after` the alert.
- **The window** opens 30 minutes before the group started (`?before=2h`, at most 24 hours), so the session that caused the alert is in the answer.

The same story in time order is `GET /api/v1/alerts/{id}/timeline`, or `get_timeline`. It gives the events of the alert, the hooks, the changes, the sessions and the notes, one line each. The console's alert page shows the timeline and the evidence side by side.

How Beacon cleans the outside text these answers carry is in the security model ([Text from outside Beacon](../self-hosting/security.md#text-from-outside-beacon)).

## Probe again, now {#probe-again-now}

After a fix, the agent need not wait for the next cycle. `POST /api/v1/probe-now`, or the MCP tool `probe_now` (scope `alerts:write`), has every location that probes a target probe it again at once. It returns each answer:

```bash
curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"alert_id": "0cdfbada62dd", "wait": 45}' https://status.example.com/api/v1/probe-now
```

You will see one result per target and location, failing first, and a `summary` such as `7 OK, 2 failing (dns)`. `missing` names locations with at least one expected result missing. Use **Probe again now** on the alert page for the same check. Checks made with an alert ID appear in its timeline and audit log. Beacon uses the alert members’ instance, module, service and failure-domain labels when available. If any member with a known scope has no eligible configured targets, Beacon reports that the check cannot run for the full incident. The probes push the same samples as in their cycle, so the alert resolves when the rule engine finds its recovery conditions met.

The limits:

- A target is probed on demand at most once every 15 seconds.
- A token keeps three requests in flight.
- One request covers at most 20 targets and waits at most 90 seconds.

When `probe_now` names locations as `missing`, see [probe_now names locations as missing](../self-hosting/troubleshooting.md#probe_now-names-locations-as-missing).

## Notes

A note tells everyone what was wrong, what was done and what remains. Send it with `POST /api/v1/alerts/{id}/notes` and `{"text": "…"}`, or with the MCP tool `add_note` (scope `alerts:write`).

- The card shows the latest three.
- The chat hears each one.
- The audit log keeps them all. A note is at most 2000 characters, and a group keeps its latest 100.

The reference Agent uses notes and leaves acknowledgement and silencing to the person on call. An identified Agent's `ack_alert` call records its response, and a person's acknowledgement pauses escalation for `ack_ttl`. Standalone credentials and stored acknowledgements of unknown origin keep their earlier behaviour and pause escalation. A note that begins with `public:` also appears as an incident update on the [status pages](status-pages.md).

## Change events {#change-events}

Deployments and Flux reconciliations that reach Beacon as change events put "what changed just before" into the evidence. [Deployments and Flux](../integrations/change-events.md) says how to send them.

## Tokens that renew themselves {#tokens-that-renew-themselves}

A headless agent's token renews itself before it expires, so nobody has to hand it a new one. See [Headless agents and CI](../integrations/headless.md#tokens-that-renew-themselves).

## Check it

1. **Ask the agent** *"what did you do with the last alert?"* after the drill in step 4.
2. **Read `get_timeline` next to the console's alert page.** The hook's call, the session on the machine, the `probe_now` result and the note should all be there.
3. **Open the Hooks page** (`/console/hooks`). Every call should have status 202 and no retries.

When it does not work:

- [The agent is not woken](../self-hosting/troubleshooting.md#the-agent-is-not-woken): a hook that never fires.
- [An alert never arrives](../self-hosting/troubleshooting.md#an-alert-never-arrives): an alert that never fires at all.

## Next

- [Give agents a machine](machines.md): the SSH channel the evidence sends the agent to.
- [Rehearse an outage](drills.md): the same loop, exercised on purpose.
- [Review what agents did](console.md): the timeline, the sessions and the replay, for people.
