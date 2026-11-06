# The catalogue

What Beacon watches is held in its database and changed through the API, MCP and the command line; the files are for import and export, and for the first start, in the layout deployments have always had in `/etc/beacon` ([Monitor a service](../guide/monitoring.md)). This page is the shapes of those files and the checks every one of them passes, whatever it comes through; the API and MCP take and return the same shapes, as JSON. A change that would not pass is refused with `config.invalid` and the reason, and nothing changes.

| Files | Hold | Configuration keys |
| --- | --- | --- |
| `catalog/*.yaml` | The catalogue: probe locations, heartbeats, expiring resources, status pages | `catalog_dir` |
| `targets/*.yaml` | Probe targets, one file per set, the file named as the set | `gateway.targets_dir` |
| `targets/modules.yml` | The probe modules | `gateway.modules_file` |

Three rules hold the files together:

- **No unknown fields.** The catalogue and target files are read strictly: a field the tables below do not list fails the file, so a typo does not go unnoticed. Through the API the same check refuses unknown fields of the JSON body.
- **A catalogue may be split over several files.** Every `*.yaml` in the directory is read and their lists are joined, in file name order; `catalog/locations.yaml` and `catalog/heartbeats.yaml` are conventions, not requirements.
- **Import is whole.** `beacon config import DIR` makes the directory's files the entire configuration, and items the files do not hold are removed; `-dry-run` says what would change first ([Command line](cli.md#beacon-config)).

## Locations

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

