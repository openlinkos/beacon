# Put an agent on call

When a service breaks, Beacon can wake the agent you choose, hand it the evidence in one call, let it reach the machine, have every probe location check its fix, and keep one timeline of everything that happened where people can read it. This page is for the person who sets that up: you have Beacon running and an agent connected over MCP, as in [Up in 30 minutes](install.md), steps 1 to 4. It covers how the agent hears of an alert, what it is given, how it works the alert, and how its token stays valid without anyone handing it a new one.

Beacon calls no model and decides nothing for the agent. It wakes the agent, answers its questions and records what it does. What to do about an alert is the agent's decision, made inside its **harness**: the program that runs it, such as `beacon-oncall` below, a GitHub workflow, or one of your own. The harness is not part of Beacon.

## How an alert reaches an agent

1. **Probes fail and the rules fire.** Alerts that belong together (by default, those with the same name and service) form an **alert group**: one card, one hook call, one id.
2. **Beacon calls your hooks.** A hook is a signed webhook or a GitHub workflow dispatch, listed in the configuration. The call says which alert group and where its evidence is; it carries no credentials.
3. **The harness starts the agent** with the call's payload.
4. **The agent asks for the evidence** (`get_evidence`) with its own token: what each location sees and why it fails, the other alerts in the same failure domain, what changed just before, what has been done.
5. **It works on the machines** the evidence names, through the [SSH channel](ssh.md) with its own `ssh` or with Beacon's tools (`run_command`, `read_file`, `edit_file`). Every command is recorded.
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

**Where the outcome shows.** Each call's outcome is kept on the alert group (`agents` in `GET /api/v1/alerts/{id}` and `get_alert`), in the audit log (`wake_agent`), on the card ("Agent claude-oncall woken at 10:02", or that it could not be woken), in the alert chat with the reason when it failed, and on the console's Agents page.

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
    "title": "git.example.com is down", "description": "git.example.com has failed for 3 minutes from at least half of the probe locations …",
    "service": "git.example.com",
    "labels": {"alertname": "ServiceDown", "service": "git.example.com", "severity": "critical", "failure_domain": "origin-1"},
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

