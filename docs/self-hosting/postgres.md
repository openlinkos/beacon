# Run on PostgreSQL

Beacon keeps its state in SQLite by default: one file, no service to run, and enough for any team. This page moves that state to PostgreSQL, and back if need be.

Move it to PostgreSQL when:

- you already run PostgreSQL and want Beacon's state with the rest;
- you want a managed database or point-in-time recovery;
- you want the [primary/standby pair](#a-primary-and-a-standby).

Performance is not a reason to. The state is the same either way, in the same tables: accounts, agents and tokens, the catalogue, alert groups, the audit log and its chain.

The examples assume the reference deployment from [On a server](install.md), run from `/opt/beacon` as root.

## Prepare the database {#prepare-the-database}

Beacon needs PostgreSQL 14 or later, and a database and a role of its own. It makes its tables itself on the first start.

```sql
CREATE ROLE beacon LOGIN PASSWORD 'a long random password';
CREATE DATABASE beacon OWNER beacon;
```

**If you have no PostgreSQL yet**, one beside Beacon in the same compose file does. Its port stays on the compose network, and its data gets a volume of its own:

```yaml
services:
  postgres:
    container_name: beacon-postgres
    image: postgres:17-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: beacon
      POSTGRES_DB: beacon
      POSTGRES_PASSWORD_FILE: /run/secrets/postgres-password
    volumes:
      - postgres-data:/var/lib/postgresql/data
      - ./secrets/postgres-password:/run/secrets/postgres-password:ro
    healthcheck:
      test: [CMD, pg_isready, -U, beacon, -d, beacon]
      interval: 10s
    mem_limit: 512m

volumes:
  postgres-data:
```

**Put the connection string in a file.** Beacon reads it from a file, never from its YAML, and never writes it to a log or an error. Put it in the secrets directory, readable by Beacon's user (uid 65532) alone, like the other secrets:

```bash
cd /opt/beacon
printf 'postgres://beacon:%s@postgres:5432/beacon?sslmode=disable\n' "$PASSWORD" > secrets/postgres-dsn
chown 65532:65532 secrets/postgres-dsn && chmod 0400 secrets/postgres-dsn
```

`sslmode=disable` suits a database on the same compose network, whose traffic never leaves the machine. A database elsewhere takes `sslmode=verify-full`.

## Move the state {#move-the-state}

The move copies the SQLite database into the empty PostgreSQL one, in a single transaction. It checks the counts before it commits, and adds a record to the audit chain noting the import. The SQLite file is left as it was.

Stop Beacon for the move: what the service would write after the copy stays in SQLite. While it is down, each probe queues its results on disk and sends them when the gateway answers again.

1. **Name the database** in `config.yaml`, leaving the driver at `sqlite` for now:

   ```yaml
   storage:
     driver: sqlite
     dsn_file: /etc/beacon/secrets/postgres-dsn
   ```

2. **Stop Beacon and move:**

   ```bash
   docker compose stop beacon
   docker compose run --rm --no-deps beacon storage migrate -to postgres -config /etc/beacon/config.yaml
   ```

   It answers `moved /var/lib/beacon/beacon.db into postgres postgres:5432/beacon: N entries across the buckets, the audit log newest …`. A PostgreSQL database that holds anything already is refused, so a second run copies nothing.

3. **Switch the driver** to `postgres` in `config.yaml` and start Beacon:

   ```bash
   docker compose up -d beacon
   ```

## Check it {#check-it}

```bash
docker exec beacon beacon storage check -config /etc/beacon/config.yaml
docker exec beacon beacon audit verify -config /etc/beacon/config.yaml
```

The first command reads every table and verifies the audit chain. The second walks the chain. Both should report it whole, with the import's record the newest.

Then sign in to the console. The accounts, the alerts and the configuration's history are there as they were.

## Roll back {#roll-back}

Set `storage.driver` back to `sqlite` and restart Beacon. It serves the SQLite file as it was at the move. What was written to PostgreSQL since is not in it.

## Back up {#back-up}

The state is now in PostgreSQL, so the nightly backup of [Backup and restore](backup.md) takes a dump instead of the SQLite file. `pg_dump` reads a consistent snapshot while Beacon runs, so nothing stops:

```bash
docker compose exec -T postgres pg_dump -U beacon -Fc beacon > /opt/beacon-backups/beacon-$(date +%F).dump
```

The `beacon-data` volume still holds the sessions' recordings, unless they are in a bucket (`ssh.recordings: s3`). Back it up as before.

To restore:

1. Start Beacon's PostgreSQL empty.
2. `pg_restore -U beacon -d beacon` the dump into it.
3. Start Beacon.

## A primary and a standby {#a-primary-and-a-standby}

On PostgreSQL, two Beacon instances on two machines can share the state. The one holding a lease in the database is the primary. The other takes over when the lease expires, about 15 seconds after the primary stops renewing it.

Both instances need:

- the same configuration;
- the same secrets;
- the recordings in one bucket.

The `ha` and `ssh.recordings` keys are in the [configuration reference](../reference/config.md). The load balancer in front sends traffic to whichever answers `/healthz` with 200.

## What next {#what-next}

- [Backup and restore](backup.md): the rest of what a backup holds.
- [Upgrades](upgrade.md): moving to a new version, on either database.
