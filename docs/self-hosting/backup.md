# Backup and restore

This page says what a Beacon centre's backup must hold, and where each piece lives. It gives the commands to take a backup and to pour it back.

A restore onto a fresh machine is: install, unpack the deployment, pour the volumes back.

The examples assume the reference deployment from [On a server](install.md), run from `/opt/beacon` on the centre as root.

## What lives where

| Piece | Where | If it is lost |
| --- | --- | --- |
| Beacon's state | The `beacon-data` volume, at `/var/lib/beacon` in the `beacon` container: `beacon.db`, one SQLite database (with its `-wal` and `-shm` files while it runs), and `recordings/`, the sessions' recordings (asciicast v3) and transcripts, a directory per day | The accounts, agents and tokens, the catalogue, probe targets and modules (they live in the database, not only in the files), alert groups and their notes, the audit log, the index of sessions, the recordings |
| The metrics | The `victoriametrics-data` volume, at `/storage` in the `beacon-victoriametrics` container; 400 days of retention | The history behind the status pages' 90-day bars and the evidence's probe windows |
| The certificates | The `caddy-data` volume | Nothing that matters: Caddy asks Let's Encrypt again |
| The deployment | `/opt/beacon`: `.env`, `config.yaml`, `secrets/`, `catalog/`, `targets/`, `rules/` | Your configuration, and the SSH channel's keys |

Two things about the secrets, which `install.sh` makes on the machine and which never leave it:

- **A backup that holds them must itself be protected**: restricted to root, or encrypted off the machine.
- **Losing `ssh-ca-key` means machines stop accepting agents** until each is given the new CA's public key. Run `beacon machine add NAME HOST -replace` again, and apply the four lines it prints on the machine ([On a server](install.md#3-the-first-machine)).

## Back up

Beacon's own image is distroless, with no shell and no tar. So a helper container reads each volume, through the stopped container that owns it.

Beacon keeps `beacon.db` open and locked while it runs. A copy taken underneath a writing process is not guaranteed to be whole, hence the stops. Stopped cleanly, everything is in `beacon.db` itself.

The stops cost seconds. While the gateway is down, each probe queues its results on disk and sends them when it answers again. Open SSH sessions end, with what they did on record.

1. **Back up Beacon's state and recordings.** It takes a few seconds:

   ```bash
   sudo install -d -m 0700 /opt/beacon-backups
   cd /opt/beacon
   docker compose stop beacon
   sudo docker run --rm --volumes-from beacon -v /opt/beacon-backups:/backup alpine \
     sh -c 'cd /var/lib/beacon && tar -czf /backup/beacon-$(date +%F).tgz beacon.db* recordings'
   ```

2. **Back up the metrics** the same way. It takes as long as the copy does: this is the big one.

   ```bash
   docker compose stop victoriametrics
   sudo docker run --rm --volumes-from beacon-victoriametrics -v /opt/beacon-backups:/backup alpine \
     tar -C /storage -czf /backup/victoriametrics-$(date +%F).tgz .
   ```

3. **Back up the deployment**, with its secrets:

   ```bash
   sudo tar -C /opt -czf /opt/beacon-backups/deployment-$(date +%F).tgz beacon
   ```

4. **Start everything again:**

   ```bash
   docker compose up -d
   docker compose ps        # beacon, beacon-victoriametrics, beacon-vmalert, beacon-caddy: healthy
   ```

How often:

- **The state**: nightly. The four commands are one shell script in cron.
- **The deployment**: whenever it changes.
- **The metrics**: as often as you care about their history.

If you decide the metrics are dispensable, the state alone brings the centre back. The status pages then start their history over, grey.

## Restore

Onto the same machine with an emptied volume, or onto a fresh one.

1. **Install the centre** as far as step 1 of [On a server](install.md): the clone, and the copy to `/opt/beacon`. Then unpack the deployment backup over it, and run the installer:

   ```bash
   sudo tar -C /opt -xzf /opt/beacon-backups/deployment-YYYY-MM-DD.tgz
   cd /opt/beacon && sudo ./install.sh
   ```

   `install.sh` makes only the secrets that are missing. So the restored `ssh-host-key` and `ssh-ca-key` stay, and agents' access to machines survives the rebuild. It starts everything on empty volumes; the next step pours the state in.

2. **Pour back the state and recordings:**

   ```bash
   docker compose stop beacon
   sudo docker run --rm --volumes-from beacon -v /opt/beacon-backups:/backup alpine \
     tar -C /var/lib/beacon -xzf /backup/beacon-YYYY-MM-DD.tgz
   docker compose up -d
   ```

   Over a running Beacon this is impossible: the database is locked while the service runs. `ctl.sock` in the same directory is the service's admin socket. The restore need not carry it.

3. **Pour back the metrics** likewise, with `--volumes-from beacon-victoriametrics` and `tar -C /storage -xzf /backup/victoriametrics-YYYY-MM-DD.tgz`.

4. **Check.**

   - Sign in to the console with the account you had. The alerts, silences and hooks are as they were.
   - `docker exec beacon beacon config history` lists the versions you remember.
   - A session from before, under the console's **Sessions**, still opens its replay.
   - The probe locations need nothing. Their tokens are unchanged, and they queued while the centre was down. The overview counts them reporting again within a minute.

## The catalogue, as files

The catalogue is inside `beacon.db`, so every backup of the state holds it. To also keep the configuration in force as files, for git or to move it between centres:

```bash
docker exec beacon beacon config export /var/lib/beacon/export
sudo docker cp beacon:/var/lib/beacon/export /opt/beacon/export-$(date +%F)
```

`/etc/beacon` inside the container is mounted read-only. So the export goes into the data directory, and is copied out from there.

Importing is `beacon config import DIR`, dry-run first. [Configuration](configuration.md#the-catalogue-files-at-start-and-on-sighup) has the whole of it.

## What next

- [Upgrades](upgrade.md), which begin with a backup.
- [Security model](security.md) for what a backup holds besides data: the keys to your machines.
