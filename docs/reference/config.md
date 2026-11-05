# config.yaml

`beacon serve` reads one YAML file, `/etc/beacon/config.yaml` by default; `-config` names another. The file is read once, at start, so changing it takes a restart. The catalogue, target and module files it names (`catalog_dir`, `gateway.targets_dir`, `gateway.modules_file`) are another matter: the service imports them into its database at the first start, and again on SIGHUP ([Configuration](../self-hosting/configuration.md)). The example file in the repository, `config.example.yaml`, holds every key with comments.

Two rules hold the whole file together:

- **No unknown keys.** A key the tables below do not list is refused at start, so a typo does not go unnoticed. `beacon check-config -config FILE` validates the file, the files it names and the database, without starting ([Command line](cli.md)).
- **Secrets are files, not values.** Every credential is a `*_file` path: `rules.token_file`, `feishu.app_id_file`, a hook's `secret_file`. Beacon reads each file once at start, trims the whitespace around it, and never logs the content; an empty file stops the start. How those files are filled is the deployment's business: Beacon depends on no secret manager. `rules.token_file` and a webhook hook's `secret_file` need at least 24 characters.

Durations are written as Go writes them: `30s`, `5m`, `4h`, `720h`.

## The service

| Key | Type | Default | What it is |
| --- | --- | --- | --- |
| `listen` | address | `:17001` | Where the console, the API and MCP answer, behind the TLS proxy |
| `data_dir` | path | `/var/lib/beacon` | The bbolt state (alert groups, tokens, accounts, the configuration) and, beside it, the session recordings. The admin socket the CLI talks to lives here; `BEACON_DATA_DIR` names another directory for the CLI alone |
| `catalog_dir` | path | `/etc/beacon/catalog` | The catalogue files (probe locations, heartbeats, expiring resources, status pages), imported into the database at the first start and on SIGHUP |
| `public_url` | URL | — | Beacon's own address, behind the proxy: the links it hands out (the console, one-time links) and the issuer MCP clients sign in with. Agents' hooks need it |
| `trusted_proxies` | list of addresses or CIDR ranges | — | The proxy in front, as the service sees it, so the API knows real client addresses |
| `language` | `zh` or `en` | `zh` | The language of cards and chat messages |
| `timezone` | time zone | `Asia/Shanghai` | Times on cards; the days of the status pages' history begin at midnight here |
| `storage.driver` | `sqlite` | `sqlite` | The database's driver: `sqlite` (one file, no services) or `postgres`. A deployment that already runs PostgreSQL, wants a managed database, point-in-time recovery or the primary/standby pair switches; performance is not a reason to. `beacon storage migrate -to postgres` moves a SQLite database over |
| `storage.path` | path | `<data_dir>/beacon.db` | The SQLite database file. A first start with an old bbolt file (`status.db`) beside it migrates that file into the database in one transaction; the old file stays as `status.db.pre-sql` to roll back with |
| `storage.dsn_file` | path | — | With `storage.driver: postgres`, a file holding the connection string (for example `postgres://beacon@db.example.com:5432/beacon?sslmode=require`), read once at start; the password, if any, is in the file, never in this YAML |
| `storage.audit.checkpoint_interval` | duration | `24h` | How often the audit log's hash chain gets a signed checkpoint |
| `storage.audit.retention` | duration | `8760h` | How long audit entries are kept (365 days); the chain stays verifiable after pruning |
| `storage.sqlite.journal_mode` | `wal` | `wal` | SQLite's journal mode. A WAL database is one file plus its `-wal` and `-shm` side files and cannot live on a network file system |
| `storage.sqlite.synchronous` | `full` | `full` | SQLite's durability: `full` has every commit on the disk before it answers |
| `storage.sqlite.busy_timeout` | duration | `15s` | How long a statement waits for a lock held by another connection |
| `storage.postgres.pool_size` | number | `4` | PostgreSQL read connections; writes always take one connection of their own, whatever the pool size |
| `storage.postgres.statement_timeout` | duration | `15s` | How long a PostgreSQL statement may run before the server cancels it, so one that hangs does not hold the writer; `0` turns it off |
| `ha.enabled` | bool | `false` | The primary/standby pair over one PostgreSQL database (needs `storage.driver: postgres`): the primary renews a lease in the database, the standby takes it when it expires and answers `/healthz` with 503 until then, so the load balancer or the VIP in front sends traffic to one instance at a time. `/livez` stays 200 on a healthy standby (the image's health check) and goes 503 when this instance cannot reach PostgreSQL. The switch takes about `ha.expire`. What the pair shares is the database and the recordings' bucket (`ssh.recordings: s3`) |
| `ha.name` | string | `beacon` | The lease's name; every instance of the pair names the same |
| `ha.id` | string | hostname:pid plus a random suffix | This instance's name in the lease row. Set it when two containers would otherwise share a hostname and run as PID 1 |
| `ha.renew` | duration | `5s` | How often the primary renews the lease |
| `ha.expire` | duration | `15s` | How long the lease lasts without a renewal; the standby takes over within this. Must outlast `ha.renew` |
| `backup.destinations` | list | — | Where every backup goes, several at once: `{dir: path}` for a local directory, `{s3: …}` for an S3-compatible store. At least one turns backups on |
| `backup.destinations.dir` | path | — | A local directory, made when missing |
| `backup.destinations.s3.endpoint` | URL | — | The store's address, such as `https://s3.example.com:9000` (path-style addressing) |
| `backup.destinations.s3.region` | region | `us-east-1` | For signing; stores that ignore regions ignore it too |
| `backup.destinations.s3.bucket` | name | — | The bucket backups land in |
| `backup.destinations.s3.prefix` | prefix | — | Inside the bucket, such as `beacon/`. An empty prefix lists only objects at the bucket root, not keys under other prefixes |
| `backup.destinations.s3.access_key_file` | path | — | The access key, read from its file at use |
| `backup.destinations.s3.secret_key_file` | path | — | The secret key, read from its file at use. A single PUT is at most 5 GiB |
| `backup.recipients` | list of public keys | generated | The age (`age1…`) or ssh-ed25519 public keys backups are encrypted to; Beacon holds only these and cannot read what it wrote. With none, the first start generates a pair and leaves the private half once in `<data_dir>/backup/RECOVERY-MATERIAL.txt` |
| `backup.recipients_file` | path | — | More recipients, one public key per line |
| `backup.secrets_dir` | path | `/etc/beacon/secrets` | Packed whole into every backup, so a snapshot and the keys it was sealed with travel together |
| `backup.heartbeat.max_age` | duration | `2h` | The built-in `beacon-backup` heartbeat's patience; a backup older than this alerts through `HeartbeatStale` |
| `backup.heartbeat.severity` | severity | `warning` | That alert's severity |
| `backup.drill_expiry` | duration | `2160h` | How long a recovery drill's proof lasts: the built-in `recovery-drill` resource expires this long after the last drill |
| `backup.schedule.database` | duration | `1h` | How often the database snapshot alone is taken |
| `backup.schedule.files` | duration | `24h` | How often the recordings, configuration and secrets come along |
| `backup.schedule.files_full` | duration | `168h` | How often the recordings set is complete rather than incremental, so the chain stays restorable |
| `backup.retention.hourly` | count | `24` | Grandfather–father–son: keep the newest of each of the last N hours; a backup fitting any rule stays |
| `backup.retention.daily` | count | `14` | … of the last N days |
| `backup.retention.weekly` | count | `8` | … of the last N weeks |
| `backup.retention.monthly` | count | `12` | … of the last N months |
| `backup.retries` | count | `3` | How many times a failed upload is tried again |
| `backup.retry_wait` | duration | `1m` | The first wait between retries; it grows fourfold each time |
| `backup.verify_readback` | boolean | `true` | Read every upload back and compare size and hash before it counts |
| `backup.on_failure` | `alert` | `alert` | What a backup that keeps failing does (`alert_and_pause` is not implemented and is rejected) |
| `vault.mode` | `file`, `passphrase`, `aws-kms`, `gcp-kms`, `transit` | `file` | Where the root key comes from, read at start: a file in the secrets directory (`file`); a passphrase an owner enters (`passphrase`, after a restart the vault stays sealed until then — monitoring and the SSH channel carry on); or a keeper that unwraps it (`aws-kms`, `gcp-kms`, `transit`). Rotate with `beacon key rotate` |
| `vault.dir` | path | `<data_dir>/secrets` | The directory the key files live in, made at the first start with only-owner permissions |
| `vault.aws_kms.region` | string | — | The AWS KMS mode: the region of the key |
| `vault.aws_kms.key_id` | string | — | The key's id or alias (`alias/beacon`) |
| `vault.aws_kms.endpoint` | URL | — | A KMS of one's own for the tests; empty is AWS's own |
| `vault.aws_kms.access_key_file` | path | — | The access key, as a file |
| `vault.aws_kms.secret_key_file` | path | — | The secret key, as a file |
| `vault.aws_kms.session_token_file` | path | — | A session token, when temporary credentials are the deployment's way |
| `vault.gcp_kms.key_name` | string | — | The Cloud KMS mode: the key's full resource name, `projects/…/locations/…/keyRings/…/cryptoKeys/…` |
| `vault.gcp_kms.endpoint` | URL | — | A KMS of one's own for the tests; empty is Google's own |
| `vault.gcp_kms.token_file` | path | — | The bearer token, as a file |
| `vault.transit.address` | URL | — | The Vault or OpenBao transit mode: its address |
| `vault.transit.key_name` | string | — | The transit key's name |
| `vault.transit.token_file` | path | — | The token, as a file |

## Rules and routing

| Key | Type | Default | What it is |
| --- | --- | --- | --- |
| `rules.token_file` | path | required | The bearer token the rule engine (vmalert or Prometheus) authenticates with when it posts alerts to the gateway's `/api/v2/alerts`, as it would to Alertmanager. At least 24 characters |

Routing is what Alertmanager's `route` and `inhibit_rules` did. Label values in `match`, `source` and `target` are regular expressions over the whole value.

| Key | Type | Default | What it is |
| --- | --- | --- | --- |
| `routing.group_by` | list of label names | `[alertname, service]` | The labels an alert group shares |
| `routing.group_wait` | duration | `30s` | After a group's first alert, wait for more before the card |
| `routing.group_interval` | duration | `2m` | Then update the card at most this often |
| `routing.resolve_timeout` | duration | `5m` | An alert the rule engine stops sending resolves after this |
| `routing.routes.match` | map of label to regular expression | — | What a route matches; the first route that fits changes the grouping |
| `routing.routes.group_by` | list of label names | — | That route's own grouping, one card for many services |
| `routing.inhibit.source` | map of label to regular expression | — | While a source alert fires |
| `routing.inhibit.target` | map of label to regular expression | — | target alerts with the same `equal` labels are muted |
| `routing.inhibit.equal` | list of label names | — | The labels whose values must match |

## The gateway

The machines' door: probes and hosts write with tokens from `beacon enroll` and fetch what to check, jobs push heartbeats, the rule engine and Flux post alerts, `metrics:read` tokens read ([REST API](api.md#the-gateway)).

| Key | Type | Default | What it is |
| --- | --- | --- | --- |
| `gateway.listen` | address | `:17006` | Where machines connect. Empty turns the gateway off |
| `gateway.targets_dir` | path | `/etc/beacon/targets` | The probe target files (`all-*.yaml`, `<location>-*.yaml`), imported into the database at the first start and on SIGHUP |
| `gateway.modules_file` | path | `modules.yml` in `gateway.targets_dir` | The probe modules, imported the same way |
| `victoriametrics.url` | URL | required with the gateway | Where the probes' results are: the gateway writes there, and cards show each location's result from it |

## The SSH channel

The channel is on when `ssh.ca_key_file` is set ([Give agents a machine](../guide/machines.md)). Both keys are OpenSSH private keys: `ssh-keygen -t ed25519`.

| Key | Type | Default | What it is |
| --- | --- | --- | --- |
| `ssh.listen` | address | `:17002` | Where agents reach the channel |
| `ssh.public_address` | host:port | — | Where agents connect from outside, for the ssh configuration Beacon hands out |
| `ssh.host_key_file` | path | required with the channel | The channel's own host key, not the machines' |
| `ssh.ca_key_file` | path | — | The key that signs a short-lived certificate for every connection to a machine; setting it turns the channel on. It must be a different key from the host key |
| `ssh.source_addresses` | list of CIDR ranges | — | When set, the certificates work only from these addresses |
| `ssh.keep` | duration | `4320h` (180 days) | How long sessions and their recordings are kept. At least `24h` |
| `ssh.recordings` | `dir` or `s3` | `dir` | Where sessions are recorded: the recordings directory under `data_dir`, or an S3-compatible bucket (`ssh.recordings_s3` names it). A primary/standby pair shares the bucket, so a session one machine recorded the other replays. With `s3`, Beacon writes the recording locally first and uploads it when the session closes, retrying a failed put; set a lifecycle rule on the bucket so objects that outlive a failed tidy are still expired |
| `ssh.recordings_s3.endpoint` | URL | — | The bucket's address, such as `https://s3.example.com`, or `http://10.0.0.5:9000` for MinIO on a private network. Addresses are path-style |
| `ssh.recordings_s3.region` | string | `us-east-1` | The region the bucket signs with |
| `ssh.recordings_s3.bucket` | string | — | The bucket's name |
| `ssh.recordings_s3.access_key_id_file` | path | required with `s3` | A file holding the access key id |
| `ssh.recordings_s3.secret_access_key_file` | path | required with `s3` | A file holding the secret access key |

## Links, pages and branding

| Key | Type | Default | What it is |
| --- | --- | --- | --- |
| `links.grafana_service` | URL template | — | The dashboard an alert's card links to; `{service}` is filled in, and the alert's own annotation wins |
| `links.runbook` | URL template | — | The runbook; `{alertname}` is filled in, and the alert's own annotation wins |
| `pages.link_ttl` | duration | `720h` (30 days) | How long the signed links to the console's pages (an alert's timeline, a session's replay) are valid. Between `1h` and 366 days |
| `branding.title` | string | `Beacon` | The deployment's name on the page at `/` when there is no status page. One line, at most 80 characters |
| `branding.powered_by` | bool | `true` | `false` leaves "Powered by Beacon, open source" out of the public pages' footer |
| `oauth.client_id_documents` | bool | `true` | Take MCP clients by the metadata document their `client_id` names (Claude Code's and Codex's do), which Beacon fetches from the internet. `false` when Beacon cannot reach the internet: clients then register themselves |

## Feishu

The self-built app (cards, button callbacks, buzz) and the group's custom bot ([Set up the Feishu app](../integrations/feishu.md)).

| Key | Type | Default | What it is |
| --- | --- | --- | --- |
| `feishu.app_id_file` | path | — | The app's id; goes with `feishu.app_secret_file` and `feishu.chat_id` |
| `feishu.app_secret_file` | path | — | The app's secret |
| `feishu.domain` | `feishu` or `lark` | `feishu` | open.feishu.cn or open.larksuite.com |
| `feishu.callback` | `websocket` or `http` | `websocket` | How the app's button callbacks arrive: Feishu's long connection, or POSTs to `/hooks/feishu` |
| `feishu.verification_token_file` | path | — | The verification token, `http` callbacks only |
| `feishu.encrypt_key_file` | path | — | The encrypt key, `http` callbacks with encryption on |
| `feishu.chat_id` | string | required with the app | The group the alert cards go to |
| `feishu.on_call` | list | required with the app | The only people who may act on cards, and the ones buzzed on escalation |
| `feishu.on_call.open_id` | string | — | A person's `ou_…` id |
| `feishu.on_call.name` | string | — | Their name, shown on the cards |
| `feishu.monthly_budget` | int | `10000` | The app's API calls per month, Feishu's free-plan limit. At least 100 |
| `feishu.webhook_url_file` | path | — | The group's custom bot: plain messages, no API quota. Used for info alerts, over-budget notices and echoes |
| `feishu.webhook_secret_file` | path | — | When the bot signs its requests |
| `feishu.webhook_keyword` | string | `Beacon` | The keyword, when the bot's security setting is one |

## Pushover, Bark and healthchecks.io

| Key | Type | Default | What it is |
| --- | --- | --- | --- |
| `pushover.user_key_file` | path | — | Who the emergency pushes go to; goes with `pushover.token_file` |
| `pushover.token_file` | path | — | The Pushover app's token |
| `pushover.retry` | duration | `60s` | Emergency alerts repeat this often until acknowledged. At least `30s` |
| `pushover.expire` | duration | `3h` | And stop repeating after this long. At most `3h` |
| `pushover.api` | URL | `https://api.pushover.net` | Pushover's API, for a proxy or a test |
| `bark.url_file` | path | — | An iOS push with a ringing critical level: `https://api.day.app/<key>`, or a self-hosted server |
| `healthchecks.bridge_url_file` | path | — | Pinged every minute while the bridge can deliver alerts, `/fail` when it cannot: a dead man's switch outside your infrastructure (healthchecks.io) |

## Escalation and acknowledgements

How alerts of each severity reach people. A zero duration turns a step off. Without the whole `escalation` section Beacon picks the best channel configured: the Feishu app when it is set, else the group bot, else `none` (alerts are only recorded, and the API and MCP still show them); critical alerts buzz after 10 minutes with the app and reach Pushover after 15 minutes when it is configured; info alerts go to the group bot when there is one beside the app.

| Key | Default | What it is |
| --- | --- | --- |
| `escalation.critical.channel` | the best channel configured | `app` (Feishu card), `webhook` (group bot) or `none` (only recorded) |
| `escalation.critical.buzz` | `10m` with the app, off without it | Buzz the on-call people after this |
| `escalation.critical.pushover` | `15m` with Pushover, off without it | An emergency push to phones after this |
| `escalation.critical.bark` | off | A Bark push after this |
| `escalation.warning.channel` | the best channel configured | As `escalation.critical.channel` |
| `escalation.warning.buzz` | off | As `escalation.critical.buzz` |
| `escalation.warning.pushover` | off | As `escalation.critical.pushover` |
| `escalation.warning.bark` | off | As `escalation.critical.bark` |
| `escalation.info.channel` | the app, or the group bot when both are set | Low-severity alerts need no app quota |
| `escalation.info.buzz` | off | As `escalation.critical.buzz` |
| `escalation.info.pushover` | off | As `escalation.critical.pushover` |
| `escalation.info.bark` | off | As `escalation.critical.bark` |
| `ack_ttl` | `4h` | An acknowledgement lapses after this, and escalation starts again. At least `10m` |

## Agent hooks

Hooks that wake agents when a new alert group is first notified, telling them where the evidence is ([Agents woken by a webhook](../integrations/webhooks.md)). Beacon calls no model: what to do is the agent's call. Each entry of the list is `kind: webhook` (a signed JSON POST) or `kind: github` (a workflow dispatch).

| Key | Kind | What it is |
| --- | --- | --- |
| `agents.name` | both | The hook's name: lower case letters, digits, dots, dashes and underscores |
| `agents.kind` | both | `webhook` or `github` |
| `agents.severities` | both | The severities that wake it. Default `[critical]` |
| `agents.service` | both | A regular expression over the whole `service` label; empty matches any service |
| `agents.resolved` | both | Also call when the group resolves |
| `agents.resolved_after` | both | How long the resolved call waits, so the agent's earlier run can leave its closing note first. Default `1m`, at most `5m`; `0` sends it at once |
| `agents.url` | webhook | Where the JSON payload is POSTed |
| `agents.secret_file` | webhook | The HMAC-SHA256 signing secret, sent as `X-Beacon-Signature`; at least 24 characters. `secret_file` or `headers_file` is required, so the agent can tell Beacon's calls from a forgery |
| `agents.headers_file` | webhook | `Name: value` lines sent with every call, for a receiver that checks a secret of its own; with it, `secret_file` is optional |
| `agents.repository` | github | The repository, `owner/name` |
| `agents.workflow` | github | The workflow's file name (such as `oncall.yml`) or id |
| `agents.ref` | github | Default `main` |
| `agents.token_file` | github | A token that may dispatch the workflow (Actions: read and write on the repository) |
| `agents.api` | github | Default `https://api.github.com`; GitHub Enterprise sets its own |

## Change events

| Key | Type | Default | What it is |
| --- | --- | --- | --- |
| `changes.keep` | duration | `720h` (30 days) | How long the change events (deployments, Flux reconciliations) that senders post to the gateway are kept. At least `24h` |
