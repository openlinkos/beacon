# Monitor a service

By the end of this page, a service of yours is checked from every probe location you run, and each failure carries its reason.

What Beacon watches lives in its database as versions. Every change is checked before it takes effect, takes effect at once (no probe to redeploy) and can be reverted. Jobs that must report in and things that expire are on the next page: [Heartbeats and expiring resources](heartbeats.md).

**You need:**

- A centre with at least one probe location reporting. To add one, see [Probe locations](../self-hosting/probes.md).
- A console account or token with `config:write`. Reads take `status:read`.

The reference deployment runs commands as `docker exec beacon beacon …`.

## Add an HTTP monitor {#add-an-http-monitor}

For a monitor created here, open the service's **Edit HTTP monitor** to change its endpoint, probe locations, check profile or alert severity, or to remove the check. Changes to probe locations move the target set in one version, with the service name and its history intact. Configuration history can revert the change. Manage target sets supplied through files or the configuration API at their existing entry points.

In the console, open **Monitors** and select **Add HTTP monitor**. Give the service a name and its HTTP or HTTPS endpoint. Choose an existing GET or HEAD check profile, the locations that should probe it and its alert severity. Use an endpoint without a query, fragment or credentials.

If a service has one machine operators should open during an outage, an owner can select its enrolled SSH registration as the **Designated machine**. Use **All probe locations**, or another scope with at least two locations, for the reference service outage rule. When a new `ServiceDown` incident reports this service, the signed-in alert page can link to the machine that was designated when the alert started. Reassigning the monitor applies to later incidents. If the registration is removed or replaced, select the new enrolment in the monitor for future alerts. SSH still requires the operator's own account permissions and personal key. People with session access can return to the incident and open recordings captured on that enrolment.

After saving, the service page lists the checks expected from the current probe locations. Select **Refresh results** after a probe cycle. A check with no sample is waiting. A check whose last sample is too old is stale. A failed check has a result and shows its reason when available. The service summary uses the current endpoint, check profile and locations. Each new monitor has its own versioned target set and can be reverted from **Configuration**. Existing rules evaluate the results. Public status pages show the service when explicitly configured to include it.

## Add a probe location {#locations}

A location is one machine running `beacon-probe agent`. Add it to the catalogue first, then install the probe there. Enrolling an id that the catalogue lacks fails.

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "https://status.example.com/api/v1/config/locations/edge-2?note=new+node" \
  -d '{"name": "Germany - Frankfurt", "region": "overseas", "network": "BGP", "probe": {"timeout": "30s"}}'
```

You will see the new version and the item, and the change is echoed to the alert chat.

The gateway labels everything a probe writes with `location`, `region`, `network` and `host` from the catalogue. These take the place of anything the probe says about itself.

- `region` (`overseas` or `mainland`) lets the rules tell a path problem from an outage.
- `name` is public, shown on the [status pages](status-pages.md). Name locations by place and network, never by vendor.

Every field of the entry is in the catalogue reference: [The catalogue](../reference/catalogue.md).

## Add a target set {#targets}

A target is what gets checked: a URL, a `host:port`, a registry repository or a domain. Targets come in named sets, and the set's name says which locations probe it.

A set is a list of groups in the format of Prometheus file service discovery. Each group has its targets and the labels they share:

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/config/targets/all-web \
  -d '[{"targets": ["https://example.com/healthz"], "labels": {"module": "http_head", "service": "example.com"}}]'
```

