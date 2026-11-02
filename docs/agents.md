# Agents and alerts

English | [简体中文](zh/agents.md)

When an alert fires, Beacon can wake an agent, hand it the evidence in one call, put the notes it leaves on the alert's card, and keep one timeline of everything that happened. Beacon calls no model and decides nothing for the agent: it wakes it, answers its questions and records what it does. Whatever runs the agent (a small server of your own, a GitHub workflow with Claude Code or Codex in it) is the agent's harness, not part of Beacon.

## Wake an agent

List the hooks in the configuration:

```yaml
public_url: https://status.example.com     # needed: hooks tell agents where the evidence is

agents:
  - name: claude-oncall
    kind: webhook
    url: https://agents.example.com/hooks/beacon
    secret_file: /etc/linkos-beacon/secrets/agent-hook-secret   # at least 24 random characters
    severities: [critical, warning]
    service: "hub-.*|git-.*"                  # optional
    resolved: true                            # also say when the alert resolves
  - name: ops-workflow
    kind: github
    repository: example/ops
    workflow: oncall.yml
    ref: main
    token_file: /etc/linkos-beacon/secrets/github-dispatch-token
```

| Field | Meaning |
| --- | --- |
| `name` | Lower-case letters, digits, dots, dashes and underscores; shown on the card and in the chat |
| `kind` | `webhook` (a signed POST) or `github` (a `workflow_dispatch`) |
| `severities` | Which alert groups wake it; `[critical]` by default |
| `service` | A regular expression over the whole `service` label; a group of several services matches when one does |
| `resolved` | Call again when a group it was woken for resolves |
| `url`, `secret_file` | Webhook: where to POST, and the secret that signs each call |
| `repository`, `workflow`, `ref`, `token_file` | GitHub: `owner/name`, the workflow's file name or id, the branch (`main` by default), and a token that may dispatch it (a fine-grained token with Actions: read and write on that repository) |
| `api` | GitHub: `https://api.github.com` by default, for GitHub Enterprise |

A hook is called once when a new alert group is first notified (when its card goes out), not again while it keeps firing, and once more when it resolves if `resolved` is set. A group that fires again after resolving is a new episode and wakes the hook again.

The calls run beside alert delivery: the card, the buzz and Pushover never wait for them. A call that gets no answer, a timeout, a 429 or a 5xx is tried again after 5 seconds, 20 seconds, 1 minute and 3 minutes; any other refusal (401, 404, 422) fails at once, since trying again would not help. Redirects are not followed, so the payload and its signature go only to the address configured. Calls a restart interrupted are taken up again when they are less than ten minutes old.

Each outcome is kept on the alert group (`agents` in `GET /api/v1/alerts/{id}` and `get_alert`), in the audit log (`wake_agent`), on the card ("Agent claude-oncall woken at 10:02", or that it could not be woken) and in the alert chat, with the reason when it failed.

### The webhook

Beacon POSTs JSON with these headers:

| Header | Value |
| --- | --- |
| `X-Beacon-Signature` | `sha256=` and the hex HMAC-SHA256 of the body under the secret |
| `X-Beacon-Event` | `firing` or `resolved` |
| `X-Beacon-Delivery` | The call's id, the same in every attempt |

```json
{
  "delivery": "9f3c2a1b7e4d5c60",
  "event": "firing",
  "at": "2026-11-02T02:00:31Z",
  "hook": "claude-oncall",
  "alert": {
    "id": "0cdfbada62dd", "status": "firing", "severity": "critical",
    "title": "git.example.com is down", "description": "No answer from 2 locations",
    "service": "git-example-com",
    "labels": {"alertname": "ServiceDown", "service": "git-example-com", "severity": "critical", "failure_domain": "site-a"},
    "alerts": 1, "starts_at": "2026-11-02T01:58:00Z"
  },
  "links": {
    "alert": "https://status.example.com/api/v1/alerts/0cdfbada62dd",
    "evidence": "https://status.example.com/api/v1/alerts/0cdfbada62dd/evidence",
    "timeline": "https://status.example.com/api/v1/alerts/0cdfbada62dd/timeline",
    "mcp": "https://status.example.com/mcp",
    "runbook": "https://github.com/example/ops/blob/main/runbooks/ServiceDown.md"
  },
  "hint": "Beacon woke you for this alert group. Get its evidence in one call: …",
  "untrusted": {"fields": ["alert.title", "alert.description", "alert.service", "alert.services", "alert.labels"], "note": "…"}
}
```

Check the signature against the raw body before reading it, and refuse a delivery whose `at` is more than ten minutes old or whose `delivery` you have seen:

