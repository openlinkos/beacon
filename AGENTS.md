# AGENTS.md

Instructions for AI coding agents working in this repository. `CLAUDE.md` is a symlink to this file; edit `AGENTS.md` only.

## Project

Beacon · 烽火 (`linkos-beacon`, and the probe agent `linkos-probe`) is infrastructure for AI agents that run operations, in three parts. **Sees**: monitoring that tells whether services work for their users, probed from where the users are, with a gateway for machines (enrolled tokens, labels set by the server, targets handed out), alert routing in place of Alertmanager, Feishu app cards, escalation to phones through Pushover and a dead man's switch on healthchecks.io. **Reaches**: an SSH channel through which agents get to machines with the tools they know (ssh, scp, sftp, rsync, port forwarding, MCP tools) without holding a key, every session recorded. **Connects**: one platform for people and agents, with scoped tokens, a REST API and a built-in MCP server, an audit log echoed to the alert chat, and later status pages. Go; one service and one probe binary; metrics and rules live in VictoriaMetrics and vmalert. Beacon is one piece of infrastructure for the AI era, the piece where agents run operations; its makers run their own operations on it, configured outside this repository.

**Read [docs/goals.md](docs/goals.md) before you plan a change**, and say which goal it serves. The goals rank what matters when they pull apart, and list what Beacon will not do.

## Architecture

```
linkos-probe agent ──push (text format, gzip)──▶ gateway ──▶ VictoriaMetrics ◀── vmalert (rules)
  ▲ targets, modules, settings ◀── gateway                                          │ alerts (Alertmanager API v2)
                                                                                    ▼
people ◀── Feishu cards, Pushover ◀── bridge ◀── router (group, inhibit, silence) ◀── gateway ◀── CI, Flux (change events)
agents ◀── webhook, workflow dispatch ◀── bridge (a new alert wakes them)
agents ──▶ REST /api/v1, MCP /mcp ──▶ bridge (evidence, timeline, notes), store, SSH channel
agents ──▶ ssh, scp, sftp, rsync (port 17002) ──▶ SSH channel ──▶ machines (5-minute certificates; every session recorded)
```

One process, `linkos-beacon serve`, holds the gateway (port 17006), the router, the bridge, the API and MCP (port 17001) and the SSH channel (port 17002). State is one bbolt file; recordings of SSH sessions are files beside it. Ports 17001 and 17002 are for people and agents, 17006 for machines.

| Package | What it is |
| --- | --- |
| `cmd/linkos-beacon` | The service and its CLI: `serve`, `check-config`, `config`, `token`, `enroll`, `machine`, `ssh`, `drill`, `healthcheck`. Wires everything; imports the configuration files at the first start and on SIGHUP |
| `cmd/linkos-probe` | The probe agent: `agent` (push mode), `serve` (pull, like the Blackbox Exporter), `check`, `run` |
| `cmd/linkos-oncall`, `internal/oncall` | The reference harness on the agent's machine: checks a hook's signature, starts the agent program named on its command line with the payload on stdin; `contrib/oncall` has the wrappers and the unit |
| `internal/probenow` | Probing on demand: the board of requests the gateway's long-poll serves and `probe_now` waits on |
| `internal/gateway` | The machines' door: token scopes, labels from the catalogue, write/import proxy to VictoriaMetrics, targets/modules/settings for probes, alerts from the rule engine, change events from CI and Flux, read proxy for `metrics:read` |
| `internal/router` | Alertmanager's job: grouping, `group_wait`/`group_interval`, `resolve_timeout`, inhibition, silences, Watchdog; groups survive restarts |
| `internal/bridge` | Alert groups to people: cards, acknowledgements, silences, escalation, budget, readiness and the healthchecks.io ping; and to agents: hooks that wake them (`agents.go`), evidence, timeline, notes and change events, untrusted text cleaned (`untrusted.go`) |
| `internal/feishu`, `internal/notify` | The Feishu app (cards, long connection, buzz); Pushover, Bark and the group bot |
| `internal/api`, `internal/mcpserver` | REST (`openapi.yaml`) and MCP, both calling the same bridge and SSH channel methods |
| `internal/sshgate` | The SSH channel: agents' SSH sign-in by the keys on their tokens, certificates for machines, relaying sessions and forwarded ports, recordings (asciicast v3) and transcripts (shell-integration marks), the tools (run, read, write, edit), pausing, echoes to the chat |
| `internal/store` | bbolt: groups (with notes and agent calls), silences, tokens (SHA-256, scopes, subjects, expiry, SSH keys, machines), machines, the index of SSH sessions, change events, audit log, drill |
| `internal/ctl` | The admin socket in the data directory, behind the CLI's `token`, `enroll`, `machine`, `ssh`, `config` and `drill` |
| `internal/catalog`, `internal/targets` | Locations, heartbeats, resources; probe target files and the probe protocol types (`Group`, `Settings`); the checks every change passes |
| `internal/configdb` | The catalogue, target sets and module file in the database: changes with versions, history and revert, import and export, the state the gateway reads |
| `internal/inventory` | CDN nodes against their clusters' DNS rotations |
| `internal/probe`, `internal/hostmetrics`, `internal/agent` | Probers (Blackbox-compatible plus registry, git, rdap) with failure reasons; host metrics from /proc; the agent's cycle and disk queue |
| `internal/config`, `internal/i18n`, `internal/metrics`, `internal/vm`, `internal/clientip`, `internal/version` | Configuration and secrets; translations; the `/metrics` writer; VictoriaMetrics queries; client addresses; build info |
| `internal/alertmanager` | Only Alertmanager's data formats, which the router and the rule engine still speak |

