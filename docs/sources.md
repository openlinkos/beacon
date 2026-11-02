# Sources

Beacon judges with what is sent to it. This page lists each source: who sends, where to, with which token and in what format. Machines and tools send to the gateway on port 17006, each with a token of its own, so that nothing can speak for anything else; people and agents use port 17001. The routes are listed in the [REST API](reference/api.md#the-gateway) reference.

| Source | Route | Token | Format |
| --- | --- | --- | --- |
| Probes | `POST /api/v1/import/prometheus` | `probe:write`, its location | Prometheus text format, gzip |
| Hosts | `POST /api/v1/import/prometheus`, `POST /api/v1/write` | `host:write`, its host | Text format, or Prometheus remote write |
| Heartbeats | `POST /api/v1/import/prometheus` | `heartbeat:write` | One sample per job |
| The rule engine | `POST /api/v2/alerts` | The rules token, or `alerts:send` | Alertmanager's API v2 |
| CI and scripts | `POST /api/v1/changes` | `changes:write` | Beacon's change event, JSON |
| Flux | `POST /api/v1/changes/flux` | `changes:write` | Flux's `generic` provider |
| The chat | The Feishu long connection, or `POST /hooks/feishu` | The app's credentials | Feishu card actions |
| People and agents | `/api/v1`, `/mcp`, the SSH channel | Their own token | JSON, MCP, SSH |

## Probes

`beacon-probe agent` runs at each probe location with a token from `beacon enroll probe <location>`. Every minute it fetches what to do from the gateway (`GET /api/v1/targets`, `GET /api/v1/modules` and `GET /api/v1/agent`, the location's settings), probes, and pushes the results in the Prometheus text format, gzipped, keeping a queue on disk for when the network is down. It keeps one long-poll open beside its cycle for [probing on demand](agents.md#probe-again-now).

The gateway labels everything a probe writes with `location`, `region`, `network` and `host` from the catalogue, in place of any the probe sent itself; a location that is not in the catalogue, or whose machine has expired, is refused with `location.inactive`. The results use the Blackbox Exporter's metric names (`probe_success`, `probe_duration_seconds`, `probe_http_status_code`, `probe_ssl_earliest_cert_expiry`, …), with `probe_failure_info{reason}` for why a check failed and `beacon_probe_attempts` for how many tries it took. A probe also pushes a few host metrics from `/proc`, and the heartbeats written to its textfile directory.

`beacon-probe serve` answers `/probe` and `/metrics` instead, like the Blackbox Exporter, for a scraper of your own.

## Hosts

A machine that is not a probe location reports as a host, with a token from `beacon enroll host <name>`: the text format to `POST /api/v1/import/prometheus`, or Prometheus remote write to `POST /api/v1/write`. The gateway labels what it writes with `host`.

## Heartbeats

A job that must report success at least every `max_age` (a backup, a scheduled task) pushes one sample when it succeeds, with a token from `beacon enroll heartbeat <name>`:

```bash
curl -fsS -H "Authorization: Bearer $TOKEN" --data-binary \
  "beacon_heartbeat_last_success_timestamp_seconds{heartbeat=\"nas-backup\"} $(date +%s)" \
  https://metrics.example.com/api/v1/import/prometheus
```

On a machine with the probe agent, the job writes the same line into the agent's textfile directory instead. The heartbeat itself, its `max_age` and severity, is in the catalogue ([Heartbeats](catalog.md#heartbeats)).

## The rule engine

vmalert evaluates the rules over VictoriaMetrics and posts what fires to the gateway as it would to Alertmanager:

```bash
vmalert -notifier.url=http://beacon:17006 -notifier.bearerTokenFile=/etc/vmalert/rules-token   # the file rules.token_file names
```

Alerts need the labels `severity` (`critical`, `warning` or `info`) and `service`, and the annotation `summary`; `description`, `runbook` and `dashboard` are used when present. An alert named `Watchdog`, always firing, proves the pipeline works and is never shown. Another sender may post alerts the same way with an `alerts:send` token.

## Change events

Deployments and configuration changes reach Beacon as change events, so the evidence for an alert says what changed just before it. A CI job or a script posts Beacon's own JSON event to `POST /api/v1/changes`; Flux's notification-controller posts its events through a Provider of type `generic` to `POST /api/v1/changes/flux`. Both need a `changes:write` token, at most 60 events a minute. The fields and the Flux setup are in [Change events](agents.md#change-events). Change events never page anyone.

## The chat

People act on the cards in the alert chat: acknowledge with a note, silence for 1, 4 or 24 hours. Feishu hands the card actions to Beacon over the long connection the service keeps (`feishu.callback: websocket`), or by `POST /hooks/feishu` on port 17001 (`feishu.callback: http`), verified with the app's token. Only the people listed in `feishu.on_call` may act. An emergency notification acknowledged in Pushover acknowledges the alert in Beacon too: Beacon follows the notification's receipt. [Setting up the Feishu app](feishu.md) has the app's side.

## People and agents

Notes, acknowledgements, silences, configuration changes and sessions on machines come in through the REST API, MCP and the SSH channel, each with the token of whoever acts. Every write is in the audit log and echoed to the alert chat. See the [REST API](reference/api.md), the [MCP tools](reference/mcp.md) and [The SSH channel](ssh.md).
