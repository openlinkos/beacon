# What is checked, and from where

This page is for whoever decides what a Beacon watches, a person or an agent with the right token. It says which machines probe, what they probe and how, which scheduled jobs must report in, and which things are about to expire; then how to change any of it through Beacon without undoing someone else's change. The examples assume the reference deployment from [Up in 30 minutes](install.md), where commands run through `docker exec beacon beacon …`.

## How it fits together

- A **probe location** is a machine that runs `beacon-probe agent`, the probe program: a small VPS where your users are, an edge node, the centre itself (the machine Beacon runs on). Every minute it asks Beacon's gateway what to check, checks it, and pushes the results.
- A **target** is what gets checked: a URL, a `host:port`, a registry repository, a domain. Targets come in named **target sets**, and the name of a set says which locations probe it.
- A **module** says how to check a target: an HTTP `HEAD` that expects 200, a TLS handshake, a container registry's manifest, a domain's expiry. Every failed check carries its reason (`dns`, `connect`, `timeout`, `tls`, `http_status`, …).
- One location's word is never an outage. With the reference rules (`deploy/rules/probes.yml`), a location counts as failing a service once it has failed it for 3 minutes, and the service is down only when at least two locations fail it and they are at least half of those reporting.
- A **heartbeat** is a scheduled job that must report success often enough, a backup say; a **resource** is something that expires, a domain or a rented machine. Neither is probed; Beacon watches their dates.

All of this lives in Beacon's database, not in files. People and agents change it through the API, MCP and the command line; every change is checked before it takes effect, takes effect at once (nothing to reload, no probe to redeploy), and becomes a version in a history of who changed what, when, through what and why, which can be reverted. Files are for import and export, in the layout deployments have always had, so a deployment may still keep them in git.

| What | In the API and MCP | As files (import, export) | Read by |
| --- | --- | --- | --- |
| Catalogue: probe locations, heartbeats, expiring resources | `locations`, `heartbeats`, `resources` | `catalog/*.yaml` (configuration key `catalog_dir`) | Beacon, which exports it as metrics for the rules and uses it to label what probes write |
| Probe targets, in named sets | `targets` | `targets/*.yaml`, one file per set (`gateway.targets_dir`) | The gateway, which hands each probe its own list |
| Probe modules | `modules` | `targets/modules.yml` (`gateway.modules_file`) | The gateway, which hands it to every probe |
| Public status pages | `status_pages` | `catalog/*.yaml`, under `status_pages` | Beacon, which serves them ([Status pages](status.md)) |

The sections below give each item's shape in YAML, as in the files; the API and MCP take and return the same shapes in JSON. A catalogue may be split over several files; their lists are joined.

## Locations

A probe location is one machine running `beacon-probe agent` with a token of its own from `beacon enroll probe <id>`. Add the location to the catalogue first: enrolling an id the catalogue lacks fails with `location edge-2 is not in the catalogue`. The gateway labels everything a probe writes with `location`, `region`, `network` and `host` from the catalogue, in place of anything the probe says about itself, so a probe cannot speak for another.

```yaml
locations:
  - id: edge-1                         # internal: metrics, cards, configuration; the machine's name
    name: United States - Los Angeles  # public, for the status page: where and on which network, never which vendor
    region: overseas                   # overseas or mainland
    network: CN2 GIA
    address: edge-1.example.net        # CDN nodes: the node's address (a name or an IP) ...
    rotation: cdn.example.com          # ... and its cluster's DNS name
    standby: false                     # true: the node joins the rotation only when another fails
    probe:
      timeout: 30s                     # every probe here gets at least this long (at most 1m)
      retries: 1                       # a probe that fails without an answer is tried again at once (at most 2)
```

| Field | Meaning |
| --- | --- |
| `id` | Lower-case letters, digits and dashes. When the resources list a `vps` with the same name, the location is expected to report only until that date (the whole day still counts): a machine that is let go stops counting, and its token can no longer write. Renewing it (a new date) brings it back. |
| `name` | Public name. Required. |
| `region` | `overseas` or `mainland`. Rules compare the two (a service unreachable only from mainland China is a path problem, not an outage). |
| `network` | Free text, shown with the name. |
| `address`, `rotation` | Optional, for CDN nodes. Every minute Beacon resolves both and exports `beacon_location_in_rotation{location, rotation, role}` (1 when the node's address is among the rotation's answers), `beacon_rotation_resolve_success`, `beacon_rotation_members`, and `beacon_rotation_unknown_address{rotation, address}` for an answer no location claims. A rotation needs the address. |
| `standby` | A standby node is in the rotation only when another node fails: `role="standby"` instead of `primary`, so a rule can alert when a primary is out and inform when a standby is in. Needs a rotation. |
| `probe.timeout` | For a machine on a slower path: the gateway tells that agent (`GET /api/v1/agent`) and every module with a shorter timeout gets this one there. Zero keeps the modules' timeouts. |
| `probe.retries` | For a machine whose connections drop now and then: a probe that fails without an answer (`dns`, `connect`, `timeout`) is tried again at once, up to this many times (0 to 2). A failure with an answer, such as a wrong status code, is never retried. `beacon_probe_attempts` says how many tries each result took, so a flaky path stays visible. A longer timeout does not help a connection that has stalled; a retry on a new one does. |