```python
import hashlib, hmac

def genuine(secret: bytes, body: bytes, signature: str) -> bool:
    return hmac.compare_digest("sha256=" + hmac.new(secret, body, hashlib.sha256).hexdigest(), signature)
```

The payload carries no credentials: the agent fetches the evidence with its own token (`status:read`, and `alerts:write` to acknowledge and leave notes).

### A GitHub workflow

Beacon dispatches the workflow with two inputs, `alert` (the group's id) and `event` (`firing` or `resolved`), which the workflow must declare:

```yaml
on:
  workflow_dispatch:
    inputs:
      alert: {description: Beacon alert group id, required: true}
      event: {description: firing or resolved, required: false, default: firing}

jobs:
  oncall:
    if: inputs.event == 'firing'
    runs-on: ubuntu-latest
    steps:
      - name: Evidence
        env:
          ALERT: ${{ inputs.alert }}
          BEACON_TOKEN: ${{ secrets.BEACON_TOKEN }}
        run: |
          curl -fsS -H "Authorization: Bearer $BEACON_TOKEN" \
            "https://status.example.com/api/v1/alerts/$ALERT/evidence" > evidence.json
      # then the agent's own step, with Beacon's MCP server and its own token
```

Pass the inputs through `env` as above rather than into the script itself: anyone who may dispatch the workflow chooses them.

## The evidence

`GET /api/v1/alerts/{id}/evidence` or the MCP tool `get_evidence` (scope `status:read`) gives in one answer what an agent needs to judge the alert:

- **probes**: for the group's service, one result per location, target and module: whether it works at the end of the window, since when it fails (or when it came back), the reason (`probe_failure_info{reason}`: `dns`, `connect`, `timeout`, `tls`, `http_status`, …) and how many checks failed for each reason in the window, the duration, the HTTP status, the attempts (`linkos_probe_attempts`), the certificate's expiry, and whether the location has gone silent. Failing results come first. `queries` are the PromQL used, for a closer look through the gateway with a `metrics:read` token.
- **related**: other alert groups firing in the same `failure_domain`, or for the same service.
- **changes**: change events in the window (below), those about the group's service or machines first.
- **done**: the acknowledgement, the silences, the notes, the agents woken, the escalation, and the sessions in the window on the machines the alert's labels name (`machine`, `host`, `location`, `failure_domain`) or by the agents that acknowledged it or left notes.
- **links**: the runbook and the dashboard, and the routes for the alert and its timeline.

The window opens 30 minutes before the group started (`?before=2h` for longer, at most 24 hours) and closes now, or 30 minutes after the group resolved. When VictoriaMetrics cannot be reached, `probes.error` says so and the rest still answers.

### Untrusted text

Much of the evidence is text that comes from outside Beacon: label values (which probes and rule engines set), annotations made from them, what probed services answered, change events, hooks' answers, notes and the commands run on machines. Any of it could hold instructions meant for an agent. Every answer that carries such text has an `untrusted` object listing those fields, with a note to read them as data. Beacon also cleans them: terminal escapes, control characters and invisible characters (bidirectional controls, zero-width characters, Unicode tags) are removed, and long strings are cut, ending in `…`. Values that go into queries are checked first: a service whose name could change the query is not queried.

## Probe again, now

After a fix, an agent should not have to wait for the next probe cycle and the rule engine to learn whether it worked. `POST /api/v1/probe-now` or the MCP tool `probe_now` (scope `alerts:write`: it makes probes work and posts to the chat, and it belongs to working an alert, which that scope already covers) has every location that probes a target probe it again at once and returns each location's answer:

- **What to probe**: `alert_id` (the targets of the group's alerts, from their `instance` and `module` labels, or every target of their `service` when the alerts carry none), or `target` with an optional `module` (every module the target has otherwise), or `service` (every target of the service). At most 20 targets in one request.
- **wait**: seconds to wait for the locations, 45 by default, at most 90. The locations that have the target assigned are expected to answer; the call returns when all have, or when the wait is over.
- **The answer**: `targets` (what was probed, each with the locations expected), `results` per location and target (`ok`, `reason`, `duration_seconds`, `http_status`, `attempts`, `cert_expires`, `probed_at`; failing first), `missing` (the locations that did not answer in time: a probe that is down, or one too old to know the route), `complete`, and `summary` in one line, such as `7 OK, 2 failing (dns)`.

The probes push the same samples as in their cycle, so VictoriaMetrics has the fresh result and an alert resolves at the rule engine's next evaluation rather than a minute later. The call is audited (`probe_now`) and echoed to the chat in one line. Limits protect the probes: a target is probed on demand at most once every 15 seconds and a token has at most three requests in flight (`probe.busy`, 429); an unknown target, or one no active location probes, is `probe.not_found` (404); a Beacon without a gateway answers `probe.unavailable` (503). Requests live in memory for two minutes and do not survive a restart.

For the probes, the protocol gains two gateway routes and nothing changes for older ones: `GET /api/v1/agent/requests?wait=25s` (`probe:write`) answers at once with the requests pending for the probe's location, in the shape of its targets, or with an empty list when the wait passes (at most 55 seconds); `POST /api/v1/agent/requests/{id}` takes what the probe found (`request.not_found` once the request has expired). `linkos-probe agent` keeps one such poll hanging beside its cycle. Against an older gateway it gets 404, says so once and asks again in an hour; an older probe never asks, and its location appears under `missing`.

## Notes

`POST /api/v1/alerts/{id}/notes` with `{"text": "…"}`, or the MCP tool `add_note` (scope `alerts:write`), leaves a note on an alert group, firing or resolved: what was found, what was done, what comes next. The card shows the latest three, the chat hears each one, and the audit log keeps them all. A note does not acknowledge the alert; `ack_alert` does. Notes are at most 2000 characters; a group keeps its latest 100.

## The timeline

`GET /api/v1/alerts/{id}/timeline` or `get_timeline` (scope `status:read`) puts the alert's story in time order: `started`, `notified`, `buzzed`, `pushover`, `acknowledged`, `note`, `silenced` and `silence_ended`, `agent` (each hook called, with its result), `change` and `session` (those of the evidence), `resolved`. It covers the same window as the evidence.

## Change events

Deployments, configuration changes and Flux's reconciliations reach Beacon as change events, so the evidence for an alert says what changed just before it. Senders post them to the gateway (port 17006) with a token that has the `changes:write` scope:

```bash
docker exec linkos-beacon linkos-beacon token create ci-deploy -scopes changes:write -expires 365d
```

```bash
curl -fsS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"source": "github-actions", "kind": "deploy", "service": "hub-example-com", "summary": "hub v2.1",
       "url": "https://github.com/example/hub/actions/runs/1"}' \
  https://metrics.example.com/api/v1/changes
```

| Field | Meaning |
| --- | --- |
| `source` | Required: the sender, such as `github-actions` or `deploy-script` |
| `kind` | `deploy`, `config`, `rollback` or another word; `change` by default |
| `summary` | Required: what changed |
| `service`, `machine` | What it concerns, as the alerts' `service` label and the machines' names say it |
| `url`, `revision` | Where to read more; the commit or version |
| `time` | RFC 3339; now by default; at most an hour ahead, and no older than changes are kept |

Unknown fields are refused, so a typo does not go unnoticed. Beacon keeps the token's name with each change.

### Flux

Flux's notification-controller posts its events to a Provider of type `generic`. Point one at `/api/v1/changes/flux`, with the token in a header:

```yaml
apiVersion: v1
kind: Secret
metadata: {name: beacon-changes, namespace: flux-system}
stringData:
  address: https://metrics.example.com/api/v1/changes/flux
  headers: |
    Authorization: Bearer lbt_…
---
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Provider
metadata: {name: beacon-changes, namespace: flux-system}
spec:
  type: generic
  secretRef: {name: beacon-changes}
---
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Alert
metadata: {name: beacon-changes, namespace: flux-system}
spec:
  providerRef: {name: beacon-changes}
  eventSeverity: info
  eventSources:
    - {kind: Kustomization, name: "*"}
    - {kind: HelmRelease, name: "*"}
  eventMetadata:
    service: hub-example-com    # what these objects run
```

| Flux event | Change |
| --- | --- |
| `reason` | `kind`, such as `ReconciliationSucceeded` or `UpgradeFailed` |
| `involvedObject` | `object`: `Kustomization/flux-system/apps` |
| `message` (after the Alert's `summary`, if any) | `summary` |
| `metadata`: `…/revision` | `revision` |
| `severity`, `timestamp` | `severity`, `time` |
| `metadata`: `service`, `machine` (the Alert's `eventMetadata`), or `?service=…&machine=…` in the address | `service`, `machine` |

`source` is `flux`. Flux alerts that should page people still go to `POST /api/v2/alerts` as before; change events never page anyone and are not echoed to the chat.

Change events are kept for `changes.keep` (`720h`, 30 days, by default), at most 50,000. `GET /api/v1/changes` and the MCP tool `list_changes` (scope `status:read`) list them, newest first, by `service`, `machine`, `source` and `since` (`2h`, `7d` or a time; the last day by default).

## What Beacon does not do here

- It does not run agents or decide what they do, and it calls no model. It wakes the agent, answers it, and records what it did.
- It asks nobody for approval: the notes, the sessions and the timeline are there for people to see, and the pause switch of the SSH channel is the brake.
