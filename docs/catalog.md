# Catalogue, targets and modules

English | [简体中文](zh/catalog.md)

Beacon reads three kinds of files besides its configuration. All of them are reloaded on `SIGHUP`, and `linkos-beacon check-config` validates them together with the configuration.

| What | Where (configuration key) | Read by |
| --- | --- | --- |
| Catalogue: probe locations, heartbeats, expiring resources | `catalog_dir`, every `*.yaml` | Beacon, which exports it as metrics for the rules and uses it to label what probes write |
| Probe targets | `gateway.targets_dir`, every `*.yaml` | The gateway, which hands each probe its own list |
| Probe modules | `gateway.modules_file` (default `<targets_dir>/modules.yml`) | The gateway, which hands it to every probe |

A catalogue may be split over several files; their lists are joined.

## Locations

A probe location is a machine running `linkos-probe agent`. Its token comes from `linkos-beacon enroll probe <id>`, and the gateway labels everything it writes with `location`, `region`, `network` and `host`, so a probe cannot speak for another.

```yaml
locations:
  - id: edge-la-1                      # internal: metrics, cards, configuration; the machine's name
    name: United States - Los Angeles  # public, for the status page: where and on which network, never which vendor
    region: overseas                   # overseas or mainland
    network: CN2 GIA
    address: edge-la-1.example.net     # CDN nodes: the node's address (a name or an IP) ...
    rotation: cdn.example.com          # ... and its cluster's DNS name
    standby: false                     # true: the node joins the rotation only when another fails
    probe:
      timeout: 30s                     # every probe here gets at least this long (at most 1m)
      retries: 1                       # a probe that fails without an answer is tried again at once (at most 2)
```

| Field | Meaning |
| --- | --- |
| `id` | Lower-case letters, digits and dashes. When the resources list a `vps` with the same name, the location is expected to report only until that date: a machine that is let go stops counting, and its token can no longer write. Renewing it (a new date) brings it back. |
| `name` | Public name. Required. |
| `region` | `overseas` or `mainland`. Rules compare the two (a service unreachable only from mainland China is a path problem, not an outage). |
| `network` | Free text, shown with the name. |
| `address`, `rotation` | Optional, for CDN nodes. Every minute Beacon resolves both and exports `linkos_location_in_rotation{location, rotation, role}` (1 when the node's address is among the rotation's answers), `linkos_rotation_resolve_success`, `linkos_rotation_members`, and `linkos_rotation_unknown_address{rotation, address}` for an answer no location claims. A rotation needs the address. |
| `standby` | A standby node is in the rotation only when another node fails: `role="standby"` instead of `primary`, so a rule can alert when a primary is out and inform when a standby is in. Needs a rotation. |
| `probe.timeout` | For a machine on a slower path: the gateway tells that agent (`GET /api/v1/agent`) and every module with a shorter timeout gets this one there. Zero keeps the modules' timeouts. |
| `probe.retries` | For a machine whose connections drop now and then: a probe that fails without an answer (`dns`, `connect`, `timeout`) is tried again at once, up to this many times (0 to 2). A failure with an answer, such as a wrong status code, is never retried. `linkos_probe_attempts` says how many tries each result took, so a flaky path stays visible. A longer timeout does not help a connection that has stalled; a retry on a new one does. |

Beacon exports `linkos_expected_location{location, region, network}` for every location expected to report now, so a rule can find one that has gone silent.

## Heartbeats

A job that must report success at least every `max_age`: backups, scheduled tasks. Jobs on a machine with the agent write a line into its textfile directory (`/var/lib/linkos-agent/textfile/heartbeat_<name>.prom`, as the `linkos-heartbeat` script in a deployment does); others push with a token from `linkos-beacon enroll heartbeat <name>`:

```bash
curl -fsS -H "Authorization: Bearer $TOKEN" --data-binary \
  "linkos_heartbeat_last_success_timestamp_seconds{heartbeat=\"nas-backup\"} $(date +%s)" \
  https://metrics.example.com/api/v1/import/prometheus
```

```yaml
heartbeats:
  - name: nas-backup          # lower-case letters, digits and dashes
    max_age: 26h              # at least 1m
    severity: warning         # critical, warning or info
    description: Nightly backup of the NAS
```

Exported as `linkos_heartbeat_max_age_seconds{heartbeat, severity}`.

## Resources

Things that expire. Names, kinds and dates only; no prices.

```yaml
resources:
  - {name: edge-la-1, kind: vps, expires: 2027-04-02}
  - {name: example.com, kind: domain, expires: 2027-01-15, note: renews automatically}
```

`kind` is `vps`, `domain`, `cert`, `token` or `service`. Exported as `linkos_resource_expiry_timestamp_seconds{resource, kind}`. A `vps` named like a location also bounds that location (see above).

## Targets

Each file is a list of groups, as in Prometheus file service discovery. Every group needs the labels `module` (a name in the module file) and `service` (what the result belongs to); other labels are kept and reach the rules and the cards.

```yaml
- targets: [https://example.com/healthz]
  labels: {module: http_head, service: example.com, severity: critical}
```

The file name decides which locations probe it:

| File name | Probed by |
| --- | --- |
| `all-*.yaml` | every location |
| `overseas-*.yaml`, `mainland-*.yaml` | the locations of that region |
| `<location id>-*.yaml` | that location only (the longest matching id wins) |

Other names are refused. Changing targets needs a reload of Beacon, not a redeployment of the probes: each agent fetches its list every minute.

## Modules

The format of the [Blackbox Exporter's module file](https://github.com/prometheus/blackbox_exporter/blob/master/CONFIGURATION.md) for the parts below, so existing modules keep working, plus three probers of Beacon's own. Results use the Blackbox Exporter's metric names (`probe_success`, `probe_duration_seconds`, `probe_http_status_code`, `probe_ssl_earliest_cert_expiry`, …), and a failure also says why: `probe_failure_info{reason}` with `dns`, `connect`, `timeout`, `tls`, `not_ssl`, `http_status`, `body`, `banner`, `protocol`, `auth`, `manifest` or `config`.

| Prober | Fields |
| --- | --- |
| `http` | `method`, `valid_status_codes`, `follow_redirects`, `fail_if_not_ssl`, `fail_if_body_not_matches_regexp`, `headers`, `preferred_ip_protocol`, `ip_protocol_fallback` |
| `tcp` | `tls`, `query_response` (`expect`, `send`), and the IP fields |
| `registry` | A container registry: `/v2/`, a token if asked for, then HEAD of a manifest without pulling layers. `repository`, `reference`, `username`, `password_file` |
| `git` | Git over HTTP: `info/refs?service=git-upload-pack`. `username`, `password_file` |
| `rdap` | A domain's expiry from RDAP: `server` (default `https://rdap.org`) |

Every module has a `timeout` (default 10s), which a location's `probe.timeout` can lengthen. `password_file` is a path on the probe's machine.

```yaml
modules:
  http_head:
    prober: http
    timeout: 20s
    http:
      method: HEAD
      valid_status_codes: [200]
      follow_redirects: false
      fail_if_not_ssl: true
      preferred_ip_protocol: ip4
      ip_protocol_fallback: false
```

`linkos-probe check -modules <file>` validates a module file, and `linkos-probe run -modules <file> <module> <target>` runs one probe by hand.
