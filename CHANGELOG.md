# Changelog

User-facing changes, newest first. Versions follow [Semantic Versioning](https://semver.org); pre-releases (`-rc.N`) are listed under the version they lead to.

## Unreleased

- **The site, beacon.openlinkos.com** (`docs/`): Beacon's home page and documentation, in English and Simplified Chinese, on the shared OpenLinkOS theme. The home page shows the three pillars and how Beacon fits; the guide keeps its pages (`docs/goals.md` and the rest stay where they were) and gains an architecture page, a reference for the REST API (routes, scopes, error codes) and the MCP tools, a page on the sources Beacon takes in, and this changelog. Headings take GitHub's ids, so links to them work on GitHub and on the site alike.
  - The build fails when a Chinese page falls behind its English page, or when the reference misses a route, an error code, a scope or an MCP tool that the code has. CI builds the site on every pull request; the `site` workflow publishes the image `ghcr.io/openlinkos/beacon-site` (nginx, port 8080, `linux/amd64` and `linux/arm64`) from `main`, tagged `<UTC time>-<commit>`.
  - A code of conduct (Contributor Covenant 2.1), issue forms, a pull request template, Dependabot for the site, and how to work on the site in `CONTRIBUTING.md`.

## 0.3.0

### rc.6 (2026-11-02)

- Tokens are 56 characters again, as before rc.5: the secret field that seemed to cut them did not.

### rc.5 (2026-11-02)

- Tokens are 32 characters (`lbt_` and 17 random bytes in base32, 136 bits), down from 56: bot platforms' secret fields cut long values. Tokens issued before keep working.


### rc.4 (2026-11-02)

- **Hooks for receivers with a secret of their own** (`docs/agents.md`): a `kind: webhook` hook takes `headers_file`, a file of `Name: value` lines sent with every call (`Authorization: Bearer …`, `X-Webhook-Secret: …`), for bot platforms that check their own secret rather than Beacon's signature. One of `secret_file` and `headers_file` is required; with both, the call is signed and carries the headers. `X-Beacon-Event` and `X-Beacon-Delivery` are always sent.

### rc.3 (2026-11-02)

- **The agent on call: `linkos-oncall`** (`docs/agents.md`, "Running an agent when woken"): a reference harness, a third binary in the releases, that runs an agent program when a hook wakes it. It takes the signed webhook, checks the signature, refuses replays and deliveries older than 15 minutes, remembers each delivery for a day, answers at once, and starts the program named with `-run` (`-run-resolved` for resolved events) with the payload on its standard input and the alert in `BEACON_*` variables. `-concurrency` programs run at once, up to 32 wait, beyond that Beacon is told to try later; each run's output is kept under `-log-dir` for `-keep`; a run is killed with its whole process group after `-timeout`; stopping waits for the runs in progress.
  - `contrib/oncall`: wrappers for Claude Code (headless, Beacon's MCP server with the token read from a file at each connection, Beacon's tools and nothing else, nobody to ask) and Codex (`codex exec`), the one-page prompt for the woken agent, and a systemd unit with its own user and a read-only system. `contrib/github/oncall.yml`: the same for a hook of `kind: github`, through the Claude Code GitHub Action. Beacon still calls no model: the harness decides nothing, the agent does, and Beacon records it.
- **Probe again, now** (`docs/agents.md`): `POST /api/v1/probe-now` and the MCP tool `probe_now` (`alerts:write`) have every location that probes a target probe it again at once and return each location's result (ok, reason, duration, HTTP status, attempts, certificate expiry, when), the locations that did not answer in time and a one-line summary; by `alert_id`, `target` (with a `module`) or `service`, waiting 45 seconds by default and 90 at most. The probes push the fresh samples as in their cycle, so alerts resolve sooner. Limits: one request per target every 15 seconds and three in flight per token (`probe.busy`); an unknown target is `probe.not_found`; without a gateway, `probe.unavailable`. Audited (`probe_now`) and echoed to the chat.
  - The probe protocol gains `GET /api/v1/agent/requests?wait=25s` (a long-poll) and `POST /api/v1/agent/requests/{id}` on the gateway. `linkos-probe agent` polls beside its cycle; against an older gateway it asks again in an hour. Older probes keep working and show up as missing.
  - Metrics `linkos_probe_now_requests_total`, `linkos_probe_now_answers_total{location}`, `linkos_probe_now_missing_total{location}` and `linkos_probe_now_requests_open`.
- **People's pages** (`docs/pages.md`): from the card, one link opens the alert's page, and from there each session's replay. Served by `linkos-beacon` beside the API at `public_url`; no login system.
  - `GET /alerts/{id}`: the alert, each location's latest result and reason, related alerts, the agents woken and whether the SSH channel is paused, the sessions on its machines with their commands and exit statuses (each with a **Replay** link), notes, changes, labels, and the timeline. `GET /sessions/{id}`: who, where, when, how long, the commands, and the recording replayed in the page (asciinema-player 3.17.0, embedded; asciicast v3). In the configured language, light and dark, laid out for a phone; a strict `Content-Security-Policy`, `noindex`, no referrer, nothing fetched from elsewhere.
  - Signed links: `?s=<expiry>.<HMAC-SHA256>` under a key Beacon makes at first start and keeps in its database; valid for `pages.link_ttl` (30 days by default). A bearer token with `status:read` (alerts) or `sessions:read`/`machines:ssh` (sessions) opens a page too; anything else is 404. Anyone holding a link sees that page, so the chat the links go to must be private.
  - The links are on the card (a **Timeline** button; an **Agents** block with the hooks woken and "N sessions on M machines, K commands"), in the chat echoes of agents' sessions (a **Replay** link each), and in the API and MCP answers (`page` on alerts and sessions, `links.page` in the evidence, `url` on a timeline's session events), so an agent can hand a person a link.