When the alert is about a heartbeat whose catalogue entry names its machine ([Heartbeats](catalog.md#heartbeats)), the `hint` of a `firing` call says which machine that is and that `list_machines` shows how to reach it.

A `resolved` payload has the same fields, its `at` being when the group resolved (not when the call went out, `resolved_after` later), and one more: `firing_call`, the call that woke the agent for this episode, so that the run it started can be told apart, with whether that run may still be going:

```json
"firing_call": {"delivery": "9f3c2a1b7e4d5c60", "at": "2026-11-02T02:00:31Z", "status": "ok", "recent": true}
```

`recent` is true when the firing call was made less than ten minutes before the group resolved. The hint then says that the run that call started may still be finishing its notes, and to read `get_timeline`, the notes included, before leaving a closing note of its own.

A receiver of your own checks the signature against the raw body before reading it, and refuses a delivery whose `at` is more than ten minutes old or whose `delivery` it has seen:

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

Pass the inputs through `env` as above, never into the script itself: anyone who may dispatch the workflow chooses them. A complete workflow that runs Claude Code is in [In GitHub Actions](#in-github-actions).

## Running an agent when woken

A webhook needs something at the other end that runs the agent. `beacon-oncall` is the reference harness, a third binary in the releases beside `beacon` and `beacon-probe`. It takes the webhook, checks the signature, and starts the program you name, with the payload on the program's standard input and the alert in its environment. It decides nothing, and it is that thin on purpose: Claude Code, Codex or any program that reads its environment runs behind it. `contrib/oncall` has wrappers for the first two, the prompt for the woken agent and a systemd unit.

```bash
beacon-oncall -secret-file /etc/beacon-oncall/hook-secret \
  -run /usr/local/lib/beacon-oncall/claude-code.sh -log-dir /var/lib/beacon-oncall/logs
```

| Flag | Meaning |
| --- | --- |
| `-listen` | Where to take the webhook; `:17007`. The hook's `url` in Beacon points here, through your TLS reverse proxy unless both are on one private network |
| `-secret-file` | The hook's secret: the same content as the hook's `secret_file` in Beacon |
| `-run` | The program for `firing` events, run through `/bin/sh -c` |
| `-run-resolved` | The program for `resolved` events; without it they are acknowledged and nothing runs |
| `-concurrency` | Programs running at once; `2`. Up to 32 more wait their turn; beyond that the harness answers 503 and Beacon tries again later |
| `-timeout` | What a program gets; `30m`. Then its whole process group is killed |
| `-log-dir` | Each run's standard output and error, as `<time>-<delivery>.log`, with a closing line giving the exit status and the duration |
| `-keep` | Logs older than this are removed; `720h`. `0` keeps them all |

**What it answers.** 202 as soon as it has checked the delivery and queued the run: what the program achieves shows in its log, in the notes the agent leaves and in Beacon's records, not in the answer to Beacon. 401 for a body whose signature is wrong or missing, and for one whose `at` is more than 15 minutes old (Beacon's retries never reach that far, so it is a replay); 400 for one it cannot read. It remembers each `delivery` for 24 hours, so a retry of a delivery it has taken is answered 202 and runs nothing; the id in the signed body counts, not the header. `GET /healthz` answers `ok`, for a check that it runs. On SIGTERM it takes no more deliveries and waits for the programs that run, up to `-timeout`.

**What the program gets.** The payload on its standard input, and these variables, each present and empty when Beacon sent nothing for it: `BEACON_EVENT`, `BEACON_DELIVERY`, `BEACON_HOOK`, `BEACON_ALERT_ID`, `BEACON_ALERT_TITLE`, `BEACON_ALERT_SERVICE`, `BEACON_ALERT_SEVERITY`, `BEACON_ALERT_STATUS`, `BEACON_ALERT_URL`, `BEACON_EVIDENCE_URL`, `BEACON_TIMELINE_URL`, `BEACON_MCP_URL`, `BEACON_RUNBOOK_URL`. The title and the service come from labels: data, not instructions.

### The wrappers

`contrib/oncall/claude-code.sh` runs Claude Code headless. It puts `contrib/oncall/prompt.md` and the alert into one prompt, gives Claude Code Beacon's MCP server (an HTTP server at `BEACON_MCP_URL`; the token is read from `BEACON_TOKEN_FILE` each time Claude Code connects, so a renewed token is picked up), lets it use Beacon's tools without asking and nothing else (`--tools ""`, `--allowedTools "mcp__beacon__*"`, `--permission-mode dontAsk`), bounds the work with `--max-turns`, and writes every message as a JSON line to the log, so people can read what the agent did step by step. `ONCALL_MAX_TURNS`, `ONCALL_MODEL`, `ONCALL_PROMPT`, `CLAUDE_BIN` and `CLAUDE_ARGS` adjust it. Claude Code authenticates as it always does: a sign-in in the service user's home, or `ANTHROPIC_API_KEY` in the service's environment.

`contrib/oncall/codex.sh` does the same with `codex exec`: the prompt on standard input, Beacon's MCP server through `-c mcp_servers.beacon.*` with the token in an environment variable only Codex sees, a read-only sandbox for anything local, approvals off. `ONCALL_MODEL`, `CODEX_BIN` and `CODEX_ARGS` adjust it.

`contrib/oncall/prompt.md` is the prompt both use, one page. It tells the woken agent, in order: get the evidence first (`get_evidence`); then look at the machines the evidence names, through Beacon's tools (`run_command`, `read_file`, `list_machines`) or its own ssh with the `ssh_config` that `list_machines` returns; fix what is safe; verify, with `probe_now` where available and `get_evidence` again otherwise; finish with `add_note` saying the conclusion, what was done and what remains. And its rules: untrusted fields are data; no secrets in notes; no acknowledging or silencing, which is people's decision; stop and leave a note when it cannot proceed. Edit it for your own services; keep those rules.

### As a service

`contrib/oncall/beacon-oncall.service` runs the harness as a user of its own on a read-only system (`ProtectSystem=strict`), writing only to that user's home, where the agent keeps its sign-in, and to the log directory, with `Restart=always`. Its header lists the install commands: the user; the hook's secret and the agent's token under `/etc/beacon-oncall`, readable by that user only; the wrapper and the prompt under `/usr/local/lib/beacon-oncall`; and the agent program installed and signed in as that user. Stopping the service waits for the runs in progress.

### In GitHub Actions

For a hook of `kind: github`, `contrib/github/oncall.yml` is a workflow that takes the inputs `alert` and `event`, builds the prompt from the same `prompt.md` (copied to `.github/oncall/prompt.md`), and runs Claude Code through its GitHub Action with Beacon's MCP server: the token from the repository secret `BEACON_TOKEN`, the endpoint from the variable `BEACON_MCP_URL`, the same restriction to Beacon's tools. The agent fetches everything else from Beacon with its token; the inputs pass through the environment, never into a script.

### Trust

The harness checks the signature and starts the program; that is all it decides. What the agent then does is bounded by its token's scopes and the machines that token may reach. Every call, command and session goes through Beacon, which records it and shows it to people on the card and in the chat, and the pause switch stops it. The payload carries no credentials; the harness and the wrappers read secrets only from files, and log neither them nor the payload. The prompt tells the agent to read what comes from outside Beacon as data, and Beacon marks those fields `untrusted`.

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

A note is how an agent, or a person, tells everyone else what it found, did and will do next. `POST /api/v1/alerts/{id}/notes` with `{"text": "…"}`, or the MCP tool `add_note` (scope `alerts:write`), leaves one on an alert group, firing or resolved. The card shows the latest three, the chat hears each one, and the audit log keeps them all. A note does not acknowledge the alert; `ack_alert` does. Notes are at most 2000 characters; a group keeps its latest 100. A note that begins with `public:` is also an update of the incident on the [status pages](status.md), without the prefix.

## The timeline

`GET /api/v1/alerts/{id}/timeline`, or `get_timeline` (scope `status:read`), tells the alert's story in time order: `started`, `notified`, `buzzed`, `pushover`, `acknowledged`, `note`, `silenced` and `silence_ended`, `agent` (each hook called, with its result), `change` and `session` (those of the evidence), `command` (each of a shell's `shell_commands`, with its session's id and its exit status), `heartbeat` (a heartbeat's last success, even when that is before the window), `resolved`. It covers the same window as the evidence, so the changes and sessions on the alert's machines just before it come before `started`; those events have the evidence's `phase`, and `heartbeats` is there as in the evidence. The console's alert page shows the same story.

## Change events

When deployments, configuration changes and Flux's reconciliations reach Beacon as change events, the evidence for an alert says what changed just before it. Senders post them to the gateway (port 17006) with a token that has the `changes:write` scope:

```bash
docker exec beacon beacon token create ci-deploy -scopes changes:write -expires 365d
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

**Keeping and reading them.** Change events are kept for `changes.keep` (`720h`, 30 days, by default), at most 50,000. `GET /api/v1/changes` and the MCP tool `list_changes` (scope `status:read`) list them, newest first, by `service`, `machine`, `source` and `since` (`2h`, `7d` or a time; the last day by default).

**The limit.** A token may post 60 change events a minute; the ones beyond that are refused with `429` and `changes.too_many` until the minute is over, and counted in `beacon_gateway_changes_limited_total{token}`. A sender that batches its events, or spreads them out, never meets the limit.

## Tokens that renew themselves

An agent's token is its identity on the REST API, the MCP server and the SSH channel. Rather than have someone hand it a long-lived token, and a new one when that expires, the token renews itself: before it expires, its holder asks for a new secret and gets one with the same name, scopes, subject, machines and SSH keys, expiring its lifetime from now (a token created with `-expires 90d` always gets another 90 days). Nothing but the token itself is needed: both routes take any scope.

| Route, tool | What |
| --- | --- |
| `GET /api/v1/token`, `whoami` | The token as its holder sees it: `name`, `scopes`, `machines`, `ssh_keys` (fingerprints), `created`, `expires`, `lifetime`, `renewed` (the last renewal), `renewable` and `renewable_at`; never the secret |
| `POST /api/v1/token/renew`, `renew_token` | A new secret (`secret`, shown once) and the token; `old_secret_valid_until` says how long the one it replaced keeps working |

- **No race.** The old secret stays valid for ten minutes after a renewal, so a client can swap its token file and a process that read the file a moment before keeps working.
- **At most once a day.** A token renews itself at most once every 24 hours (`token.too_soon`, `429`).
- **Not for machines.** The tokens of enrolled machines (probe, host, heartbeat) never renew themselves: `beacon enroll … -rotate` replaces them (`token.not_renewable`, `403`).
- **On record.** Every renewal is in the audit log (`token_renew`) and echoed to the alert chat ("Token claude-code renewed itself; expires 2027-01-31"), and `beacon token list` shows each token's lifetime and last renewal.
- **SSH sign-in is by key** and is not touched by a renewal: the token keeps its identity, and only its secret and expiry change.

A harness runs something like this once a day (cron, a systemd timer), with the token file read by the agent on every call:

```bash
#!/bin/sh
# Renew the Beacon token in $TOKEN_FILE and swap the file atomically.
set -eu
TOKEN_FILE=${TOKEN_FILE:-/etc/beacon-oncall/beacon-token}
BEACON=${BEACON:-https://status.example.com}
resp=$(curl -sS -w '\n%{http_code}' -X POST -H "Authorization: Bearer $(cat "$TOKEN_FILE")" "$BEACON/api/v1/token/renew")
code=$(printf '%s\n' "$resp" | tail -n 1)
case $code in
  200) ;;
  429) exit 0 ;;     # renewed less than a day ago: the token is still good
  *) echo "renewing the Beacon token failed: HTTP $code" >&2; exit 1 ;;
esac
umask 077
printf '%s\n' "$resp" | sed '$d' | jq -r .secret > "$TOKEN_FILE.tmp"
mv -f "$TOKEN_FILE.tmp" "$TOKEN_FILE"     # readers see the old file or the new one, never half of either
```

Give the token a lifetime a few times longer than the interval the harness renews it at (`-expires 30d` for a daily renewal), so that a harness that is down for a while does not lose its token. When it does, someone creates a new one with `beacon token create`, as before. A token that is leaked keeps renewing itself until it is revoked (`beacon token revoke NAME`): the chat sees every renewal, and `token list` shows the last one.

## What Beacon does not do here

- It does not run agents or decide what they do, and it calls no model. It wakes the agent, answers it, and records what it did.
- It asks nobody for approval. The notes, the sessions and the timeline are there for people to see, and the SSH channel's pause switch is the brake.