- `module` names a module. `service` says what the result belongs to: alerts, cards and status pages speak of services. Both are required.
- `severity` (`critical`, `warning` or `info`) becomes the alert's level.
- `failure_domain` names what fails together, so related alerts show side by side in the [evidence](on-call.md#the-evidence).
- The name decides who probes it: `all-…` every location, `overseas-…` and `mainland-…` those regions, `<location id>-…` that location only. Anything else is refused, so a typo cannot silently stop a check.

A probe picks up the change on its next fetch, within a minute. `GET /api/v1/config/targets` shows every set with who probes it (`probed_by`).

## Check a target by hand

A failed check says why, in `probe_failure_info{reason}`: `dns`, `connect`, `timeout`, `tls`, `not_ssl`, `http_status`, `body` and a few more.

To try a module once before handing it to every probe, run it on any machine with `beacon-probe`:

```bash
beacon-probe run -modules modules.yml http_head https://example.com/healthz
```

You will see the result's metrics, and on failure `failed: <reason>: <detail>`. When a target always fails from one location only, [A target always fails](../self-hosting/troubleshooting.md#a-target-always-fails) walks the reasons.

## Modules

A module says how to check. The reference deployment ships `http_head`, `http_get_200`, `http_get_healthy`, `tcp_tls` and `rdap_domain`.

- **The format.** The file format is the [Blackbox Exporter's](https://github.com/prometheus/blackbox_exporter/blob/master/CONFIGURATION.md) for `http` and `tcp`, plus Beacon's own `registry`, `git` and `rdap` probers.
- **The timeout.** Every module has a `timeout`, 10 seconds by default. A location's `probe.timeout` can lengthen it.
- **Passwords.** `password_file` names a file in the probe's secrets directory (`/etc/beacon-probe/secrets` by default), by name only. A value with a path in it is refused with `config.invalid`, so `config:write` cannot have a probe read any file on its machine.

Put a module through the same `/api/v1/config/modules/{name}` route. Every field of every prober is in the catalogue reference: [The catalogue](../reference/catalogue.md).

## Change it without stepping on anyone

Every change, whatever it comes through, is checked as the files always were. One that would not pass is refused with `config.invalid`, and nothing changes.

Changes take `config:write`. Give it only to those you would let edit these files ([Scopes](../reference/api.md#scopes)).

1. **Say which version you are changing.** An agent that reads, thinks and then writes should pass `if_version`. It then never overwrites what someone changed meanwhile:

   ```bash
   curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
     "https://status.example.com/api/v1/config/targets/all-web?if_version=12" \
     -d '[{"targets": ["https://example.com/healthz", "https://www.example.com/"], "labels": {"module": "http_head", "service": "example.com"}}]'
   ```

   If a later version is in force, the change is refused with `config.conflict`. Read again and decide afresh.

2. **Read the history, revert a change.** Every change is a version, with who made it, through what, and the note. The latest 10,000 are kept:

   ```bash
   docker exec beacon beacon config history
   docker exec beacon beacon config revert 12
   ```

   A revert is refused with `config.conflict` when a later change touched one of the same items. Revert that one first.

## Files, import and export

The catalogue, the target sets and the module file also exist as files, for import and export: `catalog/*.yaml`, `targets/*.yaml` and `targets/modules.yml`.

An import makes the files the whole configuration. **Items the files do not hold are removed**, so always look first:

```bash
docker exec beacon beacon config import /etc/beacon -dry-run
docker exec beacon beacon config import /etc/beacon -note "ops repository at 4f2c1e0"
```

`config export DIR` writes what is in force back into a directory. Pick one way of working and keep to it:

- either the files are the source, and every change is an import;
- or Beacon is the source, and the files are an export.

When Beacon starts with an empty database, it imports the files once. After that, a `SIGHUP` imports them only when nothing was changed through Beacon since the last import ([Configuration](../self-hosting/configuration.md)).

## Heartbeats {#heartbeats}

Jobs that must report in, and things that expire, are watched by their dates rather than probed: [Heartbeats and expiring resources](heartbeats.md).

## Check it

A minute after the target set goes in, query what the probes sent. Use VictoriaMetrics on the centre, at `http://127.0.0.1:8428/vmui`:

```
probe_success{service="example.com"}
```

You will see one line per location, each 1 while the service answers.

With the reference rules, a service counts as down only when, after 3 minutes:

- at least two locations fail it, and
- they are at least half of those reporting.

So a check from one location alone never pages anyone ([How Beacon works](concepts.md#an-alerts-path)).

When it does not work:

- [A probe location shows no data](../self-hosting/troubleshooting.md#a-probe-location-shows-no-data)
- [A target always fails](../self-hosting/troubleshooting.md#a-target-always-fails)
- [An import or SIGHUP did not change the configuration](../self-hosting/troubleshooting.md#an-import-or-sighup-did-not-change-the-configuration)
- [A change is refused with config.conflict or config.invalid](../self-hosting/troubleshooting.md#a-change-is-refused-with-configconflict-or-configinvalid)

## Next

- [Heartbeats and expiring resources](heartbeats.md): the jobs and the expiry dates Beacon watches by their dates.
- [Publish a status page](status-pages.md): what the probes agree on, public.
- [Put an agent on call](on-call.md): who the resulting alerts wake.
