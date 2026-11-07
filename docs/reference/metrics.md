# Metrics and alert rules

This page lists Beacon's metrics, family by family, and every alert rule the reference deployment ships in `deploy/rules/`.

Everything Beacon judges by is a metric in VictoriaMetrics, and every alert is a vmalert rule evaluated over them ([Sources](sources.md)). Each rule names its runbook in `deploy/runbooks/`.

Write your own rules the same way: vmalert's format, posted to the gateway as to Alertmanager. Give them the labels `severity` and `service`, and the annotation `summary`.

## Where each metric comes from

- **The probes push** their results and each location's host metrics to the gateway, which labels them by the catalogue ([Sources](sources.md#probes)).
- **The centre exports** its own state on `/metrics`, on port 17001, for direct connections only. VictoriaMetrics scrapes it every minute, beside vmalert and VictoriaMetrics itself, as `deploy/metrics/scrape.yml` sets out.
- **The probe agent reports** about itself with every push, so a location's queue and push errors are visible per location.

## The centre's own metrics

On `/metrics` (job `beacon` in the reference scrape configuration):

| Metric | Labels | What it is |
| --- | --- | --- |
| `beacon_build_info` | `version`, `go` | The running build |
| `beacon_start_time_seconds` | | When the service started |
| `beacon_bridge_ready` | | 1 when alerts can reach people now |
| `beacon_alert_groups_firing` | `severity` | Alert groups firing, by severity |
| `beacon_alerts_firing` | | Firing alerts the router holds |
| `beacon_notifications_total` | `channel`, `result` | Notifications sent, by channel and result |
| `beacon_feishu_api_calls` | | Feishu app API calls this month, against the budget |
| `beacon_gateway_requests_total` | `route`, `code` | Requests to the metrics gateway by route and status |
| `beacon_gateway_changes_limited_total` | | Change events refused because the token posted more than 60 in a minute |
| `beacon_probe_now_requests_open` | | Probe-on-demand requests waiting for locations to answer |
| `beacon_probe_now_requests_total` | | Probe-on-demand requests made by agents |
| `beacon_probe_now_answers_total` | `location` | Answers from locations to a probe-on-demand request |
| `beacon_probe_now_missing_total` | `location` | Locations that did not answer one in time |
| `beacon_expected_location` | `location`, `region`, `network` | 1 for every location that should report now: gone silent is a rule away |
| `beacon_heartbeat_max_age_seconds` | `heartbeat`, `severity`, `machine` | How old a heartbeat may get before it alerts, from the catalogue |
| `beacon_resource_expiry_timestamp_seconds` | `resource`, `kind` | When a server, domain, certificate or token expires, from the catalogue |
| `beacon_rotation_resolve_success` | `rotation` | Whether a CDN cluster's DNS name resolved |
| `beacon_rotation_members` | `rotation` | How many addresses a CDN cluster's DNS name resolved to |
| `beacon_location_in_rotation` | `location`, `rotation`, `role` | Whether a location's CDN node is among its cluster's DNS answers |
| `beacon_rotation_unknown_address` | `rotation`, `address` | An answer that belongs to no location in the catalogue |
| `beacon_token_expiry_timestamp_seconds` | `token`, `subject` | When an API or gateway token expires |
| `go_goroutines`, `go_memstats_heap_inuse_bytes`, `go_memstats_sys_bytes`, `process_resident_memory_bytes` | | The process |

## The probes' results

The probes use the Blackbox Exporter's metric names, so rules written for either work: `probe_success`, `probe_duration_seconds`, `probe_http_status_code`, `probe_ssl_earliest_cert_expiry`. There are two additions: `probe_failure_info{reason}` for why a check failed, and `beacon_probe_attempts` for how many tries it took.

The gateway labels every result with `location`, `region`, `network` and `host` from the catalogue. The target's own labels (`service`, `severity`, `failure_domain`, `module`) reach the rules and the cards.

Hosts report as `node_*` metrics from `/proc`: `node_cpu_seconds_total`, `node_memory_*_bytes`, `node_disk_*_total`, `node_network_*_total`, `node_time_seconds`. A heartbeat is one sample: `beacon_heartbeat_last_success_timestamp_seconds{heartbeat}`.

