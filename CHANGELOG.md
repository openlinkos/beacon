# Changelog

User-facing changes, newest first. Versions follow [Semantic Versioning](https://semver.org); pre-releases (`-rc.N`) are listed under the version they lead to.

## Unreleased

- **Agents' credentials and safe configuration** (goals 6 and 7; `docs/agents.md`, `docs/catalog.md`): tokens renew themselves, and `config:write` is safe to give an agent.
  - Tokens renew themselves: `POST /api/v1/token/renew` and the MCP tool `renew_token` (any scope) give the caller's token a new secret with the same name, scopes, subject, machines and SSH keys, expiring its lifetime from now; the old secret works for ten more minutes, so a client swaps its token file without a race. At most once every 24 hours (`token.too_soon`, 429); enrolled machines' tokens cannot (`token.not_renewable`, 403), `enroll -rotate` replaces them. Audited (`token_renew`) and echoed to the chat. `GET /api/v1/token` and `whoami` describe the caller's token: name, scopes, machines, expiry, lifetime, whether and when it may renew. Tokens keep their lifetime (`token list` shows it and the last renewal); tokens from before get `expires - created`. SSH sign-in is by key and is untouched by a renewal.
  - Probe modules read files from one directory: `password_file` names a file in the probe's secrets directory (`linkos-probe -secrets-dir`, `/etc/linkos-agent/secrets` by default), never a path. The probe refuses a path before reading anything (`probe_failure_info{reason="config"}`), and Beacon refuses it with `config.invalid` when a module is put or imported (API, MCP, CLI, files). Transition: a module file already in the database with such a value is still served, with the problem logged, until the module is put right; probes from before this release read the name relative to their working directory, so upgrade them before adding a module with `password_file`.
  - `if_version` on `PUT` and `DELETE` under `/api/v1/config` and on `put_config` and `delete_config`: the version the caller last saw; when a later one is in force the change is refused with `config.conflict`, naming it.
  - The gateway takes at most 60 change events a minute per token; the rest get `429` with `changes.too_many`, counted in `linkos_gateway_changes_limited_total{token}`.
  - MCP has two more tools (`whoami`, `renew_token`); new error codes `token.too_soon`, `token.not_renewable`, `changes.too_many`.

## 0.3.0

### rc.2 (2026-11-02)

- **Waking agents, and evidence** (`docs/agents.md`): a new alert can wake an agent, which then finds what it needs in one call. Beacon calls no model.
  - `agents:` in the configuration: hooks called when a new alert group is first notified (and, with `resolved: true`, when it resolves), chosen by severity and a `service` regular expression. `kind: webhook` POSTs a compact JSON payload signed with HMAC-SHA256 (`X-Beacon-Signature: sha256=…`, the secret from `secret_file`); `kind: github` dispatches a workflow with the inputs `alert` and `event`. Calls never hold up alert delivery, are retried with backoff (five tries in about five minutes), and their outcome is kept on the group (`agents` in the API), in the audit log (`wake_agent`), on the card and in the chat. `public_url` is required with agents.
  - Evidence: `GET /api/v1/alerts/{id}/evidence` and the MCP tool `get_evidence`: per probe location the result, since when it fails and why, duration, HTTP status, attempts and certificate expiry from VictoriaMetrics; the alerts firing in the same failure domain; the change events around it; what was done (acknowledgement, silences, notes, agents woken, sessions on its machines); the runbook and the dashboard. Text from outside Beacon is cleaned of control and invisible characters, cut, and listed under `untrusted`.
  - Notes: `POST /api/v1/alerts/{id}/notes` and `add_note` (`alerts:write`) leave a note on an alert; the card shows the latest three, and each is audited and echoed to the chat.
  - Timeline: `GET /api/v1/alerts/{id}/timeline` and `get_timeline`: started, notified, buzzed, Pushover, acknowledged, notes, silences, agents woken, changes, sessions and resolved, in time order.
  - Change events: the gateway takes `POST /api/v1/changes` (Beacon's own form) and `POST /api/v1/changes/flux` (Flux's `generic` provider) with the new scope `changes:write`; they are kept for `changes.keep` (30 days by default) and listed by `GET /api/v1/changes` and `list_changes`.
- **Configured through Beacon** (`docs/catalog.md`): the catalogue (probe locations, heartbeats, resources), the probe targets (in named sets that keep the file names' `all-`, `overseas-`, `mainland-` and `<location>-` prefixes) and the module file live in the database. Changes are checked as the files were and take effect at once, without `SIGHUP`; probes get the same answers byte for byte.
  - Every change is a version with who made it, when, through what (`api`, `mcp`, `cli`), a note, and every item's value before and after; it is audited and echoed to the alert chat. A change can be reverted unless a later one touched the same items.
  - REST under `/api/v1/config` (`locations`, `heartbeats`, `resources`, `targets`, `modules`, each item in the shape of its file; `history`, `history/{version}`, `history/{version}/revert`) and MCP tools `get_config`, `put_config`, `delete_config`, `list_config_changes`, `get_config_change`, `revert_config_change`. New scope `config:write`; reads take `status:read`. New error codes `config.invalid`, `config.not_found`, `config.conflict`.
  - CLI: `config import DIR [-dry-run]`, `config export DIR` (the layout `catalog/*.yaml`, `targets/*.yaml`, `targets/modules.yml`; the round trip keeps the content, not the comments of the catalogue and target files), `config history`, `config revert VERSION`.
  - The first start imports the files `catalog_dir`, `gateway.targets_dir` and `gateway.modules_file` name into a database that holds no configuration yet. After that, `SIGHUP` imports them only when nothing was changed through Beacon since the last import, and says why not otherwise. `check-config` validates the files and the configuration in the database.

### rc.1 (2026-11-01)

- Goals: Beacon is infrastructure for AI agents that run operations: it sees (monitoring), reaches (an SSH channel) and connects (one platform for people and agents). See `docs/goals.md`.
- **The SSH channel** (`docs/ssh.md`), on port 17002 when `ssh.ca_key_file` is set: agents reach machines with their own `ssh`, `scp`, `sftp`, `rsync` and `ssh -L`, the user name naming the machine, and never hold a key. Beacon signs a certificate valid for five minutes for each connection; machines trust only its certificate authority.
  - Every session is recorded (asciicast v3, kept 180 days by default); an interactive bash is broken into its commands with their exit statuses; the alert chat hears what agents did, batched per agent and machine.
  - MCP tools and REST routes: `list_machines` (with an `ssh_config` for the agent's own ssh), `run_command`, `read_file`, `write_file`, `edit_file`, `list_sessions`, `get_session`, `add_machine`, `remove_machine`, `pause_ssh`, `resume_ssh`.
  - CLI: `machine add|list|remove|ca`, `ssh pause|resume|status`, `token create … -ssh-key KEY -machines a,b`, `token ssh-key`, `token machines`.
  - New scopes `machines:ssh`, `machines:write`, `sessions:read`. Beacon does not review commands; the pause switch and each token's machines are the brakes.

## 0.2.0

### rc.4 (2026-10-31)

- Probe retries per location: `probe: {retries: 1}` tries a probe again at once when it fails without an answer (DNS, connect, timeout), for a machine whose connections drop now and then. Every result now comes with `linkos_probe_attempts`.

### rc.3 (2026-10-31)

- Standby CDN nodes: a location may be `standby: true` in its rotation; the rotation check labels nodes `role="primary"` or `"standby"`.
- Probe settings per location: `probe: {timeout: …}` lengthens the probes' timeout on one machine. The gateway serves an agent's settings on `GET /api/v1/agent`; agents ask for them and keep working with a gateway that does not have the route. Agents probe up to 16 targets at once (was 8).
- `docs/catalog.md`: the catalogue, targets and module file.

### rc.2 (2026-10-31)

The project is renamed Beacon · 烽火 (was LinkOS Status, `openlinkos/status`), and grows from an alert bridge into a monitoring service.

- **Renamed**: module `github.com/openlinkos/beacon`, binaries `linkos-beacon` and `linkos-probe`, image `ghcr.io/openlinkos/beacon`, paths `/etc/linkos-beacon` and `/var/lib/linkos-beacon`, metric prefix `linkos_beacon_`, token prefix `lbt_`. The image's health command is `linkos-beacon healthcheck`.
- **Gateway** for machines (port 17006): per-machine tokens from `linkos-beacon enroll probe|host|heartbeat NAME`, labels set from the catalogue, expired machines refused, targets and modules handed to probes, alerts from the rule engine and from `alerts:send` tokens, metric queries for `metrics:read`.
- **Router** in place of Alertmanager: grouping, inhibition, silences and the Watchdog inside the service. Rule engines post to `/api/v2/alerts` as they would to Alertmanager. The `alertmanager` configuration section and `/hooks/alertmanager` are gone; `rules.token_file` and `routing` replace them.
- **`linkos-probe agent`**: one process per probe machine that fetches its targets and modules, probes them with failure reasons, reads host metrics and heartbeats, and pushes everything with a disk queue. About 15 MB resident.
- **CDN rotation check**: CDN nodes against their clusters' DNS answers.
- The catalogue links a location to its machine by name: an expired `vps` of the same name ends the location.
- rc.1 was never published: its release pointed at the old repository name.

## 0.1.0

### rc.1 (2026-10-30)

First pre-release, as LinkOS Status: the alert bridge between Alertmanager and the people on call (Feishu cards with acknowledge and silence, escalation to the Feishu app and Pushover, the group bot as fallback, budget control, the healthchecks.io ping), the REST API and the MCP server, the catalogue's metrics, and drills.
