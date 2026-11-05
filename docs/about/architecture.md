# Architecture

This page is for people who run Beacon and want to know which part does what, so that when an alert does not arrive or an agent cannot reach a machine they know where to look; and for contributors. It answers, in the order things happen: what runs where, where state lives, the path an alert takes from a probe to people and agents, the path an agent takes to the evidence and to a machine, where the configuration lives, which package holds which part, and the rules every part keeps. [Goals](goals.md) says why Beacon is built this way; `AGENTS.md` in the repository has the rules for changing the code.

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

Read it as three flows. Results go up: each probe pushes to the gateway, which writes to VictoriaMetrics. Alerts come back: vmalert evaluates the rules and posts what fires to the gateway, the router groups it, and the bridge tells people and wakes agents. Agents come in: over REST and MCP for the evidence and the notes, and over the SSH channel to the machines.

## What runs where

Three binaries, all in Go without cgo:

| Binary | Where it runs | What it does |
| --- | --- | --- |
| `beacon` | The centre (the machine Beacon runs on), beside VictoriaMetrics and vmalert | The service (`serve`) and its command line: `check-config`, `config`, `setup`, `user`, `agent`, `token`, `enroll`, `machine`, `ssh`, `silence`, `drill`, `healthcheck` |
| `beacon-probe` | Each probe location: an edge node, a small VPS where users are | `agent` pushes results to the gateway; `serve` answers scrapes like the Blackbox Exporter; `check` validates a module file; `run` probes once |
| `beacon-oncall` | The machine where an agent runs | The reference harness: checks a hook's signature and starts the agent program you name, with the payload on its standard input |

The reference deployment (`deploy/`) is four containers on the centre: Beacon (memory limit 256 MB), VictoriaMetrics (1 GB), vmalert (256 MB) and Caddy in front for TLS (128 MB); the whole installation uses about 200 MB when idle. Each probe machine runs one `beacon-probe`, about 15 to 20 MB resident, under a systemd limit of 64 MB.

## One process, three ports

`beacon serve` is one process. It holds the gateway, the router, the bridge, the REST API, the MCP server, the console and the public pages, and the SSH channel, and listens on three ports:

| Port | For | What answers |
| --- | --- | --- |
| 17001 | People and agents | The REST API (`/api/v1`), MCP (`/mcp`) and its OAuth (`/.well-known/…`, `/oauth/…`), the public pages (`/`, `/status`), the console (`/console`, with the pages a card links to: `/console/alerts/{id}`, `/console/sessions/{id}`), `/healthz`, `/readyz`, `/metrics` (direct connections only) and the drill target |
| 17002 | Agents, and machines behind NAT | The SSH channel: plain SSH, published directly rather than through the HTTP proxy |
| 17006 | Machines | The gateway: probes, hosts, heartbeats, the rule engine's alerts, change events, metric queries |

In the reference deployment Caddy serves 17001 as `https://status.example.com` and 17006 as `https://metrics.example.com`; 17002 is published as it is.

## Where state lives

- **One SQLite database**, `beacon.db` in the data directory (`data_dir`, `/var/lib/beacon`; `storage.path`), written in WAL mode (so beside it are its `-wal` and `-shm` files): alert groups with their notes and agent calls, silences, people's accounts (passwords as argon2id hashes), their agents, tokens and OAuth sign-ins (as SHA-256 hashes), machines, the index of SSH sessions, change events, the configuration with its history, the console's sign-ins, kept as keys and values in the buckets they always had; and the audit log, an event table with a hash chain and signed checkpoints. A centre that ran an earlier version moves its bbolt file, `status.db`, into it on the first start and keeps the old file as `status.db.pre-sql`.
- **Recordings** of SSH sessions, as files beside it under `recordings/YYYY/MM/DD/`, kept for `ssh.keep` (180 days by default).
- **The admin socket**, `ctl.sock` in the same directory, mode 0600. The command line reaches the running service through it, so `setup`, `user`, `agent`, `token`, `enroll`, `machine`, `ssh`, `silence`, `config` and `drill` need no network and no token; only the service's own user and root may connect. Machines' tokens are made there, and the setup link that makes the first account.
- **Metrics and rules are not Beacon's**: VictoriaMetrics stores and queries the probes' results (400 days in the reference deployment), and vmalert evaluates the rules.