Name locations by place and network, never by vendor: the name is shown on the public status page. Beacon exports `beacon_expected_location{location, region, network}` for every location expected to report now, so a rule can find one that has gone silent: the reference rule `ProbeLocationDown` fires 5 minutes after a location has gone 10 minutes without results.

## Targets

A target set is a list of groups in the format of Prometheus file service discovery: the targets, and the labels they share. As files, each set is one file in the targets directory, named as the set with `.yaml`. Every group needs at least one target and the labels `module` (a name in the module file) and `service` (what the result belongs to: alerts, cards and status pages speak of services). Other labels are kept and reach the rules and the cards: `severity` (`critical`, `warning` or `info`) becomes the alert's level, and `failure_domain` names what fails together (one origin, one cluster), so that related alerts are shown side by side.

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

Other names are refused, so a typo does not silently stop a check. A probe gets a change on its next fetch, within a minute; nothing needs a reload or a redeployment of the probes.

## Modules

The module file has the format of the [Blackbox Exporter's](https://github.com/prometheus/blackbox_exporter/blob/master/CONFIGURATION.md) for the parts below, so existing modules keep working, plus three probers of Beacon's own. Results use the Blackbox Exporter's metric names (`probe_success`, `probe_duration_seconds`, `probe_http_status_code`, `probe_ssl_earliest_cert_expiry`, …).

| Prober | Fields |
| --- | --- |
| `http` | `method`, `valid_status_codes`, `follow_redirects`, `fail_if_not_ssl`, `fail_if_body_not_matches_regexp`, `headers`, `preferred_ip_protocol`, `ip_protocol_fallback` |
| `tcp` | `tls`, `query_response` (`expect`, `send`), and the IP fields |
| `registry` | A container registry: `/v2/`, a token if asked for, then HEAD of a manifest without pulling layers. `repository`, `reference`, `username`, `password_file` |
| `git` | Git over HTTP: `info/refs?service=git-upload-pack`. `username`, `password_file` |
| `rdap` | A domain's expiry from RDAP: `server` (default `https://rdap.org`) |

Every module has a `timeout` (default 10s), which a location's `probe.timeout` can lengthen. The reference deployment ships five: `http_head`, `http_get_200`, `http_get_healthy`, `tcp_tls` and `rdap_domain`.

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

A failed check says why, in `probe_failure_info{reason}`. This is what an agent reads first in the evidence:

| Reason | What happened |
| --- | --- |
| `dns` | The name did not resolve |
| `connect` | The connection was refused or reset |
| `timeout` | No answer in time |
| `tls` | The TLS handshake or the certificate failed |
| `not_ssl` | Plain HTTP where HTTPS was required |
| `http_status` | An unexpected status code |
| `body` | The body did not match |
| `banner` | A TCP server did not say what was expected |
| `protocol` | An answer of the wrong kind (content type, JSON) |
| `auth` | A registry token could not be obtained |
| `manifest` | The registry has no such manifest |
| `config` | The module or the target is wrong |

`password_file` names a file in the probe's secrets directory, `/etc/beacon-probe/secrets` unless `beacon-probe` is started with `-secrets-dir`: `git-password`, never a path. A value with a path separator or `..`, or an absolute path, is refused with `config.invalid` when the module is put or imported, so that `config:write` cannot have a probe read any file on its machine; a probe handed such a module by an older Beacon refuses each probe that would read it (`probe_failure_info{reason="config"}`, one log line) and reads nothing. The probe never reads outside that directory for a module. Probes from before this release read the name relative to their working directory: upgrade them before adding a module with `password_file`.

Each module is an item of its own in the API (`/api/v1/config/modules/http_head`, in JSON). Changing or removing one rewrites the module file in a standard layout, keeping the other modules and their comments.

To try a module before handing it to every probe, on any machine with `beacon-probe`: `beacon-probe check -modules <file>` validates a module file, and `beacon-probe run -modules <file> <module> <target>` runs one probe by hand.

## Heartbeats

A heartbeat is a job that must report success at least every `max_age`: a backup, a scheduled task. When it is late, `HeartbeatStale` fires; when one listed here has never reported, `HeartbeatMissing`. A job on a machine with the probe agent writes a line into the agent's textfile directory (`/var/lib/beacon-probe/textfile/heartbeat_<name>.prom`, which the `beacon-heartbeat` script of the reference deployment does: end the job with `beacon-heartbeat <name>`); a job elsewhere pushes one sample with a token from `beacon enroll heartbeat <name>`:

```bash
curl -fsS -H "Authorization: Bearer $TOKEN" --data-binary \
  "beacon_heartbeat_last_success_timestamp_seconds{heartbeat=\"nas-backup\"} $(date +%s)" \
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

Give `machine` whenever the job runs on a machine the [SSH channel](ssh.md) reaches. It names the machine as `beacon machine list` and `list_machines` show it, and it is what turns a stale heartbeat into something an agent can act on: the evidence and the timeline for the heartbeat's alerts then name that machine and list the sessions and changes on it, those just before the alert included, so an agent woken by `HeartbeatStale` knows where to look and sees, say, the session that stopped the job's timer three minutes earlier ([The evidence](agents.md#the-evidence)).

A put (API, MCP) refuses a machine the SSH channel does not have, with `config.invalid` and how to put it right, unless the heartbeat named that machine already. An import and a revert take one, since files are often imported before the machines are enrolled, and say so in the change's `warnings`, in the log and in `config import`'s output; `check-config` lists such heartbeats too, without failing.

Exported as `beacon_heartbeat_max_age_seconds{heartbeat, severity}`, with `machine` too when the heartbeat names one. The rules aggregate by `heartbeat` and `severity`, so the label changes no alert.

## Resources

Things that expire. Names, kinds and dates only; no prices.

```yaml
resources:
  - {name: edge-1, kind: vps, expires: 2027-04-02}
  - {name: example.com, kind: domain, expires: 2027-01-15, note: renews automatically}
```

`kind` is `vps`, `domain`, `cert`, `token` or `service`; `expires` is a date such as `2027-04-02`. Exported as `beacon_resource_expiry_timestamp_seconds{resource, kind}`, which `ResourceExpiring` watches: a warning 30 days ahead, critical 7 days ahead. A `vps` named like a location also bounds that location ([Locations](#locations)).

## Changing it

Changes take the scope `config:write`; reads take `status:read` or `config:write`. `config:write` decides what every probe checks and how, including which file a module sends as a password (`password_file`); since a probe reads such files only from its secrets directory, by name, a holder of `config:write` can have a probe send what someone put there and nothing else. Give it to those you would let change these files.

Every change, whatever it comes through, is checked as the files always were. A change that would not pass is refused with `config.invalid` and the reason, and nothing changes: a location cannot go while a target set is named after it alone, and a set must start with a known prefix. Every change is in the audit log and echoed to the alert chat, with its note if one was given.

### Through the API and MCP

Each item is read and written in the shape of its file:

```bash
curl -H "Authorization: Bearer $TOKEN" https://status.example.com/api/v1/config            # everything, with the version
curl -H "Authorization: Bearer $TOKEN" https://status.example.com/api/v1/config/targets    # the target sets, with who probes each
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "https://status.example.com/api/v1/config/locations/edge-2?note=new+node" \
  -d '{"name": "Germany - Frankfurt", "region": "overseas", "network": "BGP", "probe": {"timeout": "30s"}}'
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/config/targets/all-web \
  -d '[{"targets": ["https://example.com/healthz"], "labels": {"module": "http_head", "service": "example.com"}}]'
curl -X DELETE -H "Authorization: Bearer $TOKEN" https://status.example.com/api/v1/config/heartbeats/nas-backup
```

The routes are `/api/v1/config/{locations,heartbeats,resources,targets,modules,status_pages}` (the list) and `…/{id or name}` (`GET`, `PUT`, `DELETE` one item), listed in the [API reference](reference/api.md) and described in full in `internal/api/openapi.yaml`. The MCP tools are `get_config`, `put_config` and `delete_config`, which take a `kind` (`locations`, …) and a `key`. A change returns its version and every item's value before and after; putting what is already there changes nothing.

A change may say which version it is based on: `?if_version=12` on `PUT` and `DELETE`, `if_version` in `put_config` and `delete_config`, the version `GET /api/v1/config` (`get_config`) returned. When a later version is in force, the change is refused with `config.conflict` (`the configuration changed: version 13 is in force, not 12 as you last saw; read it again and retry`), and nothing changes: read the item again and decide afresh. An agent that reads the configuration, thinks, and then writes should pass it, so that it never overwrites what someone changed meanwhile.

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "https://status.example.com/api/v1/config/heartbeats/nas-backup?if_version=12&note=longer+window" \
  -d '{"max_age": "30h", "severity": "warning", "description": "Nightly backup of the NAS"}'
```

### History and revert

`GET /api/v1/config/history` (MCP `list_config_changes`, CLI `beacon config history`) lists the changes, newest first: version, time, who, through what (`api`, `mcp`, `cli`, `console`; `start` and `signal` for files the service imported itself), what (`put`, `delete`, `import`, `revert`), the items added, changed or removed, and the note. `kind` and `key` narrow it to one item. `GET /api/v1/config/history/{version}` (`get_config_change`) has every item's value before and after. The command line prints one change a line, under the columns `VERSION`, `TIME`, `WHO`, `VIA`, `ACTION`, `ITEMS` and `NOTE`; the first line of a new deployment is version 1, by `beacon`, via `start`, an `import`.

`POST /api/v1/config/history/{version}/revert` (`revert_config_change`, `beacon config revert VERSION`) undoes one change as a new change: every item it touched gets its value from before. When a later change touched one of them, the revert is refused with `config.conflict`, naming that change; revert it first, or put the value you want. The latest 10,000 changes are kept.

### Import and export

An import makes the files in a directory the whole configuration, as one change. That is the point of it, and also its trap: **items the files do not hold are removed**, a location or a target set added through the API included. Always look first with `-dry-run`. The directory has the layout of the table above: `catalog/*.yaml`, `targets/*.yaml` and `targets/modules.yml`. The path is read by the command, so with `docker exec` it is a path inside the container.

```bash
docker exec beacon beacon config import /etc/beacon -dry-run
docker exec beacon beacon config import /etc/beacon -note "ops repository at 4f2c1e0"
```

The dry run says what would change and changes nothing; the import then makes the change, with its version. Each line is an item, `+` added, `~` changed, `-` removed:

```
would change 2 items (nothing changed):
  + locations edge-2
  ~ targets all-web
version 2: import of 2 items
  + locations edge-2
  ~ targets all-web
```

When the files hold what is in force already, the answer is `nothing to change: version 2 holds this already`. Files that fail their checks change nothing, and the error says why.

An export writes the configuration in force into a directory: the catalogue as `catalog/locations.yaml`, `heartbeats.yaml`, `resources.yaml` and `status_pages.yaml` (those that hold anything), each target set as `targets/<set>.yaml` and the module file as it is held. It removes the other `*.yaml` files (and a `modules.yml` the configuration lacks) from those two directories, so that importing the directory again changes nothing, and it prints `exported version 2 to DIR: 5 files`, then a line for each file it removed. The round trip keeps all content; comments and layout in the catalogue and target files are not kept, and their lists come out in the order of their ids and names. The module file is kept as it was imported, comments and all, and probes get it byte for byte.

### Files in the reference deployment

`deploy/compose.yaml` mounts the deployment's `catalog/` and `targets/` into the container read-only, as `/etc/beacon/catalog` and `/etc/beacon/targets`. So:

- **To change through the files**, edit them under `/opt/beacon/catalog/` and `/opt/beacon/targets/` on the host and run `docker exec beacon beacon config import /etc/beacon -dry-run`, then without `-dry-run`.
- **To bring changes made through Beacon into the files**, export to a directory the container can write, the data volume, and copy it out; compare before replacing your files, since `docker cp` adds and overwrites but removes nothing:

```bash
docker exec beacon beacon config export /var/lib/beacon/export
docker cp beacon:/var/lib/beacon/export ./export && diff -r ./export/catalog /opt/beacon/catalog
```

Pick one way of working and keep to it: either the files are the source and every change is an import, or Beacon is the source and the files are an export. Mixing the two is how an import removes what someone added through the API.

### The files at start and on SIGHUP

When the database holds no configuration yet, the first start imports the files the configuration names (`catalog_dir`, `gateway.targets_dir`, `gateway.modules_file`) and logs `imported the configuration files into the database, where it is changed from now on`; files that fail their checks stop the start. From then on the database is the source, and the files are read only when asked:

- `SIGHUP` imports them, but only when nothing was changed through Beacon since the last import, so that the files cannot silently undo what people and agents changed. Otherwise it logs which change came since, and how to go on: `config export` to bring that change into the files, or `config import` to replace it. Deployments that keep the files in git and send `SIGHUP` after a pull keep working.
- `beacon config import DIR` imports a directory at any time.

`beacon check-config` validates the files when they exist and the configuration in the database: through the running service when it answers on its socket, else in the database file. Its last two lines say what each holds, such as `files: 2 probe locations, 0 heartbeats, 0 resources, 3 probe targets in 2 sets, 5 modules, 1 status pages` and `database: version 2 in the running service: …`, followed by any warnings.
