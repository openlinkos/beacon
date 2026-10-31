# AGENTS.md

Instructions for AI coding agents working in this repository. `CLAUDE.md` is a symlink to this file; edit `AGENTS.md` only.

## Project

Beacon · 烽火 (`linkos-beacon`, and the probe agent `linkos-probe`) tells whether services work for their users, probed from where the users are, and closes the loop with the people on call and their AI agents: a gateway for machines (enrolled tokens, labels set by the server, targets handed out), alert routing in place of Alertmanager (grouping, inhibition, silences), Feishu app cards they can acknowledge or silence from, escalation to their phones through Pushover, a dead man's switch on healthchecks.io, and the same operations through a REST API and a built-in MCP server. Public status pages follow. Go; one service and one probe binary; metrics and rules live in VictoriaMetrics and vmalert. LinkOS runs its own status service (LinkOS Status) on Beacon, configured in a private ops repository.

## Architecture

```
linkos-probe agent ──push (text format, gzip)──▶ gateway ──▶ VictoriaMetrics ◀── vmalert (rules)
  ▲ targets, modules, settings ◀── gateway                                          │ alerts (Alertmanager API v2)
                                                                                    ▼
people ◀── Feishu cards, Pushover ◀── bridge ◀── router (group, inhibit, silence) ◀── gateway
agents ──▶ REST /api/v1, MCP /mcp ──▶ bridge, store
```

One process, `linkos-beacon serve`, holds the gateway (port 17006), the router, the bridge, the API and MCP (port 17001). State is one bbolt file. Port 17001 is for people and agents, 17006 for machines.

| Package | What it is |
| --- | --- |
| `cmd/linkos-beacon` | The service and its CLI: `serve`, `check-config`, `token`, `enroll`, `drill`, `healthcheck`. Wires everything; reloads catalogue, targets and modules on SIGHUP |
| `cmd/linkos-probe` | The probe agent: `agent` (push mode), `serve` (pull, like the Blackbox Exporter), `check`, `run` |
| `internal/gateway` | The machines' door: token scopes, labels from the catalogue, write/import proxy to VictoriaMetrics, targets/modules/settings for probes, alerts from the rule engine, read proxy for `metrics:read` |
| `internal/router` | Alertmanager's job: grouping, `group_wait`/`group_interval`, `resolve_timeout`, inhibition, silences, Watchdog; groups survive restarts |
| `internal/bridge` | Alert groups to people: cards, acknowledgements, silences, escalation, budget, readiness and the healthchecks.io ping |
| `internal/feishu`, `internal/notify` | The Feishu app (cards, long connection, buzz); Pushover, Bark and the group bot |
| `internal/api`, `internal/mcpserver` | REST (`openapi.yaml`) and MCP, both calling the same bridge methods |
| `internal/store` | bbolt: groups, silences, tokens (SHA-256, scopes, subjects, expiry), audit log, drill |
| `internal/ctl` | The admin socket in the data directory, behind the CLI's `token`, `enroll` and `drill` |
| `internal/catalog`, `internal/targets` | Locations, heartbeats, resources; probe target files and the probe protocol types (`Group`, `Settings`) |
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
- **Public repository**: name machines by location and network, never by internal host names, vendors or real addresses. No business plans, prices or customer names.
- **Ask before adding dependencies**; keep `go.mod` tidy.
- **Docs**: English in `README.md` and `docs/`, Chinese in `README.zh-CN.md` and `docs/zh/`, section for section. Change both together. Record user-facing changes in `CHANGELOG.md`.

## Where it stands

`v0.2.0-rc` runs LinkOS's monitoring: one centre, nine probe locations, alerting recorded but not yet sent until the Feishu app, Pushover and healthchecks.io are set up. Next, roughly in this order:

1. **Evidence for agents**: an MCP tool and API to query metrics (through the gateway's read path), the probe results and failure reasons behind an alert, change events (deployments, Flux), runbooks over MCP, and notes an agent leaves on a card. Treat everything fetched from probed targets as untrusted text.
2. **Status pages**: components from the catalogue and targets, 90-day history, incidents and maintenance (with silences), Atom feed, badges, public JSON; the design system and i18n as elsewhere.
3. **More channels**: Slack, Telegram, DingTalk, WeCom, ntfy, a generic webhook.
4. **Operations**: a `silence` CLI on the admin socket; `CONTRIBUTING.md` and `SECURITY.md`; a reference deployment (compose) in `docs/`.
5. **Fewer moving parts** (to be decided): rule evaluation inside Beacon instead of vmalert, optional embedded storage instead of a separate VictoriaMetrics, optional built-in TLS (ACME) instead of a reverse proxy.

## Git

- Conventional Commits in English: `<type>(<scope>): <summary>`, types `feat | fix | docs | refactor | test | perf | build | ci | chore`.
- Run the tests and the linter before committing. Do not add attribution trailers such as `Co-Authored-By`.
- Releases: tag `vX.Y.Z-rc.N` first (the release workflow builds, signs and verifies), test it, then tag `vX.Y.Z`. Move the `CHANGELOG.md` entries from Unreleased to the version.