## An alert's path

From a service breaking to people and agents hearing of it, with the reference rules and the default settings:

1. **Probes push.** Every minute, `beacon-probe agent` probes the targets the gateway handed it and pushes the samples in the Prometheus text format, keeping a queue on disk for when the network is down. Its token names its location; the gateway labels everything it writes with the location, region, network and host from the catalogue, so a probe cannot speak for another, and a location whose machine has expired cannot write at all (`location.inactive`).
2. **Rules decide.** vmalert evaluates the rules every 30 seconds. A location counts as failing a service once it has failed it for 3 minutes; the service is down when at least two locations fail it and they are at least half of those reporting, and `ServiceDown` fires after that has held for 1 minute. vmalert posts it to the gateway's `POST /api/v2/alerts`, in Alertmanager's form, with the rules token. Counting from the first failed probe, that is about 4 to 5 minutes.
3. **The router groups.** It does Alertmanager's job inside Beacon: alerts are grouped (by `alertname` and `service` unless a route says otherwise), a new group waits `group_wait` (30 seconds) for more alerts before it is sent, and is then updated at most every `group_interval` (2 minutes); inhibition and silences mute what they match. An alert fires until the `endsAt` the rule engine gives it, which vmalert sets in the past when the alert resolves; one sent without `endsAt` resolves `resolve_timeout` (5 minutes) after it was last sent. Groups survive restarts. The alert named `Watchdog`, always firing, proves the pipeline works and is never shown.
4. **The bridge tells people.** A new group becomes a card in the alert chat, through the Feishu app. A critical one nobody acknowledges is buzzed in the Feishu app after 10 minutes and sent to Pushover's emergency priority after 15; an acknowledgement holds escalation for `ack_ttl` (4 hours). `info` alerts go to the chat's custom bot, which costs no API calls; above 70% of the app's monthly budget only critical alerts use the app. While the Watchdog keeps arriving and alerts can be delivered, the bridge pings healthchecks.io every minute; when the centre or any part of the pipeline stops, the pings stop and healthchecks.io raises the alarm from outside.
5. **The bridge wakes agents.** The hooks in the configuration (a signed webhook, or a GitHub workflow dispatch) are called once when a group is first notified, and again when it resolves if the hook asks for that (`resolved`, 1 minute later by default). The calls run beside delivery, which never waits for them, and are tried again after 5 seconds, 20 seconds, 1 minute and 3 minutes.

## An agent's path

An agent acts for a person, its owner, and never does more than its owner's role allows, read afresh on every request. Woken by a hook or asked by a person, it signs in once, through the browser with MCP's OAuth or with a token, and that identity opens every door:

- **REST and MCP** call the same bridge methods. `get_evidence` gathers in one answer each location's result and reason (from VictoriaMetrics), the other alerts in the same failure domain, the change events just before, and what people and agents have done, over a window that opens 30 minutes before the alert started. `add_note`, `ack_alert` and `silence_alert` write; every write is in the audit log and echoed to the alert chat.
- **Probing on demand.** `probe_now` puts a request on a board in memory that every probe long-polls through the gateway (up to 55 seconds a poll). The locations that probe the target do so at once, push the samples as in their cycle, and post their answers, which the call returns within its wait (45 seconds by default, at most 90).
- **Machines** are reached through the SSH channel (next section).
- **Its credentials** look after themselves: a token renews itself before it expires (`renew_token`, at most once a day), and an OAuth client refreshes its own access token, so nobody has to hand an agent a long-lived one.