| Field | Type | Rules |
| --- | --- | --- |
| `id` | string | Lower-case letters, digits and dashes, at most 63 characters; unique. When the resources list a `vps` of the same name, the location is expected to report only until that date — the whole expiry day still counts — and its token can no longer write once it passes |
| `name` | string | Required, public: where and on which network, never which vendor |
| `region` | `overseas` or `mainland` | Required; the rules compare the two |
| `network` | string | Free text, shown with the name |
| `address` | string | For CDN nodes; a `rotation` needs it |
| `rotation` | string | The cluster's DNS name, resolved every minute ([Metrics and alert rules](metrics.md)) |
| `standby` | bool | True: the node joins the rotation only when another fails; needs a `rotation` |
| `probe.timeout` | duration | The least time any probe gets at this location, 0 to `1m` (the agent's interval); zero keeps the modules' timeouts |
| `probe.retries` | 0, 1 or 2 | How many more times a probe that fails without an answer (`dns`, `connect`, `timeout`) is tried at once; a failure with an answer is never retried |

## Heartbeats

```yaml
heartbeats:
  - name: nas-backup          # lower-case letters, digits and dashes
    max_age: 26h              # at least 1m
    severity: warning         # critical, warning or info
    description: Nightly backup of the NAS
    machine: nas-1            # optional: the machine the job runs on, as the SSH channel names it
```

| Field | Type | Rules |
| --- | --- | --- |
| `name` | string | Lower-case letters, digits and dashes, at most 63 characters; unique |
| `max_age` | duration | At least `1m`; how old a success may get before `HeartbeatStale` fires |
| `severity` | `critical`, `warning` or `info` | The alert's level |
| `description` | string | Free text |
| `machine` | string | Optional; a machine's name, lower-case letters, digits and dashes. A put (API, MCP) refuses a machine the SSH channel does not have, unless the heartbeat named it already; an import and a revert take one, with a warning |

How a job reports success, and how the heartbeat becomes something an agent can act on, is in [Heartbeats and expiring resources](../guide/heartbeats.md).

## Resources

Things that expire. Names, kinds and dates only; no prices.

```yaml
resources:
  - {name: edge-1, kind: vps, expires: 2027-04-02}
  - {name: example.com, kind: domain, expires: 2027-01-15, note: renews automatically}
```

| Field | Type | Rules |
| --- | --- | --- |
| `name` | string | Required, unique |
| `kind` | `vps`, `domain`, `cert`, `token` or `service` | Required |
| `expires` | date | `YYYY-MM-DD` |
| `note` | string | Free text |

Exported as `beacon_resource_expiry_timestamp_seconds{resource, kind}`; `ResourceExpiring` warns 30 days ahead and goes critical 7 days ahead. A `vps` named like a location also bounds that location ([Locations](#locations)).

## Status pages

A status page is part of the catalogue, under `status_pages`, so it is changed, versioned and imported like the rest. This is the shape; what a page shows and the full example are in [Status pages](../guide/status-pages.md#define-your-own-page).

| Field | Type | Rules |
| --- | --- | --- |
| `name` | string | Lower-case letters, digits and dashes; unique; the page's address `/status/<name>` |
| `title`, `description` | text | One string, or one per language (`{en: …, zh: …}`); title required, at most 120 characters; description at most 1000 |
| `host` | host name | Optional; served at `/` on this host too; no port, no scheme, no other page's |
| `default` | bool | At most one page is `default` |
| `brand` | `beacon`, `linkos`, `linkos-status`, `linkos-mirrors`, `openlinkos` | The design system's brand it wears |
| `languages` | list | `en` and `zh`, each once, the default first |
| `components` | list | At least one; each needs a `name` and `title`, and `services` (the service labels of its probe targets) or `page` (a page label on them) |
| `groups` | list | Optional headings; each names the page's own components, each component in one group at most |
| `alerts` | list of alert names | The alerts that count as incidents; the availability alerts by default |
| `notes` | `public` or `all` | Which notes are published as updates: those that begin with `public:` (the default), or all |
| `links`, `menu` | links | `https`, `http` or `mailto` links only |

## Target sets

A target set is a list of groups in the format of Prometheus file service discovery: the targets, and the labels they share. Each set is one file in the targets directory, named as the set with `.yaml`.

```yaml
- targets: [https://example.com/healthz]
  labels: {module: http_head, service: example.com, severity: critical}
```

| Field | Type | Rules |
| --- | --- | --- |
| `targets` | list | Required; a group with none fails the file |
| `labels.module` | string | Required; a name in the module file |
| `labels.service` | string | Required; what the result belongs to: alerts, cards and status pages speak of services |
| other labels | strings | Kept: `severity` (`critical`, `warning` or `info`) becomes the alert's level, `failure_domain` names what fails together |

The set's name says which locations probe it (the API shows it as `probed_by`); any other name fails, so a typo does not silently stop a check:

| Set name (file name) | Probed by |
| --- | --- |
| `all-…` | every location |
| `overseas-…`, `mainland-…` | the locations of that region |
| `<location id>-…` | that location only; the longest matching id wins |

## Modules

The module file has the format of the [Blackbox Exporter's](https://github.com/prometheus/blackbox_exporter/blob/master/CONFIGURATION.md) for the parts below, so existing modules keep working, plus three probers of Beacon's own. Results use the Blackbox Exporter's metric names (`probe_success`, `probe_duration_seconds`, `probe_http_status_code`, `probe_ssl_earliest_cert_expiry`), with `probe_failure_info{reason}` for why a check failed and `beacon_probe_attempts` for how many tries it took.

| Prober | Fields |
| --- | --- |
| `http` | `method`, `valid_status_codes`, `follow_redirects`, `fail_if_not_ssl`, `fail_if_body_not_matches_regexp`, `headers`, `preferred_ip_protocol`, `ip_protocol_fallback` |
| `tcp` | `tls`, `query_response` (`expect`, `send`), and the IP fields |
| `registry` | A container registry: `/v2/`, a token if asked for, then HEAD of a manifest without pulling layers. `repository`, `reference`, `username`, `password_file` |
| `git` | Git over HTTP: `info/refs?service=git-upload-pack`. `username`, `password_file` |
| `rdap` | A domain's expiry from RDAP: `server` (default `https://rdap.org`) |

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

Every module has a `timeout` (default `10s`), which a location's `probe.timeout` can lengthen. `prober` must be one of the five; `preferred_ip_protocol` must be `ip4` or `ip6`; the regular expressions must compile. A failed check says why, in `probe_failure_info{reason}`:

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

`password_file` names a file in the probe's secrets directory, `/etc/beacon-probe/secrets` unless `beacon-probe` is started with `-secrets-dir`: a name such as `git-password`, never a path. A value with a path separator or `..`, an absolute path, or a control character is refused when the module is put or imported (`config.invalid`), so that `config:write` cannot have a probe read any file on its machine; a probe handed such a module by an older Beacon refuses each probe that would read it and reads nothing. The module file is kept as it was imported, comments and all, and probes get it byte for byte; changing one module rewrites the file in a standard layout, keeping the others and their comments.

`beacon-probe check -modules <file>` validates a module file on any machine, and `beacon-probe run -modules <file> <module> <target>` runs one probe by hand ([Command line](cli.md#beacon-probe)).

## The checks

Collected, the checks every change passes, whatever it comes through — a put, a delete, an import, a revert:

| Check | Outcome |
| --- | --- |
| An unknown field in a file or a body | Refused, `config.invalid`, nothing changes |
| A location, heartbeat, resource or status page listed twice | Refused, `config.invalid` |
| A target set whose name starts with none of `all-`, a region or a location id | Refused, `config.invalid`; removing a location that a set is named after alone fails here, until the set goes or is renamed |
| A heartbeat naming a machine the SSH channel does not have (a put; an import and a revert take one) | Refused for a put, a warning for an import or a revert |
| A module's `password_file` that is not a plain file name | Refused, `config.invalid` |

A change that passes takes effect at once — nothing to reload, no probe to redeploy — and becomes a version in the history, with who changed what, when, through what and why, which can be reverted. Warnings stop no change: a heartbeat whose machine is not enrolled, a status page component whose services no target has. `beacon check-config` reports both kinds without changing anything.
