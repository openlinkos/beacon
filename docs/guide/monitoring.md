# Monitor a service

By the end of this page a service of yours is checked from every probe location you run, each failure carries its reason, and what Beacon watches lives in its database as versions: every change is checked before it takes effect, takes effect at once (no probe to redeploy) and can be reverted. Jobs that must report in and things that expire are on the next page: [Heartbeats and expiring resources](heartbeats.md).

You need a centre with at least one probe location reporting (to add one, [Probe locations](../self-hosting/probes.md)) and a credential with `config:write`: the console's **Configuration** page, or a token. Reads take `status:read`. The reference deployment runs commands as `docker exec beacon beacon …`.

## Add a probe location {#locations}

A location is one machine running `beacon-probe agent`. Add it to the catalogue first (enrolling an id the catalogue lacks fails), then install the probe there:

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "https://status.example.com/api/v1/config/locations/edge-2?note=new+node" \
  -d '{"name": "Germany - Frankfurt", "region": "overseas", "network": "BGP", "probe": {"timeout": "30s"}}'
```

You will see the new version and the item, and the change echoed to the alert chat. The gateway labels everything a probe writes with `location`, `region`, `network` and `host` from the catalogue, in place of anything the probe says about itself. `region` (`overseas` or `mainland`) lets the rules tell a path problem from an outage; `name` is public, shown on the [status pages](status-pages.md), so name locations by place and network, never by vendor. Every field of the entry is in the catalogue reference: [The catalogue](../reference/catalogue.md).

## Add a target set {#targets}

A target is what gets checked (a URL, a `host:port`, a registry repository, a domain); targets come in named sets, and the set's name says which locations probe it. A set is a list of groups in the format of Prometheus file service discovery, each with its targets and the labels they share:

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/config/targets/all-web \
  -d '[{"targets": ["https://example.com/healthz"], "labels": {"module": "http_head", "service": "example.com"}}]'
```

- `module` names a module; `service` says what the result belongs to (alerts, cards and status pages speak of services). Both are required.
- `severity` (`critical`, `warning` or `info`) becomes the alert's level; `failure_domain` names what fails together, so related alerts show side by side in the [evidence](on-call.md#the-evidence).
- The name decides who probes it: `all-…` every location, `overseas-…` and `mainland-…` those regions, `<location id>-…` that location only. Anything else is refused, so a typo cannot silently stop a check.

A probe picks up the change on its next fetch, within a minute. `GET /api/v1/config/targets` shows every set with who probes it (`probed_by`).

## Check a target by hand

A failed check says why, in `probe_failure_info{reason}`: `dns`, `connect`, `timeout`, `tls`, `not_ssl`, `http_status`, `body`, and a few more. To try a module once before handing it to every probe, on any machine with `beacon-probe`:

```bash
beacon-probe run -modules modules.yml http_head https://example.com/healthz
```

You will see the result's metrics, and on failure `failed: <reason>: <detail>`. When a target always fails from one location only, [A target always fails](../self-hosting/troubleshooting.md#a-target-always-fails) walks the reasons.

## Modules

A module says how to check: the reference deployment ships `http_head`, `http_get_200`, `http_get_healthy`, `tcp_tls` and `rdap_domain`. The file format is the [Blackbox Exporter's](https://github.com/prometheus/blackbox_exporter/blob/master/CONFIGURATION.md) for `http` and `tcp`, plus Beacon's own `registry`, `git` and `rdap` probers; every module has a `timeout` (10 seconds by default), which a location's `probe.timeout` can lengthen. `password_file` names a file in the probe's secrets directory by name only (`/etc/beacon-probe/secrets` by default) — a value with a path in it is refused with `config.invalid`, so `config:write` cannot have a probe read any file on its machine. Put a module through the same `/api/v1/config/modules/{name}` route; every field of every prober is in the catalogue reference: [The catalogue](../reference/catalogue.md).

## Change it without stepping on anyone

Every change, whatever it comes through, is checked as the files always were; one that would not pass is refused with `config.invalid` and nothing changes. Changes take `config:write`; give it only to those you would let edit these files ([Scopes](../reference/api.md#scopes)).

1. **Say which version you are changing.** An agent that reads, thinks and then writes should pass `if_version`, so it never overwrites what someone changed meanwhile:

   ```bash
   curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
     "https://status.example.com/api/v1/config/targets/all-web?if_version=12" \
     -d '[{"targets": ["https://example.com/healthz", "https://www.example.com/"], "labels": {"module": "http_head", "service": "example.com"}}]'
   ```

   A later version in force is refused with `config.conflict`; read again and decide afresh.

2. **Read the history, revert a change.** Every change is a version with who, through what, and the note; the latest 10,000 are kept:

   ```bash
   docker exec beacon beacon config history
   docker exec beacon beacon config revert 12
   ```

   A revert is refused with `config.conflict` when a later change touched one of the same items; revert that one first.

## Files, import and export

The catalogue, target sets and the module file also exist as files (`catalog/*.yaml`, `targets/*.yaml`, `targets/modules.yml`), for import and export. An import makes the files the whole configuration: **items the files do not hold are removed**, so always look first:

```bash
docker exec beacon beacon config import /etc/beacon -dry-run
docker exec beacon beacon config import /etc/beacon -note "ops repository at 4f2c1e0"
```

`config export DIR` writes what is in force back into a directory. Pick one way of working and keep to it: either the files are the source and every change is an import, or Beacon is the source and the files are an export. When Beacon starts with an empty database it imports the files once; after that a `SIGHUP` imports them only when nothing was changed through Beacon since the last import ([Configuration](../self-hosting/configuration.md)).

## Heartbeats {#heartbeats}

Jobs that must report in, and things that expire, are watched by their dates rather than probed: [Heartbeats and expiring resources](heartbeats.md).

## Check it

A minute after the target set goes in, query what the probes sent, in VictoriaMetrics on the centre (`http://127.0.0.1:8428/vmui`):

```
probe_success{service="example.com"}
```

One line per location, each 1 while the service answers. With the reference rules a service counts as down only when at least two locations fail it and they are at least half of those reporting, after 3 minutes — so a check from one location alone never pages anyone ([How Beacon works](concepts.md#an-alerts-path)).

When it does not work: [A probe location shows no data](../self-hosting/troubleshooting.md#a-probe-location-shows-no-data), [A target always fails](../self-hosting/troubleshooting.md#a-target-always-fails), [An import or SIGHUP did not change the configuration](../self-hosting/troubleshooting.md#an-import-or-sighup-did-not-change-the-configuration), [A change is refused with config.conflict or config.invalid](../self-hosting/troubleshooting.md#a-change-is-refused-with-configconflict-or-configinvalid).

## Next

- [Heartbeats and expiring resources](heartbeats.md): the jobs and the expiry dates Beacon watches by their dates.
- [Publish a status page](status-pages.md): what the probes agree on, public.
- [Put an agent on call](on-call.md): who the resulting alerts wake.