## The probe agent about itself

The probe agent sends these with every push, so they are per location:

- `beacon_probe_build_info`
- `beacon_probe_targets`
- `beacon_probe_queue_batches` and `beacon_probe_queue_bytes`: what waits on disk for the gateway
- `beacon_probe_push_errors_total`
- `beacon_probe_last_push_timestamp_seconds`

## The alert rules

The reference rules are four files, and each group says which metric it watches. Recording rules in `deploy/rules/probes.yml` (the `beacon:` series) carry the intermediate steps, and the alerts build on them. There, a location counts as failing once it has failed for 3 minutes, and a service's availability over a day is `avg_over_time(beacon:service_up[1d])`.

### Service availability

`deploy/rules/probes.yml`, over `probe_success{job="probe"}`. One location's word is never an outage. A service is down when at least two locations fail it, and they are at least half of those reporting.

| Alert | Severity | For | When |
| --- | --- | --- | --- |
| `ServiceDown` | the target's `severity`; `critical` without one | 1m | The service has failed for 3 minutes from at least half of the locations reporting it (and at least two) |
| `ServiceDegraded` | `warning` | 5m | At least two locations fail it, but fewer than half: a partial outage, usually one CDN node or one network path |
| `MainlandPathDegraded` | `warning` | 5m | Every mainland location fails it while the overseas ones succeed: the path from mainland China, not the service |
| `ProbeLocationDown` | `warning` | 5m | A location has sent no results for 10 minutes (`beacon_expected_location` against `probe_success`) |

### Things that run out

`deploy/rules/expiry.yml`, over the catalogue's exports, the probes' certificates and the heartbeats' samples.

| Alert | Severity | For | When |
| --- | --- | --- | --- |
| `ResourceExpiring` | `warning`, then `critical` | — | A resource in the catalogue expires within 30 days; critical within 7 |
| `CertificateExpiring` | `warning`, then `critical` | — | The earliest certificate expiry any location sees is within 14 days; critical within 7 (Let's Encrypt renews 30 days ahead, so renewal has failed twice over) |
| `HeartbeatStale` | the heartbeat's own `severity` | — | A job has not reported success within its `max_age` |
| `HeartbeatMissing` | the heartbeat's own `severity` | 1d | A heartbeat in the catalogue has never reported (a new one gets a day to succeed once) |

### DNS rotations

`deploy/rules/cdn.yml`, over `beacon_location_in_rotation` and its sisters, for CDN nodes that name an address and a rotation.

| Alert | Severity | For | When |
| --- | --- | --- | --- |
| `CDNNodeOutOfRotation` | `warning` | 15m | A primary node is up but its cluster's DNS does not answer with it: it serves nobody |
| `CDNStandbyInRotation` | `info` | 5m | A standby node is serving: a primary failed or was taken out |
| `CDNRotationUnknownAddress` | `warning` | 30m | The rotation answers with an address no location in the catalogue claims: a node nobody watches |
| `CDNRotationResolveFailing` | `warning` | 10m | The rotation's DNS name does not resolve from the monitoring host |

### The monitoring itself

`deploy/rules/meta.yml`, over the scrape of the centre, vmalert and VictoriaMetrics (`deploy/metrics/scrape.yml`).

"The monitoring is down" is not a rule here. The Watchdog always fires. While Beacon receives it and can deliver alerts, Beacon pings healthchecks.io every minute. When the whole machine stops, the pings stop, and healthchecks.io raises the alarm from outside.

| Alert | Severity | For | When |
| --- | --- | --- | --- |
| `Watchdog` | `none` (never shown) | — | Always firing while the pipeline works; Beacon counts it towards its readiness |
| `VMAlertRuleErrors` | `warning` | 5m | vmalert fails to evaluate a rule group; alerts in it may not fire |
| `VictoriaMetricsDiskLow` | `warning` | 10m | Below 5 GB free on the data volume; below its minimum VictoriaMetrics stops accepting data |
| `GatewayErrors` | `warning` | 10m | The gateway answered a route with server errors for 10 minutes, usually VictoriaMetrics refusing writes |
| `ProbeQueueBacklog` | `warning` | 15m | A location's agent has over 8 MB queued on disk; at 64 MB it drops the oldest batches |
