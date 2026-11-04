# Configuration

Two different things are called Beacon's configuration, and this page is about the first:

- **`config.yaml`**, the service's own configuration: where it listens, the chat it alerts through, the escalation to phones, the SSH channel's keys. It is a file on the centre, read once when Beacon starts.
- **The catalogue, probe targets and modules** — what is checked and from where. These live in Beacon's database and are changed through the API, MCP or the command line; the files are for import and export. [What is checked, and from where](../guide/monitoring.md) is that story; the last section of this page says when the files are read.

The examples assume the reference deployment from [On a server](install.md).

## config.yaml

In the reference deployment it is `/opt/beacon/config.yaml` on the centre, mounted read-only into the container as `/etc/beacon/config.yaml`. Every field is explained in [`config.example.yaml`](https://github.com/openlinkos/beacon/blob/main/config.example.yaml) at the repository's root, and listed key by key in the [config.yaml reference](../reference/config.md); this section is the map.

| Section | What it decides |
| --- | --- |
| `listen`, `data_dir`, `catalog_dir` | Where Beacon's HTTP server listens (default `:17001`), where its database and recordings live, where the catalogue files are imported from |
| `public_url`, `trusted_proxies`, `language`, `timezone` | The address cards and pages link to; which proxies may set `X-Forwarded-For`; the language of cards and chat messages (`en` or `zh`); the time zone they are shown in |
| `rules` | The token file the rule engine authenticates its alerts with. Required |
| `routing` | Grouping and delivery of alerts, as Alertmanager's route did: `group_by`, `group_wait`, `group_interval`, `resolve_timeout`, per-service routes, inhibition |
| `victoriametrics` | Where the gateway writes results and the bridge reads them |
| `gateway` | The machines' door: the listen address (default `:17006`; empty turns it off), the targets directory and the module file |
| `ssh` | The SSH channel: the listen address (default `:17002`), the public address agents connect to, the host key and CA key files, addresses certificates work from, how long sessions and recordings are kept. On when `ca_key_file` is set |
| `links` | URL templates for the cards: the runbook and the dashboard, per service and alert |
| `pages` | How long the signed links a card opens are valid |
| `branding` | The deployment's title on the public pages, and whether they say they are powered by Beacon |
| `oauth` | Whether Beacon takes MCP clients by their client-id metadata documents (Claude Code's and Codex's are); a deployment that cannot reach the internet turns it off |
| `feishu`, `pushover`, `bark`, `healthchecks` | The chat, the phones and the dead man's switch; each is off until its section and files exist |
| `escalation` | Per severity: which channel, when the on-call are buzzed, when a phone is called |
| `ack_ttl`, `agents`, `changes` | How long an acknowledgement lasts; hooks that wake agents on new alert groups; how long change events are kept |

An unknown key is an error, not a warning: a misspelled section stops Beacon from starting, and `check-config` (below) says so before that. Durations are written as `30s`, `2m`, `4320h`.

## Secrets are files, never values

No credential is ever written in `config.yaml`: every one is a `*_file` path, and Beacon reads the file once at start. A secret in the YAML is a configuration error; the `*_file` fields and the files they name are the whole story.

In the reference deployment the files live under `/opt/beacon/secrets/`, owner 65532 and mode 0400. `install.sh` makes the three it can make itself (`rules-token`, `ssh-host-key`, `ssh-ca-key`, which never leave the machine) and gives any file you add there the same owner and mode. The chat's are yours to put there: `feishu-app-id`, `feishu-app-secret`, `feishu-webhook-url`, `pushover-user-key`, `pushover-app-token`, `healthchecks-bridge-url`, as [the Feishu page](../integrations/feishu.md) walks through.

Secrets are never logged and never appear in errors. How those files are filled is the deployment's own business: Beacon depends on no secret manager.

## Checking it

`check-config` validates `config.yaml` with everything it names, and the configuration in the database, without starting anything. In the reference deployment:

```bash
docker exec beacon beacon check-config
```

```
configuration OK: Feishu app false, Pushover false, Bark false; gateway ":17006"; SSH channel :17002; 0 agent hooks
files: 1 probe locations, 0 heartbeats, 0 resources, 3 probe targets in 2 sets, 5 modules, 1 status pages
database: version 2 in the running service: …
```

`install.sh` runs the same check inside its own image before every start, so a file that does not validate stops the install with the reason, not the service in the night.

## Applying a change

`config.yaml` is read once, at start. A change to it reaches Beacon with a restart:

```bash
docker compose restart beacon
```

`install.sh` does this itself when it sees that `config.yaml` or a secret changed since its last run, so the round for a configuration change is: edit the file, `sudo ./install.sh`, done.

SIGHUP is not the way, and this is the one thing worth being clear about: **a hang-up does not re-read `config.yaml`**. It is about the catalogue files (next section). The chat, the escalation, the agents hooks — all of these need a restart.

The chat, the common case, in three moves:

1. Put each secret in its file under `/opt/beacon/secrets/`: `feishu-app-id`, `feishu-app-secret`, `feishu-webhook-url`; `pushover-user-key` and `pushover-app-token` for phones; `healthchecks-bridge-url` for the dead man's switch.
2. Uncomment the `feishu:` section (and `pushover:`, `healthchecks:`) in `/opt/beacon/config.yaml`, with your `chat_id` and the people on call.
3. `sudo ./install.sh` again: it fixes the secret files' owner and mode, validates everything and restarts Beacon. The log then says `feishu long connection ready` (`docker compose logs beacon`).

## The catalogue files, at start and on SIGHUP

The first start imports the files the configuration names (`catalog_dir`, `gateway.targets_dir`, `gateway.modules_file`) into the database and logs `imported the configuration files into the database, where it is changed from now on`; files that fail their checks stop the start. From then on the database is the source of truth, and the files are read only when asked:

- **SIGHUP** (`docker kill -s HUP beacon`) imports them, but only when nothing was changed through Beacon since the last import, so the files cannot silently undo what people and agents changed. Otherwise the log says which change came since, and how to go on: `config export` to bring that change into the files, or `config import` to replace it.
- **`beacon config import DIR`** imports a directory at any time, `-dry-run` first; **`beacon config export DIR`** writes what is in force back to files; **`beacon config history`** and **`beacon config revert VERSION`** walk and undo the changes.

[What is checked, and from where](../guide/monitoring.md#the-files-at-start-and-on-sighup) has the whole of it, including the trap of an import: items the files do not hold are removed.
