# Changelog

User-facing changes, newest first. Versions follow [Semantic Versioning](https://semver.org); pre-releases (`-rc.N`) are listed under the version they lead to.

## Unreleased

- Standby CDN nodes: a location may be `standby: true` in its rotation; the rotation check labels nodes `role="primary"` or `"standby"`.
- Probe settings per location: `probe: {timeout: …}` lengthens the probes' timeout on one machine. The gateway serves an agent's settings on `GET /api/v1/agent`; agents ask for them and keep working with a gateway that does not have the route. Agents probe up to 16 targets at once (was 8).
- `docs/catalog.md`: the catalogue, targets and module file.

## 0.2.0

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
