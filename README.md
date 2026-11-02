# Beacon · 烽火

English | [简体中文](README.zh-CN.md)

Beacon is infrastructure for AI agents that run operations. It tells whether your services work for your users, checked from where they are; it lets agents reach your machines over SSH without ever holding a key, and records every session; and it puts the people on call and their agents on one platform. A Go service and a small probe agent, on top of VictoriaMetrics and vmalert. What it aims at, and what it leaves out, is in [docs/goals.md](docs/goals.md).

> Named after the beacon towers of the Great Wall: watch posts along the border lit a fire when they saw trouble, the signal ran from tower to tower to the capital, and the number of fires told how serious it was.

Early days: Beacon runs the monitoring of LinkOS's own services and the SSH channel to its machines. Being built now: the first agent on call (a reference harness and probing on demand), the pages people open from a card, machines behind NAT, and a reference deployment for other teams.

## What it does

### Sees: monitoring

- **Probes from where your users are.** `linkos-probe` runs on any small machine (an edge node, a VPS in mainland China) and checks HTTP, TCP, TLS, container registries, Git over HTTP and domain expiry, like the Blackbox Exporter and with its metric names, and says why each failure happened: DNS, connect, timeout, TLS, status code, body. It reports a few host metrics and heartbeats too, and pushes everything with a queue on disk for when the network is down. One static binary, under 20 MB of memory.
- **An outage is what several locations agree on.** A service is down only when at least two locations, and at least half of those reporting, fail; one location's hiccup never pages anyone. Locations follow their machines: when a machine is let go, its location stops counting.
- **One door for machines.** The gateway gives each probe and host its own token (`linkos-beacon enroll`) and labels what it writes, so no machine can speak for another. It hands every probe its targets, probe modules and settings (a longer timeout for a machine on a lossy path, say), so changing what is checked needs no redeployment, and it takes heartbeats, alerts from the rule engine and Flux, and metric queries from agents.
- **Alert routing built in.** Grouping, inhibition and silences, as Alertmanager does them, inside the service. Rule engines post alerts to it as they would to Alertmanager.
- **Alert cards in Feishu.** A new alert becomes an interactive card in the alert chat: what is wrong, since when, which locations see it failing, and links to the runbook and the dashboard. Buttons on the card acknowledge it (with a note) or silence it for 1, 4 or 24 hours. The card changes in place, and turns green when the alert resolves.
- **Escalation to phones.** A critical alert that nobody acknowledges is buzzed in the Feishu app after 10 minutes and sent to Pushover's emergency priority after 15: the phone keeps ringing until someone acknowledges it there, which also acknowledges it here. An acknowledgement lapses after 4 hours if the alert is still firing.
- **Nothing silently lost.** Low-severity alerts and anything over the Feishu app's monthly API budget go to the chat's custom bot, which needs no quota. If the card cannot be sent, a critical alert goes straight to Pushover. The service pings healthchecks.io every minute while rules, routing and delivery all work, so a dead monitoring host or a broken Feishu connection still reaches a phone.
- **CDN nodes in rotation.** For probe locations on CDN nodes, Beacon notices when a node drops out of its cluster's DNS answers (up and probing, yet serving nobody), when a standby node steps in, and when the rotation holds a node nobody watches.

### Reaches: the SSH channel

- **Agents use the tools they know.** ssh, scp, sftp, rsync and port forwarding go through Beacon unchanged, and MCP tools run commands and read and edit files with clean, structured results.
- **No keys for agents.** Beacon holds the credentials and signs a certificate valid for a few minutes for each connection; machines trust only Beacon's certificate authority.
- **Every session on record.** Sessions are recorded for replay, and interactive shells are broken down into commands with their exit status. Deciding what to run is the agent's own business; people see everything it does and can pause agents at once.

### Connects: the platform