Text that comes from outside Beacon (labels, probed services' answers, change events, what machines print) is cleaned of control and invisible characters, cut when long, and named in an `untrusted` object in every answer that carries it.

## Reaching a machine

An agent signs in to the SSH channel with its own key, registered with Beacon; the user name it gives is the machine it wants. The channel checks that the agent may use `machines:ssh` and reaches that machine, connects to the machine with its pinned host key, and presents a certificate it signed for that one connection: valid for five minutes, for the machine's account (`beacon` by default), with a key ID that names the connection, the agent and the way in (`beacon 20261101-091724-aa8bba for alice / claude-code via ssh`), and limited to `ssh.source_addresses` when that is set. The machine trusts only Beacon's certificate authority. The agent's `ssh`, `scp`, `sftp`, `rsync` and `ssh -L` go through unchanged, and the MCP tools (`run_command`, `read_file`, `write_file`, `edit_file`) take the same path.

Every session is recorded as asciicast v3 for replay; an interactive shell is broken down into its commands and their exit statuses through shell-integration marks, and the alert chat hears what ran. `beacon ssh pause` closes every session at once and refuses new ones until `ssh resume`; when one agent's access ends (a credential revoked or expired, the agent deleted or narrowed, its owner disabled or demoted), what that agent has open closes at once. A machine Beacon cannot reach connects out with plain `ssh -R` and a key registered for it, and Beacon reaches its sshd back through that connection.

## Configuration

- **The configuration file** (`/etc/beacon/config.yaml`) names everything else: ports, VictoriaMetrics, the chat, hooks, the SSH channel. It names secrets only as `*_file` paths, and is read when the service starts.
- **What Beacon monitors** lives in the database: the catalogue (probe locations, heartbeats, expiring resources, status pages), the probe target sets and the probe modules. It is changed through the API, MCP and the command line; every change is checked as the files always were, takes effect at once, and is a version with who made it and the values before and after, which can be reverted. The latest 10,000 changes are kept.
- **Files are for import and export.** The first start imports them into a database that holds no configuration yet; `SIGHUP` imports them again only when nothing was changed through Beacon since the last import; `beacon config import DIR` imports a directory at any time, and `config export DIR` writes the configuration back as files.

## Packages

| Package | What it is |
| --- | --- |
| `cmd/beacon` | The service and its command line; wires everything, and imports the configuration files at the first start and on `SIGHUP` |
| `cmd/beacon-probe` | The probe agent: `agent` (push mode), `serve` (pull mode, like the Blackbox Exporter), `check`, `run` |
| `cmd/beacon-oncall`, `internal/oncall` | The reference harness on the agent's machine: checks a hook's signature and starts the program named on its command line with the payload on standard input; `contrib/oncall` has the wrappers for Claude Code and Codex, the prompt and a systemd unit |
| `internal/server` | The routes on port 17001: health and readiness, `/metrics` for direct connections, Feishu's HTTP callbacks, the drill target, the REST API, MCP and the pages |
| `internal/gateway` | The machines' door: token scopes, labels from the catalogue, the write and import proxy to VictoriaMetrics, targets, modules and settings for probes, alerts from the rule engine, change events from CI and Flux, the read proxy for `metrics:read` |
| `internal/probenow` | Probing on demand: the board of requests that the gateway's long-poll serves and `probe_now` waits on |
| `internal/router` | Alertmanager's job: grouping, `group_wait` and `group_interval`, `resolve_timeout`, inhibition, silences and the Watchdog; groups survive restarts |
| `internal/bridge` | Alert groups to people (cards, acknowledgements, silences, escalation, the Feishu budget, readiness, the healthchecks.io ping) and to agents (the hooks that wake them, evidence, timeline, notes, change events, untrusted text cleaned) |
| `internal/feishu`, `internal/notify` | The Feishu app (cards, the long connection, buzz); Pushover, Bark and the group's custom bot |
| `internal/api`, `internal/mcpserver` | REST (`openapi.yaml`) and MCP, both calling the same bridge and SSH channel methods, for whoever a credential resolves to: a person, a person's agent, or a token of its own |
| `internal/oauth` | Beacon as the authorization server for its own `/mcp`: the metadata, client registration and client ID metadata documents, the token and revocation endpoints; the consent page is the console's |
| `internal/status` | The public status pages: components, their state now and 90 days of history from the probes' agreement, incidents from alert groups, maintenance windows |
| `internal/web`, `internal/web/link` | The console (with sign-in, setup, invitations and resets, people, agents and the OAuth consent page) and the public status pages, on the workbench shell of `@openlinkos/ui` (vendored under `static/ol`, refreshed by `scripts/update-ui.sh`; asciinema-player under `static/`), and the signed links a card opens them with |
| `internal/sshgate` | The SSH channel: agents' sign-in by their keys, what is open closed when access ends, certificates for machines, relaying sessions and forwarded ports, recordings (asciicast v3) and transcripts, the tools (run, read, write, edit), connectors for machines behind NAT, pausing, echoes to the chat |
| `internal/kv`, `internal/events` | Under the store: buckets of keys and values on SQLite (and a bbolt file read once, to migrate it); event tables with a hash chain, signed checkpoints and pruning that keeps the chain verifiable |
| `internal/store` | The state on top of them: groups (with notes and agent calls), silences, accounts and one-time links, agents and their presets, tokens (hashed, with scopes, subjects, expiry, SSH keys and machines), OAuth clients and grants, machines, the index of SSH sessions, change events, console sign-ins, the audit log, the drill |
| `internal/ctl` | The admin socket in the data directory, behind the command line's `setup`, `user`, `agent`, `token`, `enroll`, `machine`, `ssh`, `silence`, `config` and `drill` |
| `internal/catalog`, `internal/targets` | Locations, heartbeats, resources and status pages; the target files and the probe protocol's types; the checks every change passes |
| `internal/configdb` | The catalogue, target sets and module file in the database: changes with versions, history and revert, import and export, the state the gateway reads |
| `internal/probe`, `internal/hostmetrics`, `internal/agent` | The probers (Blackbox-compatible, plus container registries, Git over HTTP and RDAP) with failure reasons; host metrics from `/proc`; the agent's cycle and disk queue |
| `internal/inventory` | CDN nodes against their clusters' DNS rotations |
| `internal/config`, `internal/i18n`, `internal/metrics`, `internal/vm`, `internal/clientip`, `internal/version` | Configuration and secrets; translations; the `/metrics` writer; VictoriaMetrics queries; client addresses; build information |
| `internal/alertmanager` | Only Alertmanager's data formats, which the rule engine sends and the router speaks |
| `internal/testutil` | A bridge with fake collaborators, for the tests of the packages above it |

## Rules every part keeps

- **Secrets come only from files** the configuration names, and are never logged or put in errors.
- **Tokens are stored as SHA-256 hashes**, with scopes and an expiry, as are OAuth tokens, one-time link codes and console session ids; passwords as argon2id hashes. 20 failed attempts from one client address within 10 minutes block that address for the rest of those 10 minutes (one count for the API, the console's sign-in and the gateway; the SSH channel keeps its own, with the same limit), and 10 wrong passwords block a username. A probe's or host's token writes only as its own subject.
- **People sign in only with Beacon's own accounts, and an agent never does more than its owner's role allows**, read afresh on every request.
- **Every operation is both a REST route and an MCP tool**, through the same method, with a scope check, an audit entry and an echo to the alert chat. Errors are `{"error", "code"}`, with codes that stay stable ([REST API](../reference/api.md)).
- **The probe protocol only grows**: fields and routes are added, never changed or removed, so probes and gateways of different versions keep working together.
- **Close to how agents work**: SSH, the shell, HTTP with JSON, Prometheus queries and MCP, rather than a protocol or configuration language of Beacon's own.
- **No approval workflow and no model**: Beacon records and shows what agents do; deciding is the agent's, and the pause switch and each machine's access are the only brakes.
- **What people read is translated**: cards, toasts and chat messages are in English or Chinese; logs, command output and API errors stay English.
