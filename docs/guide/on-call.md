# Put an agent on call

When a service breaks, Beacon can wake the agent you choose, hand it the evidence in one call, let it reach the machine, have every probe location check its fix, and keep one timeline of everything that happened where people can read it. This page is for the person who sets that up: you have Beacon running and an agent connected over MCP, as in [Up in 30 minutes](../self-hosting/install.md), steps 1 to 4. It covers how the agent hears of an alert, what it is given, how it works the alert, and how its token stays valid without anyone handing it a new one.

Beacon calls no model and decides nothing for the agent. It wakes the agent, answers its questions and records what it does. What to do about an alert is the agent's decision, made inside its **harness**: the program that runs it, such as `beacon-oncall` below, a GitHub workflow, or one of your own. The harness is not part of Beacon.

## How an alert reaches an agent

1. **Probes fail and the rules fire.** Alerts that belong together (by default, those with the same name and service) form an **alert group**: one card, one hook call, one id.
2. **Beacon calls your hooks.** A hook is a signed webhook or a GitHub workflow dispatch, listed in the configuration. The call says which alert group and where its evidence is; it carries no credentials.
3. **The harness starts the agent** with the call's payload.
4. **The agent asks for the evidence** (`get_evidence`) with its own credentials: what each location sees and why it fails, the other alerts in the same failure domain, what changed just before, what has been done.
5. **It works on the machines** the evidence names, through the [SSH channel](machines.md) with its own `ssh` or with Beacon's tools (`run_command`, `read_file`, `edit_file`). Every command is recorded.
6. **It has the fix checked** (`probe_now`): every location probes the target again at once. The alert resolves on what the probes see, not on what the agent says.
7. **It leaves a note** (`add_note`) saying what was wrong, what it did and what remains. People see it on the card, in the chat and in the console.
8. **When the group resolves**, Beacon can call the hook once more, so that the agent can write a closing note.

## Try it by hand first

Before wiring a hook, see the same loop with you in the place of the hook. Start a drill (`docker exec beacon beacon drill fail 15m`), wait about five minutes for `ServiceDown`, and ask your agent: *"Is anything failing right now? Look at the evidence, check the machine, and tell me what you would do."* It finds the alert with `list_alerts`, reads `get_evidence`, and answers from what the probes see. A woken agent works the same way; the hook only takes the place of the person who asks.

## Wake an agent

List the hooks in the configuration:

```yaml
public_url: https://status.example.com     # needed: hooks tell agents where the evidence is

agents:
  - name: claude-oncall
    kind: webhook
    url: https://agents.example.com/hooks/beacon
    secret_file: /etc/beacon/secrets/agent-hook-secret   # at least 24 random characters
    severities: [critical, warning]
    service: "hub-.*|git-.*"                  # optional
    resolved: true                            # also say when the alert resolves
    resolved_after: 1m                        # ... a minute later (the default)
  - name: ops-workflow
    kind: github
    repository: example/ops
    workflow: oncall.yml
    ref: main
    token_file: /etc/beacon/secrets/github-dispatch-token
```

| Field | Meaning |
| --- | --- |
| `name` | Lower-case letters, digits, dots, dashes and underscores; shown on the card and in the chat |
| `kind` | `webhook` (a signed POST) or `github` (a `workflow_dispatch`) |
| `severities` | Which alert groups wake it; `[critical]` by default |
| `service` | A regular expression over the whole `service` label; a group of several services matches when one does |
| `resolved` | Call again when a group it was woken for resolves |
| `resolved_after` | How long that call waits after the group resolved, so that the closing note the agent's first run writes right after its fix is on the timeline when the agent hears of the end; `1m` by default, `0` to call at once, at most `5m` |
| `url`, `secret_file` | Webhook: where to POST, and the secret that signs each call |
| `headers_file` | Webhook: a file of `Name: value` lines sent with every call, for a receiver that checks a secret of its own (`Authorization: Bearer …`, `X-Webhook-Secret: …`); `#` starts a comment. One of `secret_file` and `headers_file` is required, both may be given |
| `repository`, `workflow`, `ref`, `token_file` | GitHub: `owner/name`, the workflow's file name or id, the branch (`main` by default), and a token that may dispatch it (a fine-grained token with Actions: read and write on that repository) |
| `api` | GitHub: `https://api.github.com` by default; another address for GitHub Enterprise |