The catalogue, targets and module file are described in `docs/catalog.md`.

## Commands

```bash
go test ./...                  # unit tests; CI also runs -race and Windows
golangci-lint run ./...        # also with GOOS=linux: some files are Unix-only
go run ./cmd/linkos-beacon check-config -config config.example.yaml   # needs the secret files it names
```

## Rules

- **Use English** for code, comments, logs, CLI output, scripts and commit messages. British spelling, as in the docs.
- **What people read goes through `internal/i18n`**: card text, toasts and chat messages are English format strings passed to `i18n.T` or `(*Bridge).t`, with the Chinese in `internal/i18n/zh.go`, keyed by that string. `TestCatalogue` fails on a missing or unused translation, and `TestNoChinese` on Chinese anywhere outside the catalogue and `docs/zh/`. Logs, CLI output and API errors stay English.
- **API errors** are `{"error", "code"}`; codes are stable and listed in `internal/api/openapi.yaml`, which `TestOpenAPIInStep` keeps in step with the routes. Every new operation is offered both as a REST route and as an MCP tool, through the same bridge method, with a scope check, an audit entry and an echo to the alert chat.
- **Secrets** are only ever read from `*_file` paths named in the configuration; never log them, never put them in errors. Beacon depends on no secret manager: how a deployment fills those files is its own business.
- **Security first**: tokens are stored as SHA-256 only, with scopes and an expiry; a probe or host token writes only as its own subject, and an expired machine cannot write; the rule engine's alerts need the rules token or `alerts:send`; `/metrics` answers direct connections only; only the on-call people may act on cards. Do not weaken a check to make a test pass.
- **The probe protocol** (`/api/v1/targets`, `/modules`, `/agent`, `/import/prometheus`) is shared by agents and gateways of different versions: add fields and routes, do not change or remove them, and keep agents working against an older gateway.
- **Probe agents stay small**: they run on CDN edge nodes with little memory to spare (about 15 MB resident today, 64 MB hard limit in deployments). No new dependencies in `cmd/linkos-probe` without measuring.
- **Close to how agents work**: offer what agents already know (SSH, the shell, HTTP with JSON, Prometheus queries, MCP) rather than a protocol or configuration language of our own. MCP tools take the names and behaviour of agents' own tools (run a command, read a file, edit a file); results are clean and structured, without terminal control codes, and long ones are cut with a way to fetch the rest.
- **No approval workflows**: Beacon records and shows what agents do; deciding what to run is the agent's job, done by its own harness. Do not add approval gates; the pause switch and each machine's access are the only brakes. Beacon calls no model.
- **Public repository, no internal information**: name machines by location and network, never by internal host names, vendors or real addresses; examples use `example.com`. No deployment state (how many machines or locations, which accounts exist, what is switched on), no business plans, prices or customer names. Such things live in the makers' private repositories.
- **Ask before adding dependencies**; keep `go.mod` tidy.
- **Docs**: English in `README.md` and `docs/`, Chinese in `README.zh-CN.md` and `docs/zh/`, section for section. Change both together. Record user-facing changes in `CHANGELOG.md`.

## Where it stands

`v0.3.0-rc.2` runs in production for its makers: monitoring from several locations, the SSH channel with enrolled machines, agents on the MCP; alert delivery and agent hooks are in the code and wait on each deployment's accounts. The north star and what "landed" means are in `docs/goals.md`. What remains runs as tracks side by side, each a loop that can be exercised on its own:

1. **The agent on call** (closure; goals 1, 3): `linkos-oncall` (`cmd/linkos-oncall`), a reference harness that verifies a hook's signature and starts the agent program you name, with examples for Claude Code and a GitHub workflow; `probe_now`, so an agent can have every location probe a target again at once and see each location's result (the probe protocol gains a route, nothing changes); then the first end-to-end drill on LinkOS, and alert delivery turned on once the accounts exist.
2. **People's pages** (goal 3): the alert's timeline and each session's replay as HTML served by Beacon, opened from the card through signed links; the card's agent block. No login system: signed links and tokens.
3. **Machines behind NAT** (goal 2): a machine connects out to the channel (plain `ssh -R` with a key enrolled for it) and Beacon reaches its sshd through that connection.
4. **Agents' credentials and safe configuration** (goals 6, 7): tokens renew themselves before they expire, so nobody hands out long-lived ones; `config:write` made safe to give agents (probe modules read password files only under one directory; a version as precondition on a change); change events limited per token.
5. **Release for others** (goals 8, 9): a reference deployment under `deploy/`, generic rules, an install guide, `CONTRIBUTING.md`, `SECURITY.md`, a `silence` CLI.
6. Later: status pages; more chat channels (Slack, Telegram, DingTalk, WeCom, ntfy, a generic webhook); search and background jobs as MCP tools; machines' changes versioned; rule evaluation inside Beacon; optional built-in TLS.

## Git

- Conventional Commits in English: `<type>(<scope>): <summary>`, types `feat | fix | docs | refactor | test | perf | build | ci | chore`.
- Run the tests and the linter before committing. Do not add attribution trailers such as `Co-Authored-By`.
- Releases: tag `vX.Y.Z-rc.N` first (the release workflow builds, signs and verifies), test it, then tag `vX.Y.Z`. Move the `CHANGELOG.md` entries from Unreleased to the version.
