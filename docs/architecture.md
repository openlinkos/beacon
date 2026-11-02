# Architecture

How Beacon is put together: the processes, where state lives, the path an alert takes, how an agent is woken and reaches a machine, and the rules every part keeps. It is written for people who run Beacon and want to know where things happen, and for contributors; [Goals](goals.md) says why it is built this way, and `AGENTS.md` in the repository holds the rules for changing the code.

## The whole picture

```
beacon-probe agent ──push (text format, gzip)──▶ gateway ──▶ VictoriaMetrics ◀── vmalert (rules)
  ▲ targets, modules, settings ◀── gateway                                          │ alerts (Alertmanager API v2)
                                                                                    ▼
people ◀── Feishu cards, Pushover ◀── bridge ◀── router (group, inhibit, silence) ◀── gateway ◀── CI, Flux (change events)
agents ◀── webhook, workflow dispatch ◀── bridge (a new alert wakes them)
agents ──▶ REST /api/v1, MCP /mcp ──▶ bridge (evidence, timeline, notes), store, SSH channel
agents ──▶ ssh, scp, sftp, rsync (port 17002) ──▶ SSH channel ──▶ machines (5-minute certificates; every session recorded)
```

Three binaries, all in Go with no cgo:

| Binary | Where it runs | What it does |
| --- | --- | --- |
| `beacon` | The centre, beside VictoriaMetrics and vmalert | The service (`serve`) and its command line: `check-config`, `config`, `token`, `enroll`, `machine`, `ssh`, `silence`, `drill`, `healthcheck` |
| `beacon-probe` | Each probe location: an edge node, a small VPS where users are | `agent` pushes results to the gateway; `serve` answers scrapes like the Blackbox Exporter; `check` validates a module file; `run` probes once |
| `beacon-oncall` | The machine where an agent runs | The reference harness: checks a hook's signature and starts the agent program you name, with the payload on its standard input |

## One process, three ports

`beacon serve` is one process holding the gateway, the router, the bridge, the REST API, the MCP server, the pages people open from a card, and the SSH channel.

| Port | For | What answers |
| --- | --- | --- |
| 17001 | People and agents | The REST API (`/api/v1`), MCP (`/mcp`), the pages (`/alerts/{id}`, `/sessions/{id}`), `/healthz`, `/readyz`, `/metrics` (direct connections only) and the drill target |
| 17002 | Agents, and machines behind NAT | The SSH channel: plain SSH, published directly rather than through the HTTP proxy |
| 17006 | Machines | The gateway: probes, hosts, heartbeats, the rule engine's alerts, change events, metric queries |

State is one bbolt file in the data directory: alert groups with their notes and agent calls, silences, tokens, machines, the index of SSH sessions, change events, the configuration with its history, and the audit log. Recordings of SSH sessions are files beside it, under `recordings/YYYY/MM/DD/`. Metrics and rules are not Beacon's: VictoriaMetrics stores and queries, vmalert evaluates. The command line reaches the running service through an admin socket in the data directory, so `token`, `enroll`, `machine`, `ssh`, `silence`, `config` and `drill` need no network access and no token.

A whole installation is four containers (Beacon, VictoriaMetrics, vmalert and Caddy in front for TLS) and one binary per probe machine.

## An alert's path

1. **Probes push.** Every minute, `beacon-probe agent` probes the targets the gateway handed it and pushes the samples in the Prometheus text format, with a queue on disk for when the network is down. Its token names its location; the gateway labels everything it writes with the location, region, network and host from the catalogue, so a probe cannot speak for another, and a location whose machine has expired cannot write at all.
2. **Rules decide.** vmalert evaluates the rules over VictoriaMetrics and posts what fires to the gateway's `POST /api/v2/alerts`, in Alertmanager's form, with the rules token. The reference rules call a service down only when at least two locations, and at least half of those reporting, fail it.
3. **The router groups.** Alertmanager's job, done inside Beacon: grouping, `group_wait` and `group_interval`, `resolve_timeout`, inhibition and silences. Groups survive restarts. An alert named `Watchdog` proves the pipeline works and is never shown.
4. **The bridge tells people.** A new group becomes a card in the alert chat; nobody acknowledging a critical one means a buzz in the Feishu app after 10 minutes and Pushover's emergency priority after 15. Low-severity alerts, and anything over the app's monthly budget, go to the chat's custom bot. While rules, routing and delivery all work, the bridge pings healthchecks.io every minute, so a dead centre still reaches a phone.
5. **The bridge wakes agents.** Hooks in the configuration (a signed webhook, or a GitHub workflow dispatch) are called once when a group is first notified, and again when it resolves if asked. The calls run beside delivery, which never waits for them.

## An agent's path

The agent, woken by a hook or asked by a person, works through the same token on every door:

- **REST and MCP** call the same bridge methods. `get_evidence` gathers in one answer each location's result and reason (from VictoriaMetrics), the alerts in the same failure domain, the change events just before, and what people and agents have done. `add_note`, `ack_alert` and `silence_alert` write; every write is in the audit log and echoed to the alert chat.
- **Probing on demand.** `probe_now` puts a request on a board that every probe long-polls through the gateway; the locations that probe the target do so at once, push the samples as in their cycle, and post their answers, which the call returns within a minute.
- **Machines** are reached through the SSH channel, below.
- **Its own token** renews itself before it expires (`renew_token`), so nobody hands an agent a long-lived one.