- **An API and MCP for agents.** List alerts, acknowledge, silence and end silences, leave notes, with scoped tokens that expire. Every write is audited and echoed to the alert chat, so what an agent did is visible to everyone.
- **Agents woken, with the evidence at hand.** A new alert can wake an agent: a signed webhook or a GitHub workflow, told where the evidence is. One call returns it: each probe location's result and why it fails, the alerts in the same failure domain, the changes just before (deployments, Flux), and what was done. Notes an agent leaves show on the card, and one timeline joins alerts, notes, agents woken, changes and sessions. Beacon calls no model: the agent decides. See [docs/agents.md](docs/agents.md).
- **Configured through Beacon.** Probe locations, heartbeats, expiring resources, probe targets and modules live in Beacon's database and are changed through the API, MCP and the command line, checked and in force at once. Every change is a version with who made it and the values before and after, and can be reverted; files are for import and export, so they can still live in git.
- **Small.** A whole installation is four containers (Beacon, VictoriaMetrics, vmalert, Caddy) and one binary per probe machine.

## How it fits

```
linkos-probe (each location) ─push─▶ gateway ─▶ VictoriaMetrics ─▶ vmalert ─alerts─▶ router ─▶ bridge ─▶ Feishu app ◀─▶ people on call
hosts, CI, Flux ─────────────────▶ gateway                                                          ├─▶ Feishu group bot
agents ─▶ /api/v1, /mcp ─▶ linkos-beacon                                                            ├─▶ Pushover / Bark
                                                                                                    ├─▶ agents' hooks (webhook, GitHub workflow)
                                   healthchecks.io ◀─ every minute, while rules, routing and delivery work
```

The gateway, the router and the bridge are all `linkos-beacon`. Alerts need the labels `severity` (`critical`, `warning` or `info`) and `service`, and the annotation `summary` (optionally `description`, `runbook`, `dashboard`). An alert named `Watchdog` proves the pipeline works and is never shown.

## Running it

```bash
docker run -d --name linkos-beacon -p 127.0.0.1:17001:17001 -p 127.0.0.1:17006:17006 -p 17002:17002 \
  -v ./config.yaml:/etc/linkos-beacon/config.yaml:ro \
  -v ./catalog:/etc/linkos-beacon/catalog:ro \
  -v ./targets:/etc/linkos-beacon/targets:ro \
  -v ./secrets:/etc/linkos-beacon/secrets:ro \
  -v linkos-beacon:/var/lib/linkos-beacon \
  ghcr.io/openlinkos/beacon:latest
```

Start from [config.example.yaml](config.example.yaml); `linkos-beacon check-config` validates it, with the catalogue (probe locations, heartbeats, expiring resources), the probe targets and the module file, all described in [docs/catalog.md](docs/catalog.md). The first start imports those files into the database, where they are changed from then on; the two mounts are needed only for that, for `SIGHUP` and for `config import`. Port 17001 serves the API and MCP, and the status pages once they land; port 17006 is the gateway for machines; port 17002 is the SSH channel, set up as in [docs/ssh.md](docs/ssh.md).

Point vmalert at the gateway: `-notifier.url=http://linkos-beacon:17006 -notifier.bearerTokenFile=<the file in rules.token_file>`.

Give each probe location its token and start the agent there:

```bash
docker exec linkos-beacon linkos-beacon enroll probe edge-1 > token     # edge-1 is a location in the catalogue
linkos-probe agent -gateway https://metrics.example.com -token-file /etc/linkos-agent/token
```

Set up the Feishu app as in [docs/feishu.md](docs/feishu.md). Pushover needs its iOS app with Critical Alerts allowed, an application token and your user key.

### Tokens, machines and drills

Commands that change the running service talk to it over a socket in its data directory:

```bash
docker exec linkos-beacon linkos-beacon token create claude-code -scopes status:read,alerts:write,machines:ssh -expires 90d \
  -ssh-key "$(cat ~/.ssh/id_ed25519.pub)"                 # the key it signs in to the SSH channel with
docker exec linkos-beacon linkos-beacon token list
docker exec linkos-beacon linkos-beacon machine add edge-1 edge-1.example.net   # pins its host key, prints what the machine needs
docker exec linkos-beacon linkos-beacon ssh pause         # closes agents' sessions on machines until "ssh resume"
docker exec linkos-beacon linkos-beacon enroll probe edge-1 -rotate    # a new token; the old one stops working
docker exec linkos-beacon linkos-beacon drill fail 15m    # /drill/healthz answers 503: probe it to rehearse the whole chain
docker exec linkos-beacon linkos-beacon drill off
docker exec linkos-beacon linkos-beacon config import /etc/linkos-beacon -dry-run   # catalog/, targets/: what would change
docker exec linkos-beacon linkos-beacon config export /tmp/beacon   # the configuration as files, in the same layout
docker exec linkos-beacon linkos-beacon config history    # who changed what; config revert VERSION undoes one change
```

## API and MCP

- REST: `/api/v1`, described in [internal/api/openapi.yaml](internal/api/openapi.yaml) (also served at `/api/v1/openapi.yaml`).
- MCP: streamable HTTP at `/mcp` with the same bearer tokens. Tools for alerts: `status_summary`, `list_alerts`, `get_alert`, `list_silences`, `list_audit`, `ack_alert`, `silence_alert`, `expire_silence`. For working an alert: `get_evidence`, `get_timeline`, `add_note`, `list_changes`. For machines: `list_machines`, `run_command`, `read_file`, `write_file`, `edit_file`, `list_sessions`, `get_session`, `add_machine`, `remove_machine`, `pause_ssh`, `resume_ssh`. For the configuration (scope `config:write` to change it): `get_config`, `put_config`, `delete_config`, `list_config_changes`, `get_config_change`, `revert_config_change`.

```bash
claude mcp add --transport http linkos-beacon https://status.example.com/mcp --header "Authorization: Bearer lbt_…"
```

| Route (port 17001) | Purpose |
| --- | --- |
| `GET /healthz`, `GET /readyz` | Liveness; readiness (Watchdog seen, Feishu connection up, budget left) |
| `GET /metrics` | Prometheus metrics, direct connections only |
| `POST /hooks/feishu` | Feishu card callbacks when `feishu.callback: http` |
| `GET /drill/healthz` | 200, or 503 during a drill |

| Gateway route (port 17006) | Scope | Purpose |
| --- | --- | --- |
| `POST /api/v1/import/prometheus` | `probe:write`, `host:write`, `heartbeat:write` | Samples in the text format (what `linkos-probe` pushes), heartbeats |
| `POST /api/v1/write` | `probe:write`, `host:write` | Prometheus remote write |
| `GET /api/v1/targets`, `GET /api/v1/modules`, `GET /api/v1/agent` | `probe:write` | What the probe checks, how, and its location's settings |
| `POST /api/v2/alerts` | the rule engine's token, `alerts:send` | Alerts, in Alertmanager's API v2 form |
| `POST /api/v1/changes`, `POST /api/v1/changes/flux` | `changes:write` | Change events: deployments and the like, and Flux's events (its `generic` provider); see [docs/agents.md](docs/agents.md) |
| `GET /api/v1/query`, `query_range`, `series`, … | `metrics:read` | Metric queries for agents |

The SSH channel (port 17002) takes keys given to tokens with the `machines:ssh` scope; the user name is the machine. See [docs/ssh.md](docs/ssh.md).

## Security

Tokens are stored only as SHA-256 hashes and expire; failed attempts are limited per client address. A probe or host token can only write as its own location or host, and a location whose machine has expired cannot write at all. Only the people listed in `feishu.on_call` can act on cards. Secrets are read from files and never logged. Report vulnerabilities to security@linkos.io.

## Licence

[Apache License 2.0](LICENSE). The LinkOS and OpenLinkOS names and marks are not covered by it.
