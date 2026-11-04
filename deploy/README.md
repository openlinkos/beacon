# The reference deployment

Beacon on one machine with Docker Compose: `beacon`, VictoriaMetrics, vmalert and Caddy, with generic alert rules and their runbooks, and the probe agent's systemd unit for the machines that probe. It is what [docs/self-hosting/install.md](../docs/self-hosting/install.md) walks through in half an hour, and what the makers' own deployment is built from.

## Prerequisites

- A machine with 2 CPUs, 4 GB of memory and 40 GB of disk, Linux, with Docker Engine and Docker Compose v2 installed, and `ssh-keygen` (the OpenSSH client).
- Two DNS names pointing at it, say `status.example.com` (people and agents: the API, MCP, pages, and the SSH channel on port 17002) and `metrics.example.com` (machines: the gateway). Ports 80 and 443 open from the Internet for Caddy's certificates and the traffic, 17002 for the SSH channel.
- An address Let's Encrypt can write to.

## The one command

```bash
git clone https://github.com/openlinkos/beacon.git && sudo cp -r beacon/deploy /opt/beacon && cd /opt/beacon
sudo ./install.sh        # asks for the two names and the address; or STATUS_HOST=… METRICS_HOST=… ACME_EMAIL=… sudo ./install.sh
```

`install.sh` copies `.env.example` to `.env`, generates the secrets (the rules token and the SSH channel's host and CA keys, which never leave the machine), puts your host names into `config.yaml` and the probe targets, validates every file with its own image, pulls, starts, waits until the four containers are healthy and the rule engine's first alert has reached the gateway, and prints what to do next. It is safe to run again: after editing a file, or after changing `BEACON_VERSION` in `.env` to update.

## Where things are

| Path | What |
| --- | --- |
| `compose.yaml`, `.env` | The four containers, pinned versions; Beacon's version and the host names |
| `config.yaml` | Beacon's configuration ([config.example.yaml](../config.example.yaml) explains every field). The chat sections are commented out until you fill them in |
| `catalog/` | Probe locations, heartbeats, expiring resources ([docs/guide/monitoring.md](../docs/guide/monitoring.md)) and the public status page ([docs/guide/status-pages.md](../docs/guide/status-pages.md)); imported into Beacon's database at the first start |
| `targets/` | What the probes check (`all-web.yaml`, `all-drill.yaml`) and how (`modules.yml`); imported likewise |
| `rules/`, `rules/tests/` | vmalert's rules and their unit tests (`vmalert-tool unittest --disableAlertgroupLabel --files 'rules/tests/*.yml'`) |
| `runbooks/` | One page per alert: what it means, what to check, how to fix, how to verify. The cards link here |
| `Caddyfile`, `metrics/scrape.yml` | The edge, and what VictoriaMetrics scrapes about the centre itself |
| `secrets/` | Made by `install.sh`; add the chat's and the phones' secret files here (owner 65532, mode 0400) |
| `probe/` | For each probe machine: `install.sh`, the systemd unit, `beacon-heartbeat` |
| `litestream.yml` | The optional continuous-WAL-replication service's configuration (see Backups below) |
| Docker volumes `beacon-data`, `beacon-backups`, `victoriametrics-data`, `caddy-data` | Beacon's database and session recordings; its backups; the metrics; the certificates |

Everyday commands run inside the Beacon container: `docker exec beacon beacon token list`, `… enroll probe edge-1`, `… machine add NAME HOST`, `… silence list`, `… drill fail 15m`, `… config history`. VictoriaMetrics' own UI is at `http://127.0.0.1:8428/vmui` on the host (through an SSH tunnel from elsewhere), vmalert's at `:8880`.

After the first start the catalogue and targets live in Beacon's database and are changed through the API, MCP or `beacon config`; the files here are for `config import` and `config export`, so you can still keep them in git.

## Backups

Beacon backs itself up (the `backup:` section of `config.yaml`, on in this reference deployment): the database snapshot every hour, the recordings, the configuration and the secrets directory every day, each one file under the `beacon-backups` volume, encrypted to public keys Beacon holds — it cannot read what it wrote. With no recipients configured, the first start generates a key pair and leaves the private key in `/var/lib/beacon/backup/RECOVERY-MATERIAL.txt`; the log keeps reminding you to take it off the machine until you do. A backup that stops succeeding goes quiet on the built-in `beacon-backup` heartbeat and alerts like any other job that stopped reporting.

```bash
docker exec beacon beacon backup now          # one at once (everything it is due)
docker exec beacon beacon backup ls           # what the destinations hold, newest first
docker cp beacon:/var/lib/beacon/backups/. .  # take a copy off the machine (or add an s3: destination)
```

Recovery, on any machine with the backup file and the recovery material:

```bash
beacon backup restore beacon-<host>-<time>-full.tar.zst.age -identity recovery.txt -into <empty dir>
beacon backup restore <the same file> -identity recovery.txt -dry-run   # prove it without touching anything
```

Prove the chain before you need it, and renew the `recovery-drill` reminder while you are there: `beacon backup drill <file> -identity <the same file>`. To send a copy off the machine as it is made, add an `s3:` destination (MinIO, R2, S3) to `backup.destinations` — the credentials go in files under `secrets/`, like every other secret.

For a recovery point of seconds rather than an hour, uncomment the `litestream` service in `compose.yaml` and fill in `litestream.yml`: it ships the database's write-ahead log to an S3-compatible store continuously. The recordings and the configuration still come from Beacon's backups.