`beacon check-config` refuses hooks without `public_url`, a webhook without `secret_file` or `headers_file`, and a secret shorter than 24 characters.

**When a hook is called.** Once, when a new alert group is first notified (when its card goes out), and not again while it keeps firing. If `resolved` is set, once more `resolved_after` after the group resolves. A group that fires again after resolving is a new episode and wakes the hook again; a resolved call still waiting at that moment is not made (the audit log says so), since the episode it was about is over.

**Delivery never waits for agents.** The calls run beside alert delivery: the card, the buzz and Pushover never wait for them. A call that gets no answer, a timeout, a 429 or a 5xx is tried again after 5 seconds, 20 seconds, 1 minute and 3 minutes. Any other refusal (401, 404, 422) fails at once, since trying again would not help. Redirects are not followed, so the payload and its signature go only to the address configured. Calls a restart interrupted are taken up again when they are less than ten minutes old.

**Where the outcome shows.** Each call's outcome is kept on the alert group (`agents` in `GET /api/v1/alerts/{id}` and `get_alert`), in the audit log (`wake_agent`), on the card ("Agent claude-oncall woken at 10:02", or that it could not be woken), in the alert chat with the reason when it failed, and on the console's **Hooks** page (`/console/hooks`).

### The webhook

The headers, the payload and how a receiver checks the signature: see [Agents woken by a webhook](../integrations/webhooks.md).

### A GitHub workflow

The workflow that takes the two inputs, and a complete one that runs Claude Code: see [GitHub Actions](../integrations/github-actions.md).

## Running an agent when woken

