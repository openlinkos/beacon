# Changelog

User-facing changes, newest first. Versions follow [Semantic Versioning](https://semver.org); pre-releases (`-rc.N`) are listed under the version they lead to.

## Unreleased

## 0.3.0

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
