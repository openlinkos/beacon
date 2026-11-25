# AGENTS.md

Instructions for AI coding agents working in this repository. `CLAUDE.md` is a symlink to this file; edit `AGENTS.md` only.

## Project

Beacon · 烽火 (`beacon` and `beacon-probe`) is self-hosted operations infrastructure for people and their AI agents. People can run the work themselves, delegate within their authority and review what happened. **Monitoring** checks from where users are, routes alerts to people and agents, and exposes evidence and public status pages. **SSH access** lets people use their own identities and agents reach machines without holding machine passwords or keys. **Identity and audit** connect accounts, scoped agents, SSH, REST, MCP and the console. An optional team store supports traceable use of service tokens for fixed HTTPS requests. Go; metrics and rules live in VictoriaMetrics and vmalert. Deployment configuration lives outside this public repository.

**Read [docs/about/goals.md](docs/about/goals.md) before you plan a change**, and say which goal it serves. The goals rank what matters when they pull apart, and list what Beacon will not do.

## Architecture

```
beacon-probe agent ──push (text format, gzip)──▶ gateway ──▶ VictoriaMetrics ◀── vmalert (rules)
  ▲ targets, modules, settings ◀── gateway                                          │ alerts (Alertmanager API v2)
                                                                                    ▼
people ◀── Feishu cards, Pushover ◀── bridge ◀── router (group, inhibit, silence) ◀── gateway ◀── CI, Flux (change events)
agents ◀── webhook, workflow dispatch ◀── bridge (a new alert wakes them)
agents ──▶ REST /api/v1, MCP /mcp ──▶ bridge (evidence, timeline, notes), store, SSH channel
agents ──▶ ssh, scp, sftp, rsync (port 17002) ──▶ SSH channel ──▶ machines (5-minute certificates; every session recorded)
```

One process, `beacon serve`, holds the gateway (port 17006), the router, the bridge, the API and MCP (port 17001) and the SSH channel (port 17002). State is one SQLite database (`beacon.db`, buckets of keys and values plus event tables; an older bbolt `status.db` is migrated into it on the first start); recordings of SSH sessions are files beside it. Ports 17001 and 17002 are for people and agents, 17006 for machines.

