# Put an agent on call

With a hook in place, a service that breaks wakes the agent you choose: it reads the evidence in one call, works the machines through the SSH channel, has every probe location check its fix, and leaves a note people read on the card and in the console. Beacon calls no model and asks nobody for approval; the harness that runs the agent decides what it does, and everything the agent does is on record.

You need Beacon running ([Up in 30 minutes](../self-hosting/install.md), steps 1 to 4), an agent connected over MCP ([Claude Code](../integrations/claude-code.md), [Codex](../integrations/codex.md) or [a token](../integrations/headless.md)), and an address the hook can reach: a server running `beacon-oncall`, a receiver of your own, or a GitHub workflow.

## Try the loop by hand

Before wiring the hook, walk the same loop with you in the hook's place. Start a drill and wait about five minutes for `ServiceDown`:

```bash
docker exec beacon beacon drill fail 15m
```

Then ask your agent: *"Is anything failing right now? Look at the evidence, check the machine, and tell me what you would do."* It lists the alerts, reads the evidence and answers from what the probes see. A woken agent works the same way; the hook only takes the place of the person who asks. `beacon drill off` ends the drill.

## Wake an agent {#wake-an-agent}

1. **List the hook in the configuration.** Hooks are read from `config.yaml`:

   ```yaml
   public_url: https://status.example.com     # the hooks tell agents where the evidence is

   agents:
     - name: claude-oncall
       kind: webhook
       url: https://agents.example.com/hooks/beacon
       secret_file: /etc/beacon/secrets/agent-hook-secret   # at least 24 random characters
       severities: [critical, warning]
       resolved: true                            # also say when the alert resolves
   ```

   `severities` says which alert groups wake it (`[critical]` by default), and an optional `service` regular expression narrows them further. A hook of `kind: github` dispatches a workflow instead: [GitHub Actions](../integrations/github-actions.md) has that block and what each field means.

2. **Check the configuration and restart.** `beacon check-config` refuses a hook without `public_url`, a webhook without `secret_file` (or `headers_file`, for a receiver that checks a secret of its own) and a secret shorter than 24 characters:

   ```bash
   docker exec beacon beacon check-config -config /etc/beacon/config.yaml && docker compose restart beacon
   ```

3. **Stand up the other end.** A webhook needs something that runs the agent. `beacon-oncall` is the reference harness: it checks the signature and starts the program you name, with the payload on its standard input ([Headless agents and CI](../integrations/headless.md)); the headers and the payload are in [Agents woken by a webhook](../integrations/webhooks.md).

4. **Break something and watch it fire.** Start another drill, wait for the card, and look for the line about the agent on it. Each call's outcome is also in the audit log (`wake_agent`), in the alert chat, and on the console's **Hooks** page:

   ```
   Agent claude-oncall woken at 10:02
   ```

A hook is called once, when a new alert group is first notified, and not again while it keeps firing; with `resolved` set, once more `resolved_after` (a minute by default) after the group resolves. A call that gets no answer, a timeout, a 429 or a 5xx is tried again after 5 seconds, 20 seconds, 1 minute and 3 minutes; delivery of the alert itself never waits for it.

## The evidence {#the-evidence}

One call gathers what an agent needs to judge an alert: `GET /api/v1/alerts/{id}/evidence`, or the MCP tool `get_evidence` (scope `status:read`). Here is one, trimmed: two locations cannot connect to `git.example.com`, a deployment went out four minutes before, and a deploy script's reload on the service's machine failed just before that.

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
- **related** are the other groups firing in the same failure domain; **changes** are the [change events](#change-events) in the window; **done** is what people and agents have already done, the sessions on the alert's machines included, each marked `before`, `during` or `after` the alert.
- **The window** opens 30 minutes before the group started (`?before=2h`, at most 24 hours), so the session that caused the alert is in the answer.

The same story in time order is `GET /api/v1/alerts/{id}/timeline`, or `get_timeline`: the alert's, the hooks', the changes', the sessions' and the notes' events on one line each. The console's alert page shows it with the evidence side by side. How Beacon cleans the outside text these answers carry is in the security page ([Untrusted text](../self-hosting/security.md)).

## Probe again, now {#probe-again-now}

After a fix, the agent need not wait for the next cycle: `POST /api/v1/probe-now`, or the MCP tool `probe_now` (scope `alerts:write`), has every location that probes a target probe it again at once and returns each answer:

```bash
curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"alert_id": "0cdfbada62dd", "wait": 45}' https://status.example.com/api/v1/probe-now
```

You will see one line per location, failing first, and a `summary` such as `7 OK, 2 failing (dns)`; `missing` names the locations that did not answer in time. The probes push the same samples as in their cycle, so the alert resolves at the rule engine's next evaluation. A target is probed on demand at most once every 15 seconds and a token keeps three requests in flight; one request covers at most 20 targets and waits at most 90 seconds. When `probe_now` names locations as `missing`, see [probe_now names locations as missing](../self-hosting/troubleshooting.md#probe-now-names-locations-as-missing).

## Notes

A note tells everyone what was wrong, what was done and what remains: `POST /api/v1/alerts/{id}/notes` with `{"text": "…"}`, or the MCP tool `add_note` (scope `alerts:write`). The card shows the latest three, the chat hears each one, and the audit log keeps them all (at most 2000 characters each, the latest 100 per group). A note does not acknowledge the alert; `ack_alert` does, and only people should. One that begins with `public:` also appears as an incident update on the [status pages](status-pages.md).

## Change events {#change-events}

Deployments and Flux reconciliations that reach Beacon as change events put "what changed just before" into the evidence. How to send them: [Deployments and Flux](../integrations/change-events.md).

## Tokens that renew themselves {#tokens-that-renew-themselves}

A headless agent's token renews itself before it expires, so nobody hands it a new one: [Headless agents and CI](../integrations/headless.md#tokens-that-renew-themselves).

## Check it

After the drill in step 4, ask the agent *"what did you do with the last alert?"* and read `get_timeline` next to the console's alert page: the hook's call, the session on the machine, the `probe_now` result and the note should all be there. Then check the **Hooks** page (`/console/hooks`): every call with status 202 and no retries.

When it does not work: [The agent is not woken](../self-hosting/troubleshooting.md#the-agent-is-not-woken) reads a hook that never fires, and [An alert never arrives](../self-hosting/troubleshooting.md#an-alert-never-arrives) one that never fires at all.

## Next

- [Give agents a machine](machines.md): the SSH channel the evidence sends the agent to.
- [Rehearse an outage](drills.md): the same loop, exercised on purpose.
- [Review what agents did](console.md): the timeline, the sessions and the replay, for people.
