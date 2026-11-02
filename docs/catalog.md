# Catalogue, targets and modules

English | [简体中文](zh/catalog.md)

What Beacon monitors lives in its database: the catalogue (probe locations, heartbeats, expiring resources), the probe targets and the probe modules. People and agents change it through the API, MCP and the command line. Every change is checked as the files always were, takes effect at once (nothing needs a reload), and is a version in a history of who changed what, when, through what and why, which can be reverted. Files are for import and export, in the layout deployments have always had, so a deployment may still keep them in git.

| What | In the API and MCP | As files (import, export) | Read by |
| --- | --- | --- | --- |
| Catalogue: probe locations, heartbeats, expiring resources | `locations`, `heartbeats`, `resources` | `catalog/*.yaml` (configuration key `catalog_dir`) | Beacon, which exports it as metrics for the rules and uses it to label what probes write |
| Probe targets, in named sets | `targets` | `targets/*.yaml`, one file per set (`gateway.targets_dir`) | The gateway, which hands each probe its own list |
| Probe modules | `modules` | `targets/modules.yml` (`gateway.modules_file`) | The gateway, which hands it to every probe |

The sections after this one give each item's shape in YAML, as in the files; the API and MCP take and return the same shapes in JSON. A catalogue may be split over several files; their lists are joined.

## Changing it

### Through the API and MCP

Reads need `status:read` (or `config:write`), changes the scope `config:write`. Each item is read and written in the shape of its file:

```bash
curl -H "Authorization: Bearer $TOKEN" https://status.example.com/api/v1/config            # everything, with the version
curl -H "Authorization: Bearer $TOKEN" https://status.example.com/api/v1/config/targets    # the target sets, with who probes each
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "https://status.example.com/api/v1/config/locations/edge-la-1?note=new+node" \
  -d '{"name": "United States - Los Angeles", "region": "overseas", "network": "CN2 GIA", "probe": {"timeout": "30s"}}'
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/config/targets/all-web \
  -d '[{"targets": ["https://example.com/healthz"], "labels": {"module": "http_head", "service": "example.com"}}]'
curl -X DELETE -H "Authorization: Bearer $TOKEN" https://status.example.com/api/v1/config/heartbeats/nas-backup
```

The routes are `/api/v1/config/{locations,heartbeats,resources,targets,modules}` (the list) and `…/{id or name}` (`GET`, `PUT`, `DELETE` one item), described in [internal/api/openapi.yaml](../internal/api/openapi.yaml). The MCP tools are `get_config`, `put_config` and `delete_config`, which take a `kind` (`locations`, …) and a `key`. A change returns its version and every item's value before and after; putting what is already there changes nothing. A change that would not pass the checks is refused with `config.invalid` and the reason, and nothing changes: a location cannot go while a target set is named after it alone, and a set must start with a known prefix. Every change is in the audit log and echoed to the alert chat, with the note if one was given. `config:write` decides what every probe checks and how, including which file a module sends as a password (`password_file`); a probe reads such files only from its secrets directory (`/etc/linkos-agent/secrets`, `linkos-probe -secrets-dir`), by name, so that a holder of `config:write` can have a probe send what someone put there and nothing else. Give it to those you would let change these files.

A change may say which version it is based on: `?if_version=12` on `PUT` and `DELETE`, `if_version` in `put_config` and `delete_config`, the version `GET /api/v1/config` (`get_config`) returned. When a later version is in force, the change is refused with `config.conflict`, naming it, and nothing changes: read the item again and decide afresh. An agent that reads the configuration, thinks, and then writes passes it, so that it never overwrites what someone changed meanwhile.

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "https://status.example.com/api/v1/config/heartbeats/nas-backup?if_version=12&note=longer+window" \
  -d '{"max_age": "30h", "severity": "warning", "description": "Nightly backup of the NAS"}'
