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
| Team credential policy | The encrypted rows in the state database and `team-credentials/epochs` in Beacon's data directory | The rollback anchor needed to verify the current policy. Keep a protected, independently retained copy of its newest epoch |

When `ssh.recordings` is `s3`, back up the shared bucket as well as the local state of each centre. Locally sealed evidence waiting for upload is under that centre’s `recordings/` directory. Beacon resumes publication for sealed files after a restart. A pending file from an older release without a seal can still be read on its original centre, within the normal session retention.

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
   docker compose stop beacon &&
   sudo docker run --rm --volumes-from beacon -v /opt/beacon-backups:/backup alpine \
     sh -c 'cd /var/lib/beacon || exit 1
       set -- beacon.db*
       if [ -d recordings ]; then set -- "$@" recordings; fi
       if [ -f team-credentials/epochs ]; then set -- "$@" team-credentials/epochs; fi
       tar -czf /backup/beacon-$(date +%F).tgz "$@"'
   ```

   Keep the newest known-good `team-credentials/epochs` in separate protected storage when the [team credential store](../guide/team-credentials.md) is enabled. Update this independent copy after each verified policy change, including revocations, even when the next database backup is later. The key file configured in `credentials.key_file` is a separate backup item.

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

2. **Restore the state and recordings.** If team credentials were enabled, retrieve the newest known-good rollback anchor from its independent storage and choose a database backup that matches it or is newer. The archive can predate later revocations. The extraction step skips any epoch file bundled with the archive.

   ```bash
   docker compose stop beacon &&
   sudo docker run --rm --volumes-from beacon -v /opt/beacon-backups:/backup:ro alpine \
     tar -C /var/lib/beacon --exclude=team-credentials/epochs -xzf /backup/beacon-YYYY-MM-DD.tgz
   ```

   Leave Beacon stopped. The database is locked while the service runs. The restore does not need `ctl.sock`, the service's admin socket.

   **If team credentials were enabled**, put the independently retained anchor at `/opt/beacon-backups/team-credential-epochs` with root-only access. Restore it into the volume before starting Beacon. This gives the service's container user access to read and update it.

   ```bash
   sudo docker run --rm --volumes-from beacon -v /opt/beacon-backups:/backup:ro alpine \
     sh -ec 'test -s /backup/team-credential-epochs
       install -d -m 0700 -o 65532 -g 65532 /var/lib/beacon/team-credentials
       install -m 0600 -o 65532 -g 65532 /backup/team-credential-epochs /var/lib/beacon/team-credentials/epochs
       sync'
   ```

   If the latest known-good anchor or a matching database is unavailable, set `enabled: false` under `credentials` in `config.yaml` before starting Beacon. Monitoring and SSH can continue. Recover the upstream bearer tokens before delegating again. PostgreSQL deployments using one Beacon centre also need the independently retained anchor and external key alongside their database backup.

   **Start Beacon** after restoring the independent anchor and its matching state, or with team credentials disabled.

   ```bash
   docker compose up -d
   ```

3. **Pour back the metrics** likewise, with `--volumes-from beacon-victoriametrics` and `tar -C /storage -xzf /backup/victoriametrics-YYYY-MM-DD.tgz`.

4. **Check.**

   - Sign in to the console with the account you had. The alerts, silences and hooks are as they were.
   - `docker exec beacon beacon config history` lists the versions you remember.
   - A session from before, under the console's **Sessions**, still opens its replay.
   - If team credentials were restored, open **Team credentials** as an owner and send a test request to an approved service. Confirm the expected grants and verify that an agent whose grant was revoked is still denied.
   - The probe locations need nothing. Their tokens are unchanged, and they queued while the centre was down. The overview counts them reporting again within a minute.

The Linux reference deployment smoke test restores a stopped SQLite service into a fresh volume. It checks a new owner sign-in, the incident note and SSH replay, and that a revoked agent remains unable to use the credential after the independent anchor is restored.

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
