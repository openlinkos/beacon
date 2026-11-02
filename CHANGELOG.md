# Changelog

User-facing changes, newest first. Versions follow [Semantic Versioning](https://semver.org); pre-releases (`-rc.N`) are listed under the version they lead to.

## Unreleased

- **Machines behind NAT** (`docs/ssh.md`, Machines behind NAT): a machine Beacon cannot connect to connects out to the channel itself with plain OpenSSH (`ssh -N -R 0:localhost:22 NAME@beacon:17002`, a dedicated key, a systemd unit), and Beacon reaches its sshd through that connection; nothing is installed on the machine. Beacon tells a machine's connection from an agent's by the key: the connector key is registered on the machine, an agent's on its token. Such a connection may only ask for its one forward and keep alive; one per machine, the newest wins; Beacon binds no port and connects only to the address the machine itself forwarded, with the pinned host key and a certificate as always (its `source-address` also allows `127.0.0.1/32` and `::1/128` for such a machine, since its sshd sees the connection come from itself).
  - CLI: `machine add NAME connector -key KEY|@FILE [-host-key KEY|@FILE]` prints the whole machine-side setup (CA, account, `known_hosts` line for the channel, the connector's user and key, the unit); `machine connector NAME -key …` sets or changes the key; `machine list` shows whether it is connected and since when. The host key is given with `-host-key` or learned the first time the machine connects, audited (`machine_host_key`) and echoed to the chat.
  - `add_machine` and `POST /api/v1/machines` take `address: connector`, `connector_key` and `host_key` and return the setup as `connector`; new `set_machine_connector` and `PUT /api/v1/machines/{name}/connector`; machines carry `connector`, `connected`, `connected_since`. A machine whose connector is not connected fails fast with the new error code `machine.disconnected`.
  - Gauges `linkos_beacon_ssh_connectors` (connected) and `linkos_beacon_ssh_connector_machines` (added); connects, disconnects and replacements are logged; key changes are audited (`machine_connector`).

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
