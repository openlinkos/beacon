# Sources

Beacon judges with what is sent to it. This page lists each source: who sends, where to, with which token and in what format.

Machines and tools send to the gateway on port 17006, each with a token of its own, so that nothing can speak for anything else. People and agents use port 17001. The routes are listed in the [REST API](api.md#the-gateway) reference.

The gateway sets `job` on every sample a machine writes to the kind of its token: `probe`, `host` or `heartbeat`, whatever the machine sent. The reference rules count probe results only from `job="probe"`, and the catalogue's metrics only from Beacon's own scrape, `job="beacon"`. One import carries at most 200,000 samples in 64 MiB of text; a larger one is refused with `import.too_large`. The gateway reads every sample before VictoriaMetrics does; [What the gateway passes](#what-the-gateway-passes) says which it drops.

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

`beacon-probe agent` runs at each probe location with a token from `beacon enroll probe <location>`. Every minute it:

1. fetches what to do from the gateway: `GET /api/v1/targets`, `GET /api/v1/modules` and `GET /api/v1/agent`, the location's settings;
2. probes;
3. pushes the results in the Prometheus text format, gzipped.

It keeps a queue on disk for when the network is down. Beside its cycle, it keeps one long-poll open for [probing on demand](../guide/on-call.md#probe-again-now).

The gateway labels everything a probe writes with `location`, `region`, `network` and `host` from the catalogue, and `job="probe"`, in place of any the probe sent itself. A location that is not in the catalogue, or whose machine has expired, is refused with `location.inactive`.

The results use the Blackbox Exporter's metric names (`probe_success`, `probe_duration_seconds`, `probe_http_status_code`, `probe_ssl_earliest_cert_expiry`, …). Two more say why a check failed (`probe_failure_info{reason}`) and how many tries it took (`beacon_probe_attempts`), as [Metrics](metrics.md#the-probes-results) lists. A probe also pushes a few host metrics from `/proc`, and the heartbeats written to its textfile directory.

`beacon-probe serve` answers `/probe` and `/metrics` instead, like the Blackbox Exporter, for a scraper of your own.

## Hosts

A machine that is not a probe location reports as a host, with a token from `beacon enroll host <name>`: the text format to `POST /api/v1/import/prometheus`, or Prometheus remote write to `POST /api/v1/write`. The gateway labels what it writes with `host` and `job="host"`. The `job` a host's own scraper sets is replaced, so series from its scrape jobs stay apart by `instance`.

Remote write is Prometheus' protocol, version 1, compressed with snappy, as Prometheus and vmagent send it. The gateway reads each request, at most 32 MiB once decompressed, and passes on the series it lets through, written again. VictoriaMetrics' own protocol (zstd) is refused with `write.encoding` (415), on which vmagent goes back to Prometheus' protocol.

## Heartbeats

A job that must report success at least every `max_age` (a backup, a scheduled task) pushes one sample when it succeeds, with a token from `beacon enroll heartbeat <name>`:

```bash
curl -fsS -H "Authorization: Bearer $TOKEN" --data-binary \
  "beacon_heartbeat_last_success_timestamp_seconds{heartbeat=\"nas-backup\"} $(date +%s)" \
  https://metrics.example.com/api/v1/import/prometheus
```

The token writes that heartbeat and nothing else. The gateway labels the sample with `heartbeat`, the name the token was enrolled for, and `job="heartbeat"`, whatever the sample says. It refuses any other metric with `heartbeat.metric`, more than 100 samples at once with `import.too_large`, and a heartbeat token that names no heartbeat with `heartbeat.unnamed`. Each heartbeat needs its own token.

On a machine with the probe agent, the job writes the same line into the agent's textfile directory instead. That sample counts only for a heartbeat the catalogue gives that machine: its `machine` is the probe's location, or the name the host's token was enrolled for. A heartbeat that names no machine is written by its own heartbeat token alone. The heartbeat itself, its `max_age` and severity, is in the catalogue ([Heartbeats](catalogue.md#heartbeats)).

The agent sends from the textfile directory only lines of its own: it leaves out probe results (`probe_…`) and Beacon's metrics (`beacon_…`) other than the heartbeat, which the agent alone reports.

## What the gateway passes

The gateway reads each sample a probe, a host or a heartbeat sends before VictoriaMetrics does, and passes it only when it says plainly what it is:

- **The plain text format.** A line is `name{label="value",…} value [timestamp]`, with the metric's name before the braces and plain label names. VictoriaMetrics also reads a name quoted inside the braces, or a `__name__` label in place of the name before them; the gateway passes neither. A line longer than 64 KiB is not a sample.
- **Each label once.** A sample or a remote-write series that gives a label twice is not passed.
- **A heartbeat for its machine.** A probe or a host writes `beacon_heartbeat_last_success_timestamp_seconds` only for a heartbeat whose `machine` in the catalogue is that probe's location or that host.

From a probe or a host, what is not passed is dropped, the rest of the import goes on, and `beacon_gateway_samples_dropped_total` counts it by machine (`subject`) and `reason`: `syntax`, `name_label`, `labels` or `heartbeat`. The first of each is logged. A heartbeat token's import with such a line is refused whole: `heartbeat.metric` for another metric or a `__name__` label, `bad_input` otherwise.

## The rule engine

vmalert evaluates the rules over VictoriaMetrics and posts what fires to the gateway as it would to Alertmanager:

```bash
vmalert -notifier.url=http://beacon:17006 -notifier.bearerTokenFile=/etc/vmalert/rules-token   # the file rules.token_file names
```

Alerts need the labels `severity` (`critical`, `warning` or `info`) and `service`, and the annotation `summary`. `description`, `runbook` and `dashboard` are used when present. An alert named `Watchdog`, always firing, proves the pipeline works and is never shown.

Another sender may post alerts the same way with an `alerts:send` token. Its alerts are its own. Beacon labels them `sender` with the token's name, whatever they say, and groups them apart from the rule engine's. They cannot resolve or change the rule engine's alerts or another sender's, and they inhibit only that sender's own. Their `runbook` and `dashboard` annotations do not replace the configured links. Only the rule engine's `Watchdog` counts, and only the rule engine sends alerts of severity `none`. A token's are dropped and counted in `beacon_gateway_alerts_dropped_total`. A token posts at most 120 times a minute; beyond that it gets `429` and `alerts.too_many`. The rule engine is never limited.

The rules the reference deployment ships are listed in [Metrics and alert rules](metrics.md#the-alert-rules).

## Change events

Deployments and configuration changes reach Beacon as change events, so the evidence for an alert says what changed just before it. Change events never page anyone.

- A CI job or a script posts Beacon's own JSON event to `POST /api/v1/changes`.
- Flux's notification-controller posts its events through a Provider of type `generic` to `POST /api/v1/changes/flux`.

Both need a `changes:write` token, at most 60 events a minute. The fields and the Flux setup are in [Deployments and Flux](../integrations/change-events.md).

## The chat

People act on the cards in the alert chat: acknowledge with a note, or silence for 1, 4 or 24 hours. Only the people listed in `feishu.on_call` may act.

Feishu hands the card actions to Beacon over the long connection the service keeps (`feishu.callback: websocket`). Or it uses `POST /hooks/feishu` on port 17001 (`feishu.callback: http`), encrypted and signed with the app's Encrypt Key, each callback taken once and within 5 minutes of its timestamp. [Set up the Feishu app](../integrations/feishu.md) has the app's side.

An emergency notification acknowledged in Pushover acknowledges the alert in Beacon too: Beacon follows the notification's receipt.

## People and agents

Notes, acknowledgements, silences, configuration changes and sessions on machines come in through the REST API, MCP and the SSH channel, each with the token of whoever acts. Every write is in the audit log and echoed to the alert chat. See the [REST API](api.md), the [MCP tools](mcp.md) and [Give agents a machine](../guide/machines.md).