A webhook needs something at the other end that runs the agent. `beacon-oncall` is the reference harness: it takes the webhook, checks the signature and starts the program you name, with the payload on standard input and the alert in its environment; `contrib/oncall` has the wrappers for Claude Code and Codex, the prompt for the woken agent and a systemd unit. See [Headless agents and CI](../integrations/headless.md) for all of it, including what the harness decides and what it leaves to the agent's preset, its owner's role and its machines ([Trust](../integrations/webhooks.md#trust)).

## The evidence

`GET /api/v1/alerts/{id}/evidence`, or the MCP tool `get_evidence` (scope `status:read`), gives in one answer what an agent needs to judge an alert. Here is one, trimmed: two locations cannot connect to `git.example.com`, a deployment of that service went out four minutes before, and a deploy script's reload on the service's machine failed just before that.

```jsonc
{
  "alert": {"id": "0cdfbada62dd", "status": "firing", "severity": "critical", "title": "git.example.com is down",
            "service": "git.example.com", "labels": {"alertname": "ServiceDown", "failure_domain": "origin-1", "…": "…"},
            "alerts": 1, "starts_at": "2026-11-02T01:58:00Z"},
  "window": {"from": "2026-11-02T01:28:00Z", "to": "2026-11-02T02:03:10Z"},
  "probes": {
    "services": ["git.example.com"], "locations": 2, "failing": ["edge-1", "edge-2"],
    "results": [
      {"location": "edge-1", "instance": "https://git.example.com/", "module": "http_head", "ok": false,
       "failing_since": "2026-11-02T01:54:30Z", "reason": "connect", "reasons": {"connect": 9},
       "duration_seconds": 0.004, "attempts": 1, "last_seen": "2026-11-02T02:03:00Z"},
      {"location": "edge-2", "ok": false, "reason": "connect", "…": "…"}
    ],
    "queries": ["probe_success{service=\"git.example.com\"}", "…"]
  },
  "related": [{"id": "7a01c2e9f3b4", "title": "registry.example.com is down", "severity": "critical",
               "starts_at": "2026-11-02T01:59:00Z", "acknowledged": false, "why": "failure_domain=origin-1"}],
  "changes": [{"time": "2026-11-02T01:53:40Z", "source": "github-actions", "kind": "deploy", "service": "git.example.com",
               "summary": "git v2.4.1", "posted_by": "ci-deploy", "related": "service", "phase": "before", "…": "…"}],
  "done": {
    "silences": [], "notes": [],
    "agents": [{"hook": "claude-oncall", "event": "firing", "status": "ok", "code": 202, "at": "2026-11-02T02:00:31Z", "…": "…"}],
    "machines": ["origin-1"],
    "sessions": [{"id": "20261102-015312-3fa9c1", "agent": "deploy-bot", "via": "ssh", "machine": "origin-1", "kind": "exec",
                  "command": "sudo systemctl reload nginx", "exit": 1, "related": "machine", "phase": "before",
                  "page": "https://status.example.com/console/sessions/20261102-015312-3fa9c1?s=…"}]
  },
  "links": {"runbook": "https://github.com/example/ops/blob/main/runbooks/ServiceDown.md",
            "page": "https://status.example.com/console/alerts/0cdfbada62dd?s=…", "…": "…"},
  "untrusted": {"fields": ["alert.title", "probes.results[].reason", "changes[].summary", "done.sessions[].command", "…"], "note": "…"}
}
```

The parts:

- **probes**: for the group's service, one result per location, target and module: whether it works at the end of the window, since when it fails (or when it came back), the reason (`probe_failure_info{reason}`: `dns`, `connect`, `timeout`, `tls`, `http_status`, …) and how many checks failed for each reason in the window, the duration, the HTTP status, the attempts (`beacon_probe_attempts`), the certificate's expiry, and whether the location has gone silent (`stale`, no result for over three minutes). Failing results come first. `queries` are the PromQL used, for a closer look through the gateway with a `metrics:read` token.
- **heartbeats**: for an alert whose labels name a `heartbeat` (`HeartbeatStale`, `HeartbeatMissing`), the heartbeat's catalogue entry (`name`, `description`, `max_age`, `severity`, `machine`), whether the SSH channel has that machine (`enrolled`), its last success (`last_success`, from `beacon_heartbeat_last_success_timestamp_seconds` within 31 days) and a `hint` on reaching the machine. Absent for other alerts.
- **related**: other alert groups firing in the same `failure_domain`, or for the same service, with `why` saying which.
- **changes**: [change events](#change-events) in the window, those about the group's service or machines first.
- **done**: the acknowledgement, the silences, the notes, the agents woken, the escalation, the alert's `machines` (those its labels name in `machine`, `host`, `location` and `failure_domain`, and those the catalogue names for its heartbeats), and the sessions in the window on those machines or by the agents that acknowledged it or left notes, the oldest first. Each session has its agent, machine, kind, command and exit status; a shell has its last 20 commands with their exit statuses (`shell_commands`, for up to ten shells, those before the alert first; `commands` counts them all, and `get_session` has the rest with their output), and every session the link to its page.
- **links**: the runbook and the dashboard, the routes for the alert and its timeline, and `page`, the signed link to the alert's page for people.

**The window** opens 30 minutes before the group started (`?before=2h` for longer, at most 24 hours) and closes now, or 30 minutes after the group resolved. So what was done on the alert's machines just before it is there: the session that stopped a heartbeat's timer three minutes before `HeartbeatStale` fired, say. Every change and session has a `phase`: `before` the group started (a possible cause), `during` (while it fired) or `after` it resolved. When there are more than 50 sessions, up to 20 of those on the alert's machines before it started are kept first, then the latest. When VictoriaMetrics cannot be reached, `probes.error` and `heartbeats[].error` say so and the rest still answers.

### Untrusted text

Much of the evidence is text from outside Beacon: label values (which probes and rule engines set), annotations made from them, what probed services answered, change events, hooks' answers, notes and the commands run on machines. Any of it could hold instructions meant for an agent. Every answer that carries such text has an `untrusted` object listing those fields, with a note to read them as data. Beacon also cleans them: terminal escapes, control characters and invisible characters (bidirectional controls, zero-width characters, Unicode tags) are removed, and long strings are cut, ending in `…`. Values that go into queries are checked first: a service whose name could change the query is not queried.

## Probe again, now

After a fix, an agent should not have to wait for the next probe cycle and the rule engine to learn whether it worked. `POST /api/v1/probe-now`, or the MCP tool `probe_now`, has every location that probes a target probe it again at once, and returns each location's answer. It takes the scope `alerts:write`: it makes probes work and posts to the chat, and it belongs to working an alert, which that scope already covers.

- **What to probe**: `alert_id` (the targets of the group's alerts, from their `instance` and `module` labels, or every target of their `service` when the alerts carry none), or `target` with an optional `module` (every module the target has otherwise), or `service` (every target of the service). At most 20 targets in one request.
- **`wait`**: seconds to wait for the locations, 45 by default, at most 90. The locations that have the target assigned are expected to answer; the call returns when all have, or when the wait is over.
- **The answer**: `targets` (what was probed, each with the locations expected), `results` per location and target (`ok`, `reason`, `duration_seconds`, `http_status`, `attempts`, `cert_expires`, `probed_at`; failing first), `missing` (the locations that did not answer in time: a probe that is down, or one too old to know the route), `complete`, and `summary` in one line, such as `7 OK, 2 failing (dns)`.

The probes push the same samples as in their cycle, so VictoriaMetrics has the fresh result and an alert resolves at the rule engine's next evaluation rather than a minute later. The call is audited (`probe_now`) and echoed to the chat in one line.

Limits protect the probes: a target is probed on demand at most once every 15 seconds, and a token has at most three requests in flight (`probe.busy`, 429). An unknown target, or one no active location probes, is `probe.not_found` (404); a Beacon without a gateway answers `probe.unavailable` (503). Requests live in memory for two minutes and do not survive a restart.

For the probes, the protocol gains two gateway routes and nothing changes for older ones. `GET /api/v1/agent/requests?wait=25s` (`probe:write`) answers at once with the requests pending for the probe's location, in the shape of its targets, or with an empty list when the wait passes (at most 55 seconds); `POST /api/v1/agent/requests/{id}` takes what the probe found (`request.not_found` once the request has expired). `beacon-probe agent` keeps one such poll open beside its cycle. Against an older gateway it gets 404, says so once and asks again in an hour; an older probe never asks, and its location appears under `missing`.

## Notes

A note is how an agent, or a person, tells everyone else what it found, did and will do next. `POST /api/v1/alerts/{id}/notes` with `{"text": "…"}`, or the MCP tool `add_note` (scope `alerts:write`), leaves one on an alert group, firing or resolved. The card shows the latest three, the chat hears each one, and the audit log keeps them all. A note does not acknowledge the alert; `ack_alert` does. Notes are at most 2000 characters; a group keeps its latest 100. A note that begins with `public:` is also an update of the incident on the [status pages](status-pages.md), without the prefix.

## The timeline

`GET /api/v1/alerts/{id}/timeline`, or `get_timeline` (scope `status:read`), tells the alert's story in time order: `started`, `notified`, `buzzed`, `pushover`, `acknowledged`, `note`, `silenced` and `silence_ended`, `agent` (each hook called, with its result), `change` and `session` (those of the evidence), `command` (each of a shell's `shell_commands`, with its session's id and its exit status), `heartbeat` (a heartbeat's last success, even when that is before the window), `resolved`. It covers the same window as the evidence, so the changes and sessions on the alert's machines just before it come before `started`; those events have the evidence's `phase`, and `heartbeats` is there as in the evidence. The console's alert page shows the same story.

## Change events

When deployments, configuration changes and Flux's reconciliations reach Beacon as change events, the evidence for an alert says what changed just before it. How to send them, the fields and the Flux setup: see [Deployments and Flux](../integrations/change-events.md).

## Tokens that renew themselves

Rather than have someone hand a headless agent a long-lived token, and a new one when that expires, the token renews itself before it does: see [Headless agents and CI](../integrations/headless.md#tokens-that-renew-themselves).

## What Beacon does not do here

- It does not run agents or decide what they do, and it calls no model. It wakes the agent, answers it, and records what it did.
- It asks nobody for approval. The notes, the sessions and the timeline are there for people to see, and the SSH channel's pause switch is the brake.
