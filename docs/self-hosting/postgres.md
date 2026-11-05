# PostgreSQL, and high availability

Beacon's state lives in SQLite by default, and for most deployments that is the right answer: one file, no second service to run, online backups with `VACUUM INTO`, and Beacon's own [backup](backup.md) on top. This page is about the other case — when PostgreSQL is the better home for the state, and what it buys: a standby that takes over, point-in-time recovery, and hosted databases.

Performance is not a reason to switch. Beacon writes a few dozen times a second at most, far below what SQLite does; at our scale PostgreSQL brings operations, not speed.

## When PostgreSQL

| You are… | Take |
| --- | --- |
| One machine, one small team | SQLite, with Beacon's built-in backup; add Litestream (below) if a second-level recovery point is wanted |
| Already running PostgreSQL, or wanting a hosted database, or point-in-time recovery, or a standby | PostgreSQL |
| Hoping for faster writes | Neither: this is not where the time goes |

## Moving the state

The driver and its data source name go in `config.yaml` — a `dsn_file` path under `secrets/`, never the DSN itself ([config.yaml](../reference/config.md)):

```yaml
storage:
  driver: postgres
  dsn_file: secrets/postgres-dsn
```

PostgreSQL 14 or newer; the connection pool and statement timeouts are keys of the same block. A Beacon that starts with an SQLite database and a PostgreSQL driver beside it migrates in one pass: `beacon storage migrate -to postgres` copies every table and the event log across in one transaction, checks the counts, and leaves the SQLite file where it was. The other direction works the same way. `beacon storage check` reports the health of whichever database is in use — the integrity check, what each bucket holds, the audit chain's verification.

Everything Beacon stores tests against both databases; the same key-value and event interfaces carry the state either way ([Architecture](../about/architecture.md#where-state-lives)).

## A standby beside the primary

High availability, where it applies, is one primary and one standby — not two active centres. The SSH listener, the chat's long connection and the alert router must each be single-headed, so only one instance is active at a time, and the database decides which:

- A **lease** in the database. The pair that runs is `ha.renew` (5 seconds) and `ha.expire` (15 seconds) in [config.yaml](../reference/config.md). Settings also register `storage.lease.renew` and `storage.lease.expires` with the same numbers; the integration will keep one source.
- The standby holds the lease's door shut and answers `/healthz` with `503`, so a load balancer or a VIP in front sends every request to the primary. `/livez` stays `200` on a waiting standby, so Docker's health check does not restart it; a partitioned instance fails `/livez`.
- **Recordings** live on S3-compatible storage both instances can read and write ([configuration](configuration.md)); alerts, groups and the router's state were in the database all along.
- A failover takes about as long as the lease takes to expire: kill the primary, and the standby answers inside those 15 seconds.

## Continuous replication

For a recovery point measured in seconds on SQLite, [Litestream](https://litestream.io/) streams the write-ahead log to S3-compatible storage continuously; the reference deployment's compose file shows it as an optional service. Restoring is a matter of bringing the database back from the stream onto the standby's disk and starting Beacon on it; from v0.5, point-in-time recovery follows the same stream. With PostgreSQL, the same job belongs to the database: pgBackRest or WAL-G do physical backup and PITR, and Beacon's own backup then carries the recordings, the configuration and the secrets directory only ([backup](backup.md)).

Metrics history — VictoriaMetrics — is not part of either database's backup; `vmbackup` is its tool, and the reference deployment's notes show it.

## What next

- [Backup and restore](backup.md), for what is backed up in each mode and how a restore is rehearsed.
- [Configuration](configuration.md), for the `storage:` block and the recordings' S3 settings.
- [Upgrades](upgrade.md), which migrate the database forward before the new release serves.