Text that comes from outside Beacon (labels, probed services' answers, change events, what machines print) is cleaned of control and invisible characters, cut when long, and marked `untrusted` in every answer that carries it.

## Reaching a machine

An agent signs in to the SSH channel with the key on its token; the user name names the machine. The channel checks that the token may reach it, connects to the machine with its pinned host key, and presents a certificate it signed for that connection, valid for five minutes; the machine trusts only Beacon's certificate authority. The agent's `ssh`, `scp`, `sftp`, `rsync` and `ssh -L` go through unchanged, and the MCP tools (`run_command`, `read_file`, `write_file`, `edit_file`) use the same path.

Every session is recorded as asciicast v3 for replay, an interactive shell is broken down into its commands and exit statuses through shell-integration marks, and the alert chat hears what ran. `ssh pause` closes every session at once and refuses new ones until it is resumed. A machine Beacon cannot reach connects out with plain `ssh -R` and a key registered for it; Beacon then reaches its sshd back through that connection.

## Configuration

The configuration file names everything else, and secrets only as `*_file` paths. What Beacon monitors (the catalogue of probe locations, heartbeats and expiring resources; the probe target sets; the probe modules) lives in the database and is changed through the API, MCP and the command line. Every change is checked as the files always were, takes effect at once, and is a version with who made it and the values before and after, which can be reverted. Files are for import and export: the first start imports them, `SIGHUP` imports them again when nothing was changed through Beacon meanwhile, and `config export` writes them back.

## Packages

| Package | What it is |
| --- | --- |
| `cmd/beacon` | The service and its command line; wires everything, and imports the configuration files at the first start and on `SIGHUP` |
| `cmd/beacon-probe` | The probe agent: push mode, pull mode, checks |
| `cmd/beacon-oncall`, `internal/oncall` | The reference harness on the agent's machine; `contrib/oncall` has the wrappers for Claude Code and Codex, the prompt and a systemd unit |
| `internal/gateway` | The machines' door: token scopes, labels from the catalogue, the write and import proxy to VictoriaMetrics, targets, modules and settings for probes, alerts from the rule engine, change events, the read proxy for `metrics:read` |
| `internal/router` | Grouping, timing, inhibition, silences and the Watchdog |
| `internal/bridge` | Alert groups to people (cards, acknowledgements, silences, escalation, budget, readiness, the healthchecks.io ping) and to agents (hooks, evidence, timeline, notes, change events, untrusted text cleaned) |
| `internal/probenow` | Probing on demand: the board of requests the gateway's long-poll serves and `probe_now` waits on |
| `internal/feishu`, `internal/notify` | The Feishu app (cards, long connection, buzz); Pushover, Bark and the group bot |
| `internal/api`, `internal/mcpserver` | REST (`openapi.yaml`) and MCP, both calling the same bridge and SSH channel methods |
| `internal/web` | The pages people open from a card, and the signed links that open them |
| `internal/sshgate` | The SSH channel: sign-in by the keys on tokens, certificates for machines, relaying sessions and forwarded ports, recordings and transcripts, the tools, pausing, echoes to the chat |
| `internal/store` | The bbolt database: groups, silences, tokens (hashed), machines, sessions, change events, the audit log |
| `internal/ctl` | The admin socket behind the command line |
| `internal/catalog`, `internal/targets`, `internal/configdb` | The catalogue, the target sets and the probe protocol's types; the configuration in the database, with versions, history and revert |
| `internal/probe`, `internal/hostmetrics`, `internal/agent` | The probers (Blackbox-compatible, plus container registries, Git over HTTP and RDAP) with failure reasons; host metrics from `/proc`; the agent's cycle and disk queue |
| `internal/inventory` | CDN nodes against their clusters' DNS rotations |
| `internal/config`, `internal/i18n`, `internal/metrics`, `internal/vm`, `internal/clientip`, `internal/version`, `internal/alertmanager` | Configuration and secrets; translations; the `/metrics` writer; VictoriaMetrics queries; client addresses; build information; Alertmanager's data formats |

## Rules every part keeps

- **Secrets come only from files** the configuration names, and are never logged or put in errors.
- **Tokens are stored as SHA-256 hashes**, with scopes and an expiry; failed attempts are limited per client address. A probe's or host's token writes only as its own subject.
- **Every operation is both a REST route and an MCP tool**, through the same method, with a scope check, an audit entry and an echo to the alert chat. Errors are `{"error", "code"}`, with codes that stay stable ([REST API](reference/api.md)).
- **The probe protocol only grows**: fields and routes are added, never changed or removed, so probes and gateways of different versions keep working together.
- **Close to how agents work**: SSH, the shell, HTTP with JSON, Prometheus queries and MCP, rather than a protocol or configuration language of Beacon's own.
- **No approval workflow and no model**: Beacon records and shows what agents do; deciding is the agent's, and the pause switch and each machine's access are the only brakes.
- **What people read is translated**: cards, toasts and chat messages are in English or Chinese; logs, command output and API errors stay English.