- **Machines behind NAT** (`docs/ssh.md`, Machines behind NAT): a machine Beacon cannot connect to connects out to the channel itself with plain OpenSSH (`ssh -N -R 0:localhost:22 NAME@beacon:17002`, a dedicated key, a systemd unit), and Beacon reaches its sshd through that connection; nothing is installed on the machine. Beacon tells a machine's connection from an agent's by the key: the connector key is registered on the machine, an agent's on its token. Such a connection may only ask for its one forward and keep alive; one per machine, the newest wins; Beacon binds no port and connects only to the address the machine itself forwarded, with the pinned host key and a certificate as always (its `source-address` also allows `127.0.0.1/32` and `::1/128` for such a machine, since its sshd sees the connection come from itself).
  - CLI: `machine add NAME connector -key KEY|@FILE [-host-key KEY|@FILE]` prints the whole machine-side setup (CA, account, `known_hosts` line for the channel, the connector's user and key, the unit); `machine connector NAME -key …` sets or changes the key; `machine list` shows whether it is connected and since when. The host key is given with `-host-key` or learned the first time the machine connects, audited (`machine_host_key`) and echoed to the chat.
  - `add_machine` and `POST /api/v1/machines` take `address: connector`, `connector_key` and `host_key` and return the setup as `connector`; new `set_machine_connector` and `PUT /api/v1/machines/{name}/connector`; machines carry `connector`, `connected`, `connected_since`. A machine whose connector is not connected fails fast with the new error code `machine.disconnected`.
  - Gauges `linkos_beacon_ssh_connectors` (connected) and `linkos_beacon_ssh_connector_machines` (added); connects, disconnects and replacements are logged; key changes are audited (`machine_connector`).
- **Agents' credentials and safe configuration** (goals 6 and 7; `docs/agents.md`, `docs/catalog.md`): tokens renew themselves, and `config:write` is safe to give an agent.
  - Tokens renew themselves: `POST /api/v1/token/renew` and the MCP tool `renew_token` (any scope) give the caller's token a new secret with the same name, scopes, subject, machines and SSH keys, expiring its lifetime from now; the old secret works for ten more minutes, so a client swaps its token file without a race. At most once every 24 hours (`token.too_soon`, 429); enrolled machines' tokens cannot (`token.not_renewable`, 403), `enroll -rotate` replaces them. Audited (`token_renew`) and echoed to the chat. `GET /api/v1/token` and `whoami` describe the caller's token: name, scopes, machines, expiry, lifetime, whether and when it may renew. Tokens keep their lifetime (`token list` shows it and the last renewal); tokens from before get `expires - created`. SSH sign-in is by key and is untouched by a renewal.
  - Probe modules read files from one directory: `password_file` names a file in the probe's secrets directory (`linkos-probe -secrets-dir`, `/etc/linkos-agent/secrets` by default), never a path. The probe refuses a path before reading anything (`probe_failure_info{reason="config"}`), and Beacon refuses it with `config.invalid` when a module is put or imported (API, MCP, CLI, files). Transition: a module file already in the database with such a value is still served, with the problem logged, until the module is put right; probes from before this release read the name relative to their working directory, so upgrade them before adding a module with `password_file`.
  - `if_version` on `PUT` and `DELETE` under `/api/v1/config` and on `put_config` and `delete_config`: the version the caller last saw; when a later one is in force the change is refused with `config.conflict`, naming it.
  - The gateway takes at most 60 change events a minute per token; the rest get `429` with `changes.too_many`, counted in `linkos_gateway_changes_limited_total{token}`.
  - MCP has two more tools (`whoami`, `renew_token`); new error codes `token.too_soon`, `token.not_renewable`, `changes.too_many`.
- **Release for others**: a reference deployment under `deploy/` (Compose with `linkos-beacon`, VictoriaMetrics, vmalert and Caddy, images pinned by digest; `install.sh` makes the secrets, puts your host names in, validates every file with its own image and starts it; generic alert rules with unit tests and a runbook per alert; the probe's systemd unit and installer), `docs/install.md` ("Up in 30 minutes"), `CONTRIBUTING.md` and `SECURITY.md`. A `deploy` workflow validates the deployment on every change.
  - CLI: `silence create -matchers 'alertname=X,service=Y' -duration 4h -comment TEXT` (or `-alert GROUP_ID`), `silence list`, `silence expire ID`, through the admin socket and the bridge's own methods: audited as `cli` and echoed to the alert chat like a silence made through the API or MCP; a firing group every alert of which matches is marked silenced and acknowledged.

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
