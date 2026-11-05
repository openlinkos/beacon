# Backup and restore

What a Beacon centre's backup must hold, where each piece lives, and the commands to take it and pour it back. Beacon takes its own backup on a schedule — encrypted, verified, watched by a heartbeat — and this page starts with it; the sections after it are the by-hand path for the pieces Beacon does not carry (the metrics, chiefly) and for deployments that prefer their own tools. The examples assume the reference deployment from [On a server](install.md), run from `/opt/beacon` on the centre as root.

With a `backup:` destination in the configuration, Beacon takes its own encrypted backups (`beacon backup now | ls | restore | drill | keygen`; [the command line](../reference/cli.md), [Settings](../reference/settings.md#backup-and-storage), [config.yaml](../reference/config.md)). Those archives are age-encrypted and the manifest is signed; restoring into the live data directory needs `-confirm`. A single S3 PUT is at most 5 GiB. The optional Litestream service in the reference deployment ships the WAL continuously, but **that replica is plaintext** — treat it as a hot copy behind the store's access control, not as the secret off-site copy. If no recipient key is given at initialisation, Beacon makes a pair and writes the private half once to `<data_dir>/backup/RECOVERY-MATERIAL.txt` (mode 0600), with a log reminder until you take it off; `beacon backup keygen` prints a new private key once.

## What lives where

| Piece | Where | If it is lost |
| --- | --- | --- |
| Beacon's state | The `beacon-data` volume, at `/var/lib/beacon` in the `beacon` container: `beacon.db`, the SQL database (SQLite by default; [PostgreSQL](postgres.md) optional), and `recordings/`, the sessions' recordings (asciicast v3) and transcripts, a directory per day | The accounts, agents and tokens, the catalogue, probe targets and modules (they live in the database, not only in the files), alert groups and their notes, the audit log, the index of sessions, the recordings, the vault and its grants |
| The metrics | The `victoriametrics-data` volume, at `/storage` in the `beacon-victoriametrics` container; 400 days of retention | The history behind the status pages' 90-day bars and the evidence's probe windows |
| The certificates | The `caddy-data` volume | Nothing that matters: Caddy asks Let's Encrypt again |
| The deployment | `/opt/beacon`: `.env`, `config.yaml`, `secrets/`, `catalog/`, `targets/`, `rules/` | Your configuration, and the SSH channel's keys |

## The built-in backup

Beacon backs itself up on a schedule, and the backup is written so that losing it costs nothing but shame: encrypted to *your* public keys, verified before it leaves and read back after, and alarmed over when it fails or is late.

- **What goes in**: a consistent snapshot of the database taken online (SQLite's `VACUUM INTO`; under PostgreSQL the database's own backup carries that part — pgBackRest or WAL-G — and Beacon's backup then holds the recordings, the configuration and the secrets directory, with an optional daily `pg_dump` inside), the recordings incrementally, the configuration directory, and the secrets directory with the root key in it.
- **The shape**: `beacon-<host>-<time>-<full|incr>.tar.zst.age`, and inside it a `manifest.json` naming the versions, the tables' counts, the audit chain's head hash and the SHA-256 of every member.
- **The encryption**: age, to the recipients in `backup.recipients` — age or ssh-ed25519 public keys, as many as you like. The server holds only public keys and cannot read its own backup. If no key is given at initialization, Beacon makes a pair and shows the private key once, with the root key beside it ([the recovery material](security.md#what-the-operator-carries)).
- **Where it goes**: a local directory, an S3-compatible store (MinIO, R2, S3), or both at once. The destination's credentials live as files in the secrets directory, not in the vault — at restore time the vault is not open yet.
- **When**: the database hourly, the recordings and the configuration daily, and always before an upgrade or a migration; `beacon backup now` takes one at any moment, `beacon backup ls` lists what is kept.
- **How long it is kept**: grandfather-father-son — 24 hourlies, 14 dailies, 8 weeklies, 12 monthlies, about a year deep. A backup that satisfies any rule stays.
- **That it worked**: an integrity check and the counts and chain check run before the archive is encrypted; after upload the file is read back and its size and hash compared. A failure retries three times with growing gaps, then alarms (`backup.on_failure` in config.yaml is `alert` only; `alert_and_pause` is rejected). Settings also register `backup.on_failure` as `alert` / `alert_pause_use`.
- **That it happened at all**: a heartbeat named `beacon-backup`, watched like any [heartbeat](../guide/heartbeats.md) — a backup two hours late raises `HeartbeatStale` through the ordinary alerting.

## Restoring, and rehearsing it

`beacon backup restore FILE -identity KEY` stops the service, opens the archive with the private key, puts the database, the configuration and the recordings back, verifies the audit chain, and starts Beacon again; `--dry-run` does all of it into a temporary directory and reports, touching nothing. The target: one command, ten minutes.

The server cannot read its own backup, so a full restore is rehearsed elsewhere: `beacon backup drill` on another machine restores to a temporary directory, verifies, and reports the time of the drill back to Beacon. The catalogue carries a *recovery drill* resource that expires every 90 days and reminds you when the last rehearsal grows old — an untested backup is a hope, not a backup.

## Back up

Two things about the secrets, which `install.sh` makes on the machine and which never leave it: a backup that holds them must itself be protected — restricted to root, or encrypted off the machine. And losing `ssh-ca-key` means machines stop accepting agents until each is given the new CA's public key: run `beacon machine add NAME HOST` again and apply the four lines it prints on the machine ([On a server](install.md#3-the-first-machine)).

Beacon's own image is distroless — no shell, no tar — so a helper container reads each volume, through the stopped container that owns it. Beacon keeps the database open and locked while it runs, and a copy taken underneath a writing process is not guaranteed to be whole, hence the stops. They cost seconds: while the gateway is down, each probe queues its results on disk and sends them when it answers again, and open SSH sessions end (with what they did on record).

1. **Beacon's state and recordings** — a few seconds:

   ```bash
   sudo install -d -m 0700 /opt/beacon-backups
   cd /opt/beacon
   docker compose stop beacon
   sudo docker run --rm --volumes-from beacon -v /opt/beacon-backups:/backup alpine \
     tar -C /var/lib/beacon -czf /backup/beacon-$(date +%F).tgz beacon.db recordings
   ```

2. **The metrics**, the same way, as long as the copy takes (this is the big one):

   ```bash
   docker compose stop victoriametrics
   sudo docker run --rm --volumes-from beacon-victoriametrics -v /opt/beacon-backups:/backup alpine \
     tar -C /storage -czf /backup/victoriametrics-$(date +%F).tgz .
   ```

3. **The deployment**, with its secrets:

   ```bash
   sudo tar -C /opt -czf /opt/beacon-backups/deployment-$(date +%F).tgz beacon
   ```

4. **Back up:**

   ```bash
   docker compose up -d
   docker compose ps        # beacon, beacon-victoriametrics, beacon-vmalert, beacon-caddy: healthy
   ```

How often: the state nightly — the four commands are one shell script in cron — the deployment whenever it changes, the metrics as often as you care about their history. If you decide the metrics are dispensable, the state alone brings the centre back; the status pages then start their history over, grey.

## Restore

Onto the same machine with an emptied volume, or onto a fresh one.

1. **Install the centre** as far as step 1 of [On a server](install.md) (the clone, and the copy to `/opt/beacon`), then unpack the deployment backup over it and run the installer:

   ```bash
   sudo tar -C /opt -xzf /opt/beacon-backups/deployment-YYYY-MM-DD.tgz
   cd /opt/beacon && sudo ./install.sh
   ```

   `install.sh` makes only the secrets that are missing, so the restored `ssh-host-key` and `ssh-ca-key` stay, and agents' access to machines survives the rebuild. It starts everything on empty volumes; the next step pours the state in.

2. **The state and recordings**:

   ```bash
   docker compose stop beacon
   sudo docker run --rm --volumes-from beacon -v /opt/beacon-backups:/backup alpine \
     tar -C /var/lib/beacon -xzf /backup/beacon-YYYY-MM-DD.tgz
   docker compose up -d
   ```

   Over a running Beacon this is impossible: the database is locked while the service runs. `ctl.sock` in the same directory is the service's admin socket; the restore need not carry it.

3. **The metrics**, likewise with `--volumes-from beacon-victoriametrics` and `tar -C /storage -xzf /backup/victoriametrics-YYYY-MM-DD.tgz`.

4. **Check.**

   - Sign in to the console with the account you had; the alerts, silences and hooks are as they were.
   - `docker exec beacon beacon config history` lists the versions you remember.
   - A session from before, under the console's **Sessions**, still opens its replay.
   - The probe locations need nothing: their tokens are unchanged, and they queued while the centre was down; the overview counts them reporting again within a minute.

## The catalogue, as files

The catalogue is inside the database, so every backup of the state holds it. To also keep the in-force configuration as files — for git, or to move it between centres:

```bash
docker exec beacon beacon config export /var/lib/beacon/export
sudo docker cp beacon:/var/lib/beacon/export /opt/beacon/export-$(date +%F)
```

`/etc/beacon` inside the container is mounted read-only, so the export goes into the data directory and is copied out from there. Importing is `beacon config import DIR`, dry-run first; [Configuration](configuration.md#the-catalogue-files-at-start-and-on-sighup) has the whole of it.

## What next

- [Upgrades](upgrade.md), which begin with a backup.
- [Security model](security.md) for what a backup holds besides data: the keys to your machines.
