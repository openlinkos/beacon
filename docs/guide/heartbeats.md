# Heartbeats and expiring resources

By the end of this page a scheduled job of yours — a backup, a cleanup — must report success often enough or an alert fires, and the things that expire — a domain, a rented machine — warn you before they lapse. Neither is probed: Beacon watches their dates. This page follows [Monitor a service](monitoring.md), which covers targets and modules.

You need Beacon running, a job to watch, and a credential with `config:write` (the console's **Configuration** page, or a token).

## Make a heartbeat

1. **List the job in the catalogue.** `max_age` is how often it must succeed (at least a minute), `severity` the alert's level:

   ```bash
   curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
     https://status.example.com/api/v1/config/heartbeats/nas-backup \
     -d '{"max_age": "26h", "severity": "warning", "description": "Nightly backup of the NAS"}'
   ```

   You will see the new version and the item. When the job is late, `HeartbeatStale` fires; when a listed heartbeat has never reported (after a day), `HeartbeatMissing`.

2. **Make the job report.** On a machine with the probe agent, end the job with the `beacon-heartbeat` script the reference deployment installs — it writes one sample into the directory the agent reads:

   ```bash
   restic backup … && beacon-heartbeat nas-backup
   ```

   A job elsewhere pushes one sample with a token of its own, made on the centre with `beacon enroll heartbeat nas-backup`:

   ```bash
   curl -fsS -H "Authorization: Bearer $TOKEN" --data-binary \
     "beacon_heartbeat_last_success_timestamp_seconds{heartbeat=\"nas-backup\"} $(date +%s)" \
     https://metrics.example.com/api/v1/import/prometheus
   ```

3. **Name its machine**, when the job runs on one the [SSH channel](machines.md) reaches:

   ```bash
   curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
     "https://status.example.com/api/v1/config/heartbeats/nas-backup?note=names+the+machine" \
     -d '{"max_age": "26h", "severity": "warning", "machine": "nas-1", "description": "Nightly backup of the NAS"}'
   ```

   This is what turns a late heartbeat into something an agent can act on: the [evidence](on-call.md#the-evidence) for `HeartbeatStale` then names the machine and lists the sessions and changes on it, those just before the alert included — the session that stopped the job's timer, say. A put refuses a machine the channel does not have, with `config.invalid`; an import or a revert takes one, with a warning, since files are often imported before machines are enrolled.

## Watch things that expire

Names, kinds and dates only; no prices. `ResourceExpiring` warns 30 days before the date and is critical 7 days before:

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/config/resources/example.com \
  -d '{"kind": "domain", "expires": "2027-01-15", "note": "renews automatically"}'
```

`kind` is `vps`, `domain`, `cert`, `token` or `service`. A `vps` named like a probe location also bounds that location: past the date it is no longer expected to report and its token can no longer write; a new date brings it back. Renew the resource or let it lapse on purpose, then put the new date in, so the alert ends.

## Check it

After the job runs once, query what it sent, in VictoriaMetrics on the centre (`http://127.0.0.1:8428/vmui`):

```
beacon_heartbeat_last_success_timestamp_seconds{heartbeat="nas-backup"}
```

One sample with a fresh timestamp. To see the alert side work without waiting a night, put a heartbeat with `max_age: 1m` next to a job that runs every few minutes, and stop the job: `HeartbeatStale` fires within minutes, with the machine in its evidence.

When it does not work: a heartbeat that never reports is a job that does not end with `beacon-heartbeat` (or pushes with the wrong token); a change refused with `config.invalid` is usually a machine the channel does not have — [A change is refused with config.conflict or config.invalid](../self-hosting/troubleshooting.md#a-change-is-refused-with-configconflict-or-configinvalid); an alert that never arrives at all is [An alert never arrives](../self-hosting/troubleshooting.md#an-alert-never-arrives).

## Next

- [Put an agent on call](on-call.md): a stale heartbeat wakes an agent that already knows which machine to check.
- [Monitor a service](monitoring.md): targets and modules, checked from where your users are.