```

### History and revert

`GET /api/v1/config/history` (MCP `list_config_changes`, CLI `linkos-beacon config history`) lists the changes, newest first: version, time, who, through what (`api`, `mcp`, `cli`; `start` and `signal` for files the service imported itself), what (`put`, `delete`, `import`, `revert`), the items added, changed or removed, and the note. `kind` and `key` narrow it to one item. `GET /api/v1/config/history/{version}` (`get_config_change`) has every item's value before and after.

`POST /api/v1/config/history/{version}/revert` (`revert_config_change`, `linkos-beacon config revert VERSION`) undoes one change as a new change: every item it touched gets its value from before. When a later change touched one of them, the revert is refused with `config.conflict`, naming that change; revert it first, or put the value you want. The latest 10,000 changes are kept.

### Import and export

```bash
docker exec linkos-beacon linkos-beacon config import /srv/ops/beacon -dry-run   # what would change
docker exec linkos-beacon linkos-beacon config import /srv/ops/beacon -note "ops repository at 4f2c1e0"
docker exec linkos-beacon linkos-beacon config export /srv/ops/beacon
```

The directory has the layout above: `catalog/*.yaml`, `targets/*.yaml` and `targets/modules.yml`. An import makes the files the whole configuration, as one change: items the files do not hold are removed, and files that fail their checks change nothing. An export writes the catalogue as `catalog/locations.yaml`, `heartbeats.yaml` and `resources.yaml`, each target set as `targets/<set>.yaml` and the module file as it is held, and removes the other `*.yaml` files (and a `modules.yml` the configuration lacks) from those two directories, so that importing the directory again changes nothing. The round trip keeps all content; comments and layout in the catalogue and target files are not kept, and their lists come out in the order of their ids and names. The module file is kept as it was imported, comments and all, and probes get it byte for byte.

### The files at start and on SIGHUP

When the database holds no configuration yet, the first start imports the files the configuration names (`catalog_dir`, `gateway.targets_dir`, `gateway.modules_file`) and logs it; files that fail their checks stop the start, as they always did. From then on the database is the source, and the files are read only when asked:

- `SIGHUP` imports them, but only when nothing was changed through Beacon since the last import, so that the files cannot silently undo what people and agents changed. Otherwise it logs which change came since, and how to go on: `config export` to bring that change into the files, or `config import` to replace it. Deployments that keep the files in git and send `SIGHUP` after a pull keep working.
- `linkos-beacon config import DIR` imports a directory at any time.

`linkos-beacon check-config` validates the files when they exist and the configuration in the database: through the running service when it answers on its socket, else in the database file.

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
    machine: nas-1            # optional: the machine the job runs on, as the SSH channel names it
```

`machine` names the machine the job runs on, as `linkos-beacon machine list` and `list_machines` show it. The evidence and the timeline for the heartbeat's alerts then name that machine and list the sessions and changes on it, those just before the alert included, so that an agent woken by `HeartbeatStale` knows where to look ([agents.md](agents.md#the-evidence)). A put (API, MCP) refuses a machine the SSH channel does not have, with `config.invalid` and how to put it right, unless the heartbeat named that machine already; an import and a revert take one, since files are often imported before the machines are enrolled, and say so in the change's `warnings`, in the log and in `config import`'s output; `check-config` lists such heartbeats too, without failing.

Exported as `linkos_heartbeat_max_age_seconds{heartbeat, severity}`, with `machine` too when the heartbeat names one. The rules aggregate by `heartbeat` and `severity`, so the label changes no alert.

## Resources

Things that expire. Names, kinds and dates only; no prices.

```yaml
resources:
  - {name: edge-la-1, kind: vps, expires: 2027-04-02}
  - {name: example.com, kind: domain, expires: 2027-01-15, note: renews automatically}
```

`kind` is `vps`, `domain`, `cert`, `token` or `service`. Exported as `linkos_resource_expiry_timestamp_seconds{resource, kind}`. A `vps` named like a location also bounds that location (see above).

## Targets

Targets come in named sets, each a list of groups as in Prometheus file service discovery; as files, each set is one file in the targets directory, named as the set with `.yaml`. Every group needs the labels `module` (a name in the module file) and `service` (what the result belongs to); other labels are kept and reach the rules and the cards.

```yaml
- targets: [https://example.com/healthz]
  labels: {module: http_head, service: example.com, severity: critical}
```

The set's name decides which locations probe it (the API shows it as `probed_by`):

| Set name (file name) | Probed by |
| --- | --- |
| `all-…` (`all-*.yaml`) | every location |
| `overseas-…`, `mainland-…` | the locations of that region |
| `<location id>-…` | that location only (the longest matching id wins) |

Other names are refused. A probe gets a change on its next fetch, within a minute; nothing needs a reload or a redeployment of the probes.

## Modules

The format of the [Blackbox Exporter's module file](https://github.com/prometheus/blackbox_exporter/blob/master/CONFIGURATION.md) for the parts below, so existing modules keep working, plus three probers of Beacon's own. Results use the Blackbox Exporter's metric names (`probe_success`, `probe_duration_seconds`, `probe_http_status_code`, `probe_ssl_earliest_cert_expiry`, …), and a failure also says why: `probe_failure_info{reason}` with `dns`, `connect`, `timeout`, `tls`, `not_ssl`, `http_status`, `body`, `banner`, `protocol`, `auth`, `manifest` or `config`.

| Prober | Fields |
| --- | --- |
| `http` | `method`, `valid_status_codes`, `follow_redirects`, `fail_if_not_ssl`, `fail_if_body_not_matches_regexp`, `headers`, `preferred_ip_protocol`, `ip_protocol_fallback` |
| `tcp` | `tls`, `query_response` (`expect`, `send`), and the IP fields |
| `registry` | A container registry: `/v2/`, a token if asked for, then HEAD of a manifest without pulling layers. `repository`, `reference`, `username`, `password_file` |
| `git` | Git over HTTP: `info/refs?service=git-upload-pack`. `username`, `password_file` |
| `rdap` | A domain's expiry from RDAP: `server` (default `https://rdap.org`) |

Every module has a `timeout` (default 10s), which a location's `probe.timeout` can lengthen.

`password_file` names a file in the probe's secrets directory, `/etc/linkos-agent/secrets` unless `linkos-probe` is started with `-secrets-dir`: `git-password`, never a path. A value with a path separator or `..`, or an absolute path, is refused with `config.invalid` when the module is put or imported, so that `config:write` cannot have a probe read any file on its machine; a probe handed such a module by an older Beacon refuses each probe that would read it (`probe_failure_info{reason="config"}`, one log line) and reads nothing. The probe never reads outside that directory for a module. Probes from before this release read the name relative to their working directory: upgrade them before adding a module with `password_file`.

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

Each module is an item of its own in the API (`/api/v1/config/modules/http_head`, in JSON). Changing or removing one rewrites the module file in a standard layout, keeping the other modules and their comments.

`linkos-probe check -modules <file>` validates a module file, and `linkos-probe run -modules <file> <module> <target>` runs one probe by hand.
