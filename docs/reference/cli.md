# Command line

Beacon ships three binaries. `beacon` is the service, and the commands that run it and administer the running service; `beacon-probe` is the probe agent that runs on every probe location; `beacon-oncall` is the reference harness that runs an agent when Beacon wakes it. Each answers `version` (the version, commit and build) and `help` (the whole usage).

## beacon

`serve` runs the service. Every other command talks to the running service over its admin socket in the data directory, `/var/lib/beacon` by default or `BEACON_DATA_DIR` when that is set, so run them inside the container:

```bash
docker exec beacon beacon token list
```

| Command | What it does |
| --- | --- |
| `serve` | Run the service: the console, the API and MCP, the router and the bridge, the gateway and the SSH channel. Takes `-config` (default `/etc/beacon/config.yaml`); the catalogue, target and module files it names are imported into the database at the first start and again on SIGHUP. See [config.yaml](config.md) |
| `check-config` | Validate the configuration file, the files it names and the configuration in the database, without starting. Takes `-config` |
| `config` | The configuration in the database: [import, export, history, revert](#beacon-config) |
| `setup` | Print the one-time link that makes the first account, an owner, valid 30 minutes |
| `user` | People: [list, invite, disable, enable, role, reset](#beacon-setup-user-agent) |
| `agent` | Agents of a person: [add, list, remove](#beacon-setup-user-agent) |
| `token` | Credentials: [create, list, revoke, ssh-key, machines, claim](#beacon-token) |
| `machine` | Machines: [add, connector, list, remove, ca](#beacon-machine-ssh) |
| `ssh` | The SSH channel: [pause, resume, status](#beacon-machine-ssh) |
| `enroll` | A machine's gateway token: [probe, host or heartbeat](#beacon-enroll-silence-drill) |
| `silence` | Silences: [create, list, expire](#beacon-enroll-silence-drill) |
| `drill` | Rehearse an outage: [fail, off, status](#beacon-enroll-silence-drill) |
| `audit` | The audit log's chain: [verify](#beacon-audit-storage) |
| `storage` | The database's health: [check](#beacon-audit-storage) |
| `secret` | The secrets vault: [ls, collection, show, set, reveal, tag, level, archive, rm, restore, rollback, generate, import, perms, tags, rebuild-head](#beacon-secret) |
| `key` | The vault's keys: [status, unseal, seal, rotate](#beacon-key) |
| `healthcheck` | Exit 0 when the service answers; the image's health check. Takes `-url` (default `http://127.0.0.1:17001/healthz`) |
| `version` | The version, commit and build |
| `help` | The whole usage, with the roles, presets and scopes |

### beacon config

The catalogue, probe targets and modules live in the database ([Monitor a service](../guide/monitoring.md)); these commands change them as one versioned change. `import` reads `DIR/catalog`, `DIR/targets` and `DIR/targets/modules.yml`, the layout `export` writes.

| Subcommand | Flags | What it does |
| --- | --- | --- |
| `import` DIR | `-dry-run`, `-note` | Make DIR's files the catalogue, targets and modules, as one change. `-dry-run` says what would change and changes nothing; `-note` says why, kept with the change and shown in the alert chat |
| `export` DIR | | Write the configuration there in the same layout, and remove files the configuration does not hold |
| `history` | `-limit` | The changes: version, time, who, via, action, note. `-limit` (default 20) says how many |
| `revert` VERSION | `-note` | Go back to a version from `history` |

### beacon settings

The `settings` command reads and changes the system settings the secrets work runs by, each with a registered default, every change a version with who made it and why, revertible ([Settings](../reference/api.md#settings)). Values are JSON, as they are stored: `50`, `"30d"`, `["rules","agents"]`, `{"keep":"365d","min_versions":10}`. A missing or unverifiable signed head is recovered with `rebuild-head`; Beacon does not rebuild it on a later write. The epochs file lives in the secrets directory, shared with the vault's policy and items heads.

| Subcommand | Flags | What it does |
| --- | --- | --- |
| `get` [KEY] | | Every setting with its value, default, where the value was overridden and who changed it, then the other scopes' overrides; with KEY, that one and where its value stands |
| `set` KEY VALUE | `-scope`, `-target`, `-reason` | Change one setting. `-reason` says why, kept with the change and shown in the alert chat; `-scope` (default `system`) and `-target` put the value at a collection, tag, entitlement or request |
| `reset` KEY | `-scope`, `-target`, `-reason` | Take the override away, so the level above (or the default) answers again |
| `history` | `-limit` | The changes: version, time, who, via, action, setting, note |
| `revert` VERSION | `-note` | Go back to a version from `history` |
| `rebuild-head` | | Rebuild the signed head from the rows that still verify (owner recovery through the admin socket; audited) |

### beacon secret

The vault, for a person's hands on the command line: the same operations the [console's Secrets pages](https://beacon.openlinkos.com/reference/vault) and the [REST routes](api.md#vault) offer, under the same permissions and audit. Every change is made as an account (`-user`, your own by default), viewing values needs that account's password, and no value ever appears in a log or an audit entry. An item is named by its `beacon://` reference or `COLLECTION/TITLE`. Policy and item rows are named in epoch-hashed signed heads; a missing or unverifiable head is recovered with `rebuild-head`. The epochs file lives in the secrets directory.

| Subcommand | Flags | What it does |
| --- | --- | --- |
| `ls` [COLLECTION] | `-state` | The collections with their level and item count; with a collection, its items (`-state`: `normal`, `archived` or `trash`): title, version, state, level, tags |
| `collection` NAME | `-level`, `-description` | Make a collection; `-level` (default `review`) is the approval its items inherit |
| `show` REF | | One item: version, level, state, tags, its `beacon://` reference, its fields (names, types, purposes) and how many versions it has — never a value |
| `set` COLLECTION/TITLE [FIELD=VALUE …] | `-tags`, `-level`, `-category`, `-url`, `-set-note`, `-reason`, `-value-file`, `-value-from-stdin` | Add an item or write its next version. A field is `name=value` or `section/name=value`; `username` is a plain field, everything else concealed. `FIELD=-` with `-value-file` or `-value-from-stdin` reads the value from a file or stdin, so it need not sit on the command line. `-level` `-` clears the item's own; `-reason` says why, kept with the version |
| `reveal` REF [FIELD …] | `-user`, `-password-file`, `-password-from-stdin` | View values: the account's password comes from the file (`-password-file`) or stdin (`-password-from-stdin`; it shows while you type). An `otp` field gives the current code and the seconds left. Audited before the values print |
| `tag` REF TAGS | `-user`, `-reason` | Change an item's tags (comma-separated) — a policy change, so it needs `manage` on the collection |
| `level` REF LEVEL | `-user`, `-reason` | Set an item's own approval level (`auto`, `review`, `person`, `never`, or `-` to inherit the collection's) — also `manage` |
| `archive` REF | `-user` | Archive an item: kept, out of the way |
| `rm` REF | `-user` | Move an item to the trash, where `retention.trash` (30 days) keeps it |
| `restore` REF | `-user` | Bring an item back from the archive or the trash |
| `rollback` REF VERSION | `-note` | Make an old version current again; `-note` says why, kept with the new version |
| `generate` REF FIELD | `-length`, `-charset`, `-user`, `-reason` | Have Beacon make a new value (`-length` 24 by default, `-charset` `letters`, `alnum` or `all`), stored as a new version. The value is not shown; reveal it |
| `import` SOURCE | `-file`, `-collection`, `-on-conflict`, `-level`, `-map`, `-vault`, `-dry-run`, `-user`, `-reason` | Bring an export in: `1pux` (1Password's unencrypted export, recommended — every vault a collection, attachments and password history included), `1password-csv`, `op` (the signed-in `op` command line, over about 300 items prefer 1PUX), `chrome` (one collection; the same credential across sites is one item with every URL), `bitwarden` (the unencrypted JSON export) or `csv` (any CSV, columns named by `-map title=HEADER,…`). The file is parsed where the command runs, so the plaintext travels once; `-dry-run` answers the report and sends no values at all — per collection the counts, categories and the level the items start at (`-level` `COLLECTION=LEVEL` overrides), and the conflicts (`-on-conflict` `skip` by default, `new_version`, `keep_both`). Never a value in the output; delete the export file when it is done |
| `perms` COLLECTION [user:NAME=PERM] | `-user`, `-reason` | Who may what in a collection: without the second word, the list; `user:alice=read` gives a person `describe`, `read`, `edit` or `manage` (a role with `role:oncall=read`), `user:alice=-` takes it away |
| `tags` [NAME [LEVEL]] | `-description`, `-user` | The tags, or set one: a tag's level is policy (every item wearing it is at least that strict), so only an owner's account may |
| `rebuild-head` | `-user` | Rebuild the policy and items signed heads from the rows that still verify (owner recovery through the admin socket; audited). Beacon does not rebuild a missing head on a later write |

### beacon key

The vault's keys: where the root key comes from is [config.yaml's](config.md#the-vault) `vault` section. In the `file`, `aws-kms`, `gcp-kms` and `transit` modes the keys are there at the start; in the `passphrase` mode a restart leaves the vault sealed until an owner initialises it (`key init`) the first time, then unseals it.

| Subcommand | Flags | What it does |
| --- | --- | --- |
| `status` | | `sealed` or `unsealed`, the mode, and a warning for every policy row that failed its signature (served as absent) |
| `init` | `-passphrase-file`, `-passphrase-from-stdin` | Set the first passphrase of a passphrase-mode vault and unseal it. Later unseals open with that passphrase; they never take a new one. The passphrase comes from a file (`-passphrase-file`, the secrets-directory way) or stdin (`-passphrase-from-stdin`; it shows while you type) — never a shell argument |
| `unseal` | `-passphrase-file`, `-passphrase-from-stdin` | Unseal the passphrase mode after a restart. The passphrase comes from a file (`-passphrase-file`, the secrets-directory way) or stdin (`-passphrase-from-stdin`; it shows while you type) — never a shell argument |
| `seal` | | Seal again: the key functions wait for the passphrase; monitoring and the SSH channel carry on |
| `rotate` [COLLECTION] | `-passphrase-file`, `-passphrase-from-stdin` | Without a name, a new root key: every collection key wrapped again, in seconds, the verified policy, items and settings heads re-signed in the same transaction (in the passphrase mode, `-passphrase-file` names a file holding the new one). A second rotation while one is in flight is refused. With a name, that collection's key: its items are encrypted again in the background. Removing a member needs no rotation — members never hold a key |

### beacon setup, user, agent

People's own accounts and their agents ([Invite your team](../guide/people.md)). One-time links and token secrets print once, on standard output; everything else goes to standard error. `setup` prints the one-time link that makes the first account, an owner; it works once and lasts 30 minutes.

**`beacon user`** — the people:

| Subcommand | Flags | What it does |
| --- | --- | --- |
| `list` | | People, their roles and agents, and invitations not yet used |
| `invite` | `-role`, `-note` | A one-time link to join as a role. `-role` is one of `viewer`, `oncall`, `operator`, `owner`; `-note` says who it is for |
| `disable` NAME, `enable` NAME | | Stop or restore someone's sign-in |
| `role` NAME ROLE | | Change the role; their sessions and agents follow at once |
| `reset` NAME | | A one-time link to choose a new password; their sign-ins end then |

**`beacon agent`** — a person's agents:

| Subcommand | Flags | What it does |
| --- | --- | --- |
| `add` NAME | `-owner`, `-preset`, `-scopes`, `-machines`, `-expires`, `-ssh-key` | An agent of a person and its first token, with the snippets that use it. `-owner` names the person; `-preset` is `readonly`, `oncall` or `operator`, the strongest the owner's role allows; `-scopes` gives custom scopes instead of a preset; `-machines` limits the machines it reaches; `-expires` (default `365d`); `-ssh-key` adds a public key for the channel, a key or `@FILE` |
| `list` | | Every agent: owner, preset, what it may do, machines, tokens, OAuth sign-ins, keys |
| `remove` NAME | | Remove an agent, with its tokens, OAuth sign-ins and keys |

### beacon token

Credentials with scopes by name, for machines and for experts; agents' tokens come from [beacon agent](#beacon-setup-user-agent) or the console. The secret of a new token prints once, on standard output.

| Subcommand | Flags | What it does |
| --- | --- | --- |
| `create` NAME | `-scopes`, `-expires`, `-ssh-key`, `-machines` | A token. `-scopes` (default `status:read`), comma-separated; `-expires` (default `365d`); `-ssh-key` a public key for the channel, a key or `@FILE`; `-machines` the machines it reaches, all when left out |
| `list` | | Every token: scopes, expiry, lifetime, when renewed, who made it |
| `revoke` NAME | | End a token at once |
| `ssh-key` NAME `add` KEY | | Add a public key to a token (`@FILE` reads it from a file) |
| `ssh-key` NAME `remove` FINGERPRINT | | Remove one of a token's keys |
| `machines` NAME a,b | | The machines a token reaches through the channel; `all` for every machine |
| `claim` NAME | `-owner`, `-agent`, `-preset` | Make a legacy token an agent of a person. `-owner` names the person; `-agent` names the agent (the token's own name by default); `-preset` is `readonly`, `oncall`, `operator` or `custom` (it keeps its scopes) |

### beacon machine, ssh

Machines and the SSH channel ([Give agents a machine](../guide/machines.md)). `add` prints what the machine needs on its side.

**`beacon machine`**:

| Subcommand | Flags | What it does |
| --- | --- | --- |
| `add` NAME HOST[:PORT] | `-user`, `-critical`, `-note`, `-replace`, `-key`, `-host-key` | Pin a machine's host key and reach it at its address. `-user` is the account on the machine (default `beacon`); `-critical` marks it critical; `-note` says what it is; `-replace` pins the host key of a machine that is added already |
| `add` NAME connector | the same | A machine behind NAT, which connects out to Beacon with `ssh -R`; `-key` is the public key it connects with, `-host-key` its host key (learned at its first connection when left out) |
| `connector` NAME | `-key` | Set or change the key a connector machine connects out with |
| `list` | | Every machine: address, account, host key, whether a connector is connected |
| `remove` NAME | | Remove a machine |
| `ca` | | The certificate authority key machines trust (`TrustedUserCAKeys`) |

**`beacon ssh`** — the brake on agents' access to machines:

| Subcommand | What it does |
| --- | --- |
| `pause` | Stop agents' access to every machine, at once; the brake besides each machine's access |
| `resume` | Allow it again |
| `status` | Whether the channel is on, paused since when, and its host key |

### beacon enroll, silence, drill

**`beacon enroll`** — a machine's gateway token, printed once and alone on standard output, so a deploy script can pass it straight to the machine:

| Subcommand | Flags | What it does |
| --- | --- | --- |
| `probe` NAME | `-rotate` | A probe location's token, with the scopes to push results and fetch what to check |
| `host` NAME | `-rotate` | A host's token, to push its own metrics |
| `heartbeat` NAME | `-rotate` | A job's token, to push heartbeats |

`-rotate` replaces the token of a machine that is enrolled already.

**`beacon silence`**:

| Subcommand | Flags | What it does |
| --- | --- | --- |
| `create` | `-matchers`, `-alert`, `-duration`, `-comment` | Stop notifying alerts that carry the given labels: `-matchers` as `alertname=X,service=Y`, or `-alert` with an alert group's id. `-duration` (at most 24h) and `-comment` are required |
| `list` | | Silences active and pending, ending soonest first |
| `expire` ID | | End a silence now, so the alerts it covered notify again |

### beacon audit, storage

**`beacon audit`** and **`beacon storage`** read the database itself, without the service and without locking it, so they work while it runs. Both take `-config`:

| Subcommand | Flags | What it does |
| --- | --- | --- |
| `audit verify` | `-from-seq` | Walk the audit log's hash chain — from the checkpoint covering `-from-seq`, or over everything still present — and say where it broke (exit 1) or how much it verified |
| `storage check` | | The database's health: SQLite's own integrity check, what each bucket holds, and the audit chain's verification |

**`beacon drill`** — rehearse an outage ([Rehearse an outage](../guide/drills.md)):

| Subcommand | What it does |
| --- | --- |
| `fail` 15m | Make `/drill/healthz` answer 503 for that long, and watch the alerts fire |
| `off` | End the drill |
| `status` | Whether a drill runs and until when |

## beacon-probe

The probe agent ([Probe locations](../self-hosting/probes.md)). The files modules name in `password_file` are read from `-secrets-dir` only (default `/etc/beacon-probe/secrets`), by file name; a module that names a path is refused.

| Subcommand | Flags | What it does |
| --- | --- | --- |
| `agent` | `-gateway`, `-token-file`, `-state`, `-interval`, `-textfile`, `-proc`, `-listen`, `-secrets-dir` | Push mode: fetch targets and modules from the gateway (`-gateway`), probe every `-interval` (default `1m`) and push the results, with a queue on disk in `-state` (default `/var/lib/beacon-probe`) for when the gateway cannot be reached. `-token-file` (default `/etc/beacon-probe/token`) holds the token from `beacon enroll probe`. `-textfile` (default `/var/lib/beacon-probe/textfile`) reads `*.prom` files, heartbeats; `-proc` (default `/proc`) the host's own metrics; `-listen` (default `127.0.0.1:17009`, empty for none) serves `/healthz` and `/metrics` for a look by hand |
| `serve` | `-listen`, `-modules`, `-textfile`, `-proc`, `-secrets-dir` | Pull mode, like the Blackbox Exporter: answer `/probe` and `/metrics` for a scraper. `-modules` (default `/etc/beacon-probe/modules.yml`) reloads on SIGHUP |
| `check` | `-modules` | Validate the module file, as the centre checks it on import |
| `run` MODULE TARGET | `-modules`, `-secrets-dir` | Probe once and print the result |
| `version` | | The version, commit and build |

## beacon-oncall

The reference harness on the agent's machine ([Agents woken by a webhook](../integrations/webhooks.md)): it takes Beacon's signed webhook, answers at once, and starts the program named by `-run` through `/bin/sh -c`, with the payload on standard input and the alert in `BEACON_*` environment variables. The harness decides nothing; the program does.

| Flag | Default | What it does |
| --- | --- | --- |
| `-listen` | `:17007` | The address to take Beacon's webhook on |
| `-secret-file` | required | The file with the hook's secret, the same as Beacon's `secret_file` |
| `-run` | required | The program for firing events |
| `-run-resolved` | none | The program for resolved events; without it they are acknowledged and nothing runs |
| `-concurrency` | `2` | Programs running at once; the rest wait. At most 32 |
| `-timeout` | `30m` | The time a program gets before its process group is killed |
| `-log-dir` | `/var/lib/beacon-oncall/logs` | Each run's output, as `<time>-<delivery>.log` |
| `-keep` | `720h` | Logs older than this are removed; `0` keeps them all |

`beacon-oncall version` prints the version, commit and build. The wrappers around Claude Code and Codex, and the unit that runs it, are in `contrib/oncall` in the repository.