| Package | What it is |
| --- | --- |
| `cmd/beacon` | The service and its CLI: `serve`, `check-config`, `config`, `setup`, `user`, `agent`, `token`, `enroll`, `machine`, `ssh`, `drill`, `healthcheck`. Wires everything; imports the configuration files at the first start and on SIGHUP |
| `cmd/beacon-probe` | The probe agent: `agent` (push mode), `serve` (pull, like the Blackbox Exporter), `check`, `run` |
| `cmd/beacon-oncall`, `internal/oncall` | The reference harness on the agent's machine: checks a hook's signature, starts the agent program named on its command line with the payload on stdin; `contrib/oncall` has the wrappers and the unit |
| `internal/probenow` | Probing on demand: the board of requests the gateway's long-poll serves and `probe_now` waits on |
| `internal/gateway` | The machines' door: token scopes, labels from the catalogue, write/import proxy to VictoriaMetrics, targets/modules/settings for probes, alerts from the rule engine, change events from CI and Flux, read proxy for `metrics:read` |
| `internal/router` | Alertmanager's job: grouping, `group_wait`/`group_interval`, `resolve_timeout`, inhibition, silences, Watchdog; groups survive restarts |
| `internal/bridge` | Alert groups to people: cards, acknowledgements, silences, escalation, budget, readiness and the healthchecks.io ping; and to agents: hooks that wake them (`agents.go`), evidence, timeline, notes and change events, untrusted text cleaned (`untrusted.go`) |
| `internal/feishu`, `internal/notify` | The Feishu app (cards, long connection, buzz); Pushover, Bark and the group bot |
| `internal/api`, `internal/mcpserver` | REST (`openapi.yaml`) and MCP, both calling the same bridge and SSH channel methods, for whoever a credential resolves to (`store.Principal`) |
| `internal/oauth` | Beacon as the authorization server for its own `/mcp` (MCP's OAuth): the metadata, client registration and client ID metadata documents, the token and revocation endpoints; the consent page is the console's |
| `internal/status` | The public status pages: components, state and 90-day history from the probes' agreement, incidents from alert groups, maintenance windows |
| `internal/web`, `internal/web/link` | The console and the public status pages: sign-in with an account (and, for one release, a token), setup, invitations and resets, people, agents with their tokens, keys and OAuth sign-ins, the OAuth consent page, the overview, lists with facets (alerts, machines, sessions, tokens, configuration and its history, hooks, audit, changes) and the alert and session pages with the replay, on `@openlinkos/ui`'s workbench shell (vendored under `static/ol`, refreshed by `scripts/update-ui.sh`; asciinema-player under `static/`); and the signed links a card opens them with |
| `internal/sshgate` | The SSH channel: agents' SSH sign-in by their keys, what is open closed when access ends (Recheck), certificates for machines, relaying sessions and forwarded ports, recordings (asciicast v3) and transcripts (shell-integration marks), the tools (run, read, write, edit), pausing, echoes to the chat |
| `internal/kv`, `internal/events` | Under the store: buckets of keys and values on SQLite (a bbolt file is read only to migrate it); event tables with a hash chain, signed checkpoints and pruning |
| `internal/store` | On `kv` and `events`: groups (with notes and agent calls), silences, accounts (argon2id) and one-time links, agents and their presets, the principals every request resolves to, tokens (SHA-256, scopes, subjects, expiry, SSH keys, machines), OAuth clients and grants, machines, the index of SSH sessions, change events, audit log, drill |
| `internal/ctl` | The admin socket in the data directory, behind the CLI's `setup`, `user`, `agent`, `token`, `enroll`, `machine`, `ssh`, `config` and `drill` |
| `internal/catalog`, `internal/targets` | Locations, heartbeats, resources; probe target files and the probe protocol types (`Group`, `Settings`); the checks every change passes |
| `internal/configdb` | The catalogue, target sets and module file in the database: changes with versions, history and revert, import and export, the state the gateway reads |
| `internal/inventory` | CDN nodes against their clusters' DNS rotations |
| `internal/probe`, `internal/hostmetrics`, `internal/agent` | Probers (Blackbox-compatible plus registry, git, rdap) with failure reasons; host metrics from /proc; the agent's cycle and disk queue |
| `internal/config`, `internal/i18n`, `internal/metrics`, `internal/vm`, `internal/clientip`, `internal/version` | Configuration and secrets; translations; the `/metrics` writer; VictoriaMetrics queries; client addresses; build info |
| `internal/alertmanager` | Only Alertmanager's data formats, which the router and the rule engine still speak |

The catalogue, targets and module file are described in `docs/guide/monitoring.md`.

## Commands

```bash
go test ./...                  # unit tests; CI also runs -race and Windows
golangci-lint run ./...        # also with GOOS=linux: some files are Unix-only
go run ./cmd/beacon check-config -config config.example.yaml   # needs the secret files it names
```

## Rules

- **Use English** for code, comments, logs, CLI output, scripts and commit messages. British spelling, as in the docs.
- **What people read goes through `internal/i18n`**: card text, toasts and chat messages are English format strings passed to `i18n.T` or `(*Bridge).t`, with the Chinese in `internal/i18n/zh.go`, keyed by that string. `TestCatalogue` fails on a missing or unused translation, and `TestNoChinese` on Chinese anywhere outside the catalogue and `docs/zh/`. Logs, CLI output and API errors stay English.
- **API errors** are `{"error", "code"}`; codes are stable and listed in `internal/api/openapi.yaml`, which `TestOpenAPIInStep` keeps in step with the routes. Offer new programmatic operations through both REST and MCP, with the same implementation and scope checks. Record writes in audit and echo them to the alert chat. Interactive account and team credential custody forms require the person to sign in.
- **Deployment secrets** are read from `*_file` paths named in the configuration. The optional team store accepts values through the owner console and encrypts them; those values never appear in logs, errors, tool results or audit details. Beacon depends on no secret manager: how a deployment fills those files is its own business.
- **Security first**: tokens, OAuth tokens, one-time link codes and session ids are stored as SHA-256 only, passwords as argon2id; tokens have scopes and an expiry; an agent never does more than its owner's role allows, read afresh on every request; people sign in only with Beacon's own accounts; a probe or host token writes only as its own subject, and an expired machine cannot write; the rule engine's alerts need the rules token or `alerts:send`; `/metrics` answers direct connections only; only the on-call people may act on cards. Do not weaken a check to make a test pass.
- **The probe protocol** (`/api/v1/targets`, `/modules`, `/agent`, `/import/prometheus`) is shared by agents and gateways of different versions: add fields and routes, do not change or remove them, and keep agents working against an older gateway.
- **Probe agents stay small**: they run on CDN edge nodes with little memory to spare (about 15 MB resident today, 64 MB hard limit in deployments). No new dependencies in `cmd/beacon-probe` without measuring.
- **Close to how agents work**: offer what agents already know (SSH, the shell, HTTP with JSON, Prometheus queries, MCP) rather than a protocol or configuration language of our own. MCP tools take the names and behaviour of agents' own tools (run a command, read a file, edit a file); results are clean and structured, without terminal control codes, and long ones are cut with a way to fetch the rest.
- **No approval workflows**: Beacon records and shows what agents do; deciding what to run is the agent's job, done by its own harness. Do not add approval gates; the pause switch and each machine's access are the only brakes. Beacon calls no model.
- **Public repository, no internal information**: name machines by location and network, never by internal host names, vendors or real addresses; examples use `example.com`. No deployment state (how many machines or locations, which accounts exist, what is switched on), no business plans, prices or customer names. Such things live in the makers' private repositories.
- **Ask before adding dependencies**; keep `go.mod` tidy.
- **Docs**: English in `README.md` and `docs/`, Chinese in `README.zh-CN.md` and `docs/zh/`, section for section. Change both together. Record user-facing changes in `CHANGELOG.md`.

## Where it stands

The `v0.3.0-rc.44` candidate includes the shared console workflows, multi-location monitoring with editable probe scope, personal and delegated SSH, recorded sessions, incident outcomes, machine settings history, public status pages and the reference deployment. An alert links to its explicitly associated machines. Agent activity stays visible while Beacon waits for human ownership and makes any scheduled escalation. The reference deployment gives critical service outages a five-second initial group wait. Its installation drill verifies a signed wake through the released on-call harness and checks recovery from both probes. Signed notification webhooks and Slack posts send firing and recovery with persistent retry queues. The incident response area shows the current handover. It distinguishes hook submission, reported programme execution, human ownership and recovery. The on-call harness can report when a programme starts and how it exits using a credential scoped to that hook. The overview puts planned maintenance beside urgent alerts. The console previews and publishes component changes to an existing public status page, with a storage version check. Notifications open a specific incident, and stale card and console actions cannot change a later alert. Feishu chat retries failed initial delivery, route evidence follows its configured destination, and incident history preserves verified passing checks. Owners can create their first public status page in the console and review the incident notes and maintenance announcements that will become public. Owners can revoke outstanding alert and session share links. Shared alerts use the recipient's account for linked incidents and SSH replays. The console, REST and MCP apply the effective machine scope and current enrolment to session listing, details and recordings. The console shows the latest attributed note near incident response, and incident history retains hook delivery and programme reports. Episode-bound checks refuse changed groups. Pushover cancellation resumes after restart and uses the original emergency receipt. The console shows whether cancellation is pending, confirmed or past the retry window. The first-wake guide installs the pinned on-call harness. Incident history filters by severity, resolution time and retained evidence. Late hook results go to the original retained episode. The install guide checks the service account before the first drill. The session page distinguishes replay and transcript evidence. The channel checks local evidence storage before sending a machine operation, and sealed recordings publish to shared storage separately. The released on-call harness can check first-drill credentials and machine enrolment before a fault is injected. A person can add an SSH key and return to the same incident, and agents can search files on authorised machines through REST and MCP. Monitor pages show coverage for their current check assignments. The reference rules confirm a sustained outage quorum after 40 seconds, and the reference drill measures each stage of delivery. Feishu cards show completed SSH sessions as they become available within the card update budget. The next acceptance checks are in [the goals](docs/about/goals.md).

1. **Real delivery and handover** (goals 1 and 3). The reference deployment rehearses an HTTP fault, a failed agent run, local phone delivery and handover. Verify actual chat and phone recipients with the same incident on a phone and from the next shift's account.
2. **Team credential custody** (goal 6). Verify storage, clear delegation and revocation, and a recovery procedure before relying on it. Ordinary password-manager tasks such as imports and browser filling have their own acceptance criteria.
3. **More chat channels** (goal 9). Slack incoming webhooks send firing and recovery with a link to the incident. Telegram, DingTalk, WeCom and ntfy, and in-chat actions where the channel supports them, remain to do.
4. **Closer to agents' tools** (goal 4). Feishu cards refresh when sessions end, under the existing budget. File search is available through REST and MCP, and owners can revoke shared alert and session links. Background jobs remain to do.

## Git

- Conventional Commits in English: `<type>(<scope>): <summary>`, types `feat | fix | docs | refactor | test | perf | build | ci | chore`.
- Run the tests and the linter before committing. Do not add attribution trailers such as `Co-Authored-By`.
- Releases: tag `vX.Y.Z-rc.N` first (the release workflow builds, signs and verifies), test it, then tag `vX.Y.Z`. Move the `CHANGELOG.md` entries from Unreleased to the version.
