# Upgrades

Moving a running centre from one release to the next, and back again, is one line in `.env` and a run of `install.sh`.

The script pulls the new image and validates everything with it. Then it recreates the Beacon container and waits for health. The state, the secrets and the configuration stay where they were.

The examples assume the reference deployment from [On a server](install.md), run from `/opt/beacon` on the centre.

## What an upgrade touches, what it keeps

| Changes | Stays |
| --- | --- |
| The `beacon` container, recreated on the image named by `BEACON_VERSION` in `.env` (`ghcr.io/openlinkos/beacon:vX.Y.Z`) | `beacon.db` and `recordings/` in the `beacon-data` volume: accounts, agents and tokens, the catalogue, probe targets and modules (they live in that database), alert groups, the audit log |
| VictoriaMetrics, vmalert and Caddy, only when you take a new `compose.yaml`: their images are pinned by digest and move with the repository | `/opt/beacon`: `config.yaml`, `secrets/`, `catalog/`, `targets/` are yours; the upgrade does not write them |
| | The `victoriametrics-data` and `caddy-data` volumes |

Beacon runs no migrations you have to start. It opens the same `beacon.db` and adds what it misses.

While it upgrades:

- **The interruption is the container being recreated.** That takes seconds, and ends open SSH sessions. What they did is on record up to that point ([what the channel guarantees](security.md#the-ssh-channel)).
- **No probe results are lost.** While the gateway is down, each probe queues its results on disk, up to 64 MB, and sends them when it answers again.
- **Alert groups survive the restart.** Firing alerts are still in `list_alerts` after it.

### From a version before the SQL database

0.3.0-rc.16 and earlier keep their state in a bbolt file, `status.db`. The first start of a newer version moves that file into `beacon.db`, in one transaction. It checks every bucket's count before it commits, and renames the old file `status.db.pre-sql`. On the reference deployment's state it takes milliseconds.

To go back:

1. Stop Beacon.
2. Rename `status.db.pre-sql` to `status.db`.
3. Delete `beacon.db` with its `-wal` and `-shm` files.
4. Start the older version.

The older version then has everything up to the upgrade, and nothing after.

## Pick a version

- The [changelog](../changelog.md) says what each release changes. The [releases page](https://github.com/openlinkos/beacon/releases) lists the tags. Pre-releases carry `-rc.N`.
- Only the latest minor release and its release candidates receive fixes, as the [security policy](https://github.com/openlinkos/beacon/blob/main/SECURITY.md) says. Move to the newest tag rather than an older minor.

## Upgrade the centre

1. **Back up the state first**: the four steps in [Backup and restore](backup.md#back-up), a minute's work. An upgrade is a safe moment precisely because you can undo it.

2. **Take the deployment files of the release.** This is recommended: the pinned digests, the rules and the runbooks move with the repository. Keep a copy of `/opt/beacon` for the way back. Then, in the clone you installed from:

   ```bash
   sudo cp -a /opt/beacon /opt/beacon.bak
   cd beacon/deploy
   git fetch --tags && git checkout vX.Y.Z        # or: git pull, to follow main
   sudo cp -a install.sh compose.yaml Caddyfile .env.example /opt/beacon/
   sudo cp -a metrics rules runbooks probe /opt/beacon/
   ```

   `config.yaml`, `catalog/` and `targets/` are yours, and are not copied over. If the release changed `config.yaml` itself, `git diff vOLD..vX.Y.Z -- config.yaml` shows how. Carry a new key into yours by hand. Should the new release want a key yours does not have, the installer's check stops and names it before anything is restarted.

3. **Raise the version** in `.env`:

   ```bash
   cd /opt/beacon
   sudo sed -i 's/^BEACON_VERSION=.*/BEACON_VERSION=vX.Y.Z/' .env
   ```

4. **Run the installer**, which is safe at any time:

   ```bash
   sudo ./install.sh
   ```

   With the new tag, it:

   - pulls the new image;
   - validates the compose file, the Caddyfile, the rules and Beacon's own configuration with the new image;
   - recreates the Beacon container with `docker compose up -d`;
   - waits until all four containers are healthy and the rule engine's first alert has reached the gateway.

   A file that does not validate stops the upgrade before the running service is touched. You should see it end with `Beacon is up (…s)`, as at the first install.

5. **Check** the version, the containers and the state:

   ```bash
   docker exec beacon beacon version   # beacon vX.Y.Z (…)
   docker compose ps                   # four containers, healthy
   docker exec beacon beacon config history
   ```

   `config history` lists the versions you know, which is the database speaking. To see the whole chain once through, run a drill: `docker exec beacon beacon drill fail 15m`, in [Rehearse an outage](../guide/drills.md).

## Centre first, then the probes

The probe protocol is shared by releases in both directions:

- an older `beacon-probe` keeps working against a newer centre;
- a newer probe against an older centre does without what it has not heard of.

So upgrade the centre first, and the probe locations whenever convenient. [Probe locations](probes.md) has the command: `sh probe/install.sh -gateway … -version vX.Y.Z`. It is idempotent, and restarts the service only if the binary changed.

## Roll back

1. **Put the previous tag back** and run the installer again:

   ```bash
   sudo sed -i 's/^BEACON_VERSION=.*/BEACON_VERSION=vOLD/' .env
   sudo ./install.sh
   ```

2. **The database is no obstacle.** Beacon has no migrations, and records written by the newer release keep fields the older one simply does not read. Nothing is rewritten on the way back.
3. **Put back the deployment files**, if you also took new ones in step 2. The copy made there restores them: `sudo cp -a /opt/beacon.bak/. /opt/beacon/`.
4. **Beyond that**, [restore from the backup](backup.md#restore).

## What next

- [Backup and restore](backup.md): what to copy, how often, and how to pour it back.
- [Troubleshooting](troubleshooting.md) when an upgrade does not end with `Beacon is up`.
