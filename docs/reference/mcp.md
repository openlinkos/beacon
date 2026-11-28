# MCP tools

Beacon's MCP server lets an agent work an alert and the machines behind it with tools, without learning Beacon's API. This page lists every tool and the scope it needs.

The agent may be Claude Code, Codex, or any client that speaks MCP over HTTP. With the tools it can:

- read the alerts and their evidence, and leave notes;
- have the probes check again;
- run commands and edit files on machines;
- plan maintenance;
- register its own SSH key, and keep its own token alive.

The server is at `/mcp` on port 17001 (`https://status.example.com/mcp` in the reference deployment). It speaks streamable HTTP and is stateless. It takes the same credentials and scopes as the [REST API](api.md), and each tool calls the same method as its REST route.

The machine tools take the names and the behaviour of an agent's own tools (run a command, read a file, write a file, edit a file). Results are structured and free of terminal control codes. Long output is cut, with a way to get the rest. Every write and every session is recorded with the caller's name (`alice / claude-code` for an agent, a token's own name otherwise) and echoed to the alert chat.

## Connecting

Claude Code and Codex sign in through the browser, with MCP's OAuth ([Claude Code](../integrations/claude-code.md)):

1. Add Beacon without a token.
2. Sign in from the client.
3. Allow it on Beacon's consent page as one of your agents.

In Claude Code:

```bash
claude mcp add --scope user --transport http beacon https://status.example.com/mcp
```

Then run `/mcp` inside Claude Code, choose `beacon` and authenticate, or run `claude mcp login beacon` from the shell. In Codex, run `codex mcp add beacon --url https://status.example.com/mcp`, then `codex mcp login beacon`.

An agent without a browser sends an agent's token instead. Make one on the console's **Agents** page or with `beacon agent add` ([Getting a credential](api.md#getting-a-credential)). The On call preset covers working alerts on machines.

With Claude Code, add the token with `--header "Authorization: Bearer lbt_…"`. With Codex, set it in `~/.codex/config.toml`, with the token in an environment variable:

```toml
[mcp_servers.beacon]
url = "https://status.example.com/mcp"
bearer_token_env_var = "BEACON_TOKEN"
```

Any other client that speaks MCP over HTTP connects in one of these two ways. What a client meets on the way:

- **No credential, or one that does not verify.** The request gets a `401` whose `WWW-Authenticate` names the protected resource metadata (`Bearer resource_metadata="https://status.example.com/.well-known/oauth-protected-resource/mcp"`). There an MCP client finds how to sign in.
- **Too many failed attempts.** An address locked out after 20 failed attempts in 10 minutes gets a 401 as well, because the MCP library has no 429. The text says the address is locked and how long to wait (`too many failed sign-ins from this address; try again in 9 minutes`). The response carries `Retry-After`: the same wait the [REST API](api.md) reports as `auth.too_many_failures`.
- **The server's instructions.** They tell the agent what each group of tools is for, and how to use the write tools with care.
- **A missing scope.** A credential sees every tool, but a call without the scope it needs is refused. The message names the scope. For an agent, it also says what its preset within its owner's role allows (`alice / claude-code may not use the config:write scope: its preset (oncall) within its owner's role allows status:read, sessions:read, alerts:write, machines:ssh; …`).

## Capability discovery, domains, resources and prompts {#capabilities}

Beacon organises all operations into six canonical domains — `incidents`, `monitoring`, `machines`, `status`, `identity` and `governance` — shared across MCP tools, OpenAPI tags, `GET /api/v1/capabilities` and the console's **MCP & API** view. Every registered MCP tool carries structured `_meta` fields (`domain`, `access`, `rest` and `scopes`) so clients can inspect access level and REST parity without parsing prose.

Call `describe_capabilities` first in a new session to discover your active scopes, effective machine scope, enabled server subsystems and which operations are currently available to your credential.

| Tool | Scope | What it does |
| --- | --- | --- |
| `describe_capabilities` | any | Live capability catalogue for the caller: identity, active scopes, machine scope, enabled subsystems, all six operational domains with per-operation availability, MCP resources and operational prompts |

### MCP resources {#resources}

Read-only snapshots and templates exposed via MCP `resources/list`, `resources/templates/list` and `resources/read`, enforcing the same scope checks as tools:

| URI | Scope | What it returns |
| --- | --- | --- |
| `beacon://capabilities` | any | Same JSON payload as `describe_capabilities` |
| `beacon://overview` | `status:read` | Bridge readiness, configured human routes, firing counts and API budget |
| `beacon://alerts/firing` | `status:read` | Currently firing alert groups |
| `beacon://machines` | `status:read` or `machines:ssh` | Enrolled machines, connector state and SSH config |
| `beacon://silences` | `status:read` | Active and pending silences |
| `beacon://maintenance` | `status:read` | Active, planned and recent maintenance windows |
| `beacon://alerts/{id}/evidence` | `status:read` | Full diagnostic evidence for one alert group |
| `beacon://alerts/{id}/timeline` | `status:read` | Chronological event timeline for one alert group |
| `beacon://sessions/{id}` | `sessions:read` or `machines:ssh` | Transcript and command index for one SSH session |
| `beacon://status/{name}` | `status:read` | Computed public status page JSON (when status pages are enabled) |
| `beacon://config/{kind}` | `status:read` or `config:write` | Configuration items for `locations`, `heartbeats`, `resources`, `targets`, `modules` or `status_pages` (when configuration storage is enabled) |

### MCP prompts {#prompts}

Reusable operational playbooks exposed via MCP `prompts/list` and `prompts/get`:

| Prompt | Arguments | Domain | Purpose |
| --- | --- | --- | --- |
| `triage_alert` | `alert_id` (required) | `incidents` | Guided diagnostic workflow using `get_evidence`, `get_timeline`, `check_machine_ssh`, read-only inspection and handover notes |
| `verify_recovery` | `alert_id` (required), `summary` (optional) | `incidents` | Run episode-bound `probe_now` across locations and record a structured verification note |
| `fleet_health` | — | `monitoring` | Audit firing alerts, probe readiness, machine connectors, SSH channel posture and upcoming maintenance |
| `shift_handover` | `window` (optional, default `12h`) | `incidents` | Summarise open alerts, recent episodes, deployments, SSH sessions and upcoming maintenance for the next shift |
| `onboard_monitor` | `service` (required), `url` (required) | `monitoring` | Inspect existing configuration and safely stage an optimistic-concurrency `put_config` change |

## Alerts

| Tool | Scope | What it does |
| --- | --- | --- |
| `status_summary` | `status:read` | Alerting pipeline health, configured human notification routes, firing groups by severity, acknowledgement counts and the Feishu API budget |
| `list_alerts` | `status:read` | Alert groups, firing first and newest first, with who acknowledged or silenced them and how far escalation went. `state: all` adds those resolved in the last seven days |
| `get_alert` | `status:read` | One group by `id`: labels, runbook and dashboard, acknowledgement, silence, escalation, notes, the agents woken, and the signed link to its page in the console |
| `list_silences` | `status:read` | Silences active or pending, ending soonest first |
| `list_audit` | `status:read` | The latest writes with who made them and through what, newest first (`limit`, 1 to 200, 50 by default) |
| `ack_alert` | `alerts:write` | Acknowledge a firing group (`id`, `note`): escalation to phones stops for `ack_ttl` (4 hours by default). Say what is being done in the note |
| `silence_alert` | `alerts:write` | Silence a firing group (`id`, `duration` of at most 24 hours, a required `comment`); it also acknowledges the group. An agent's silence is recorded but people are still notified |
| `expire_silence` | `alerts:write` | End a silence now (`silence_id`), so the alerts it covered notify again |

## Working an alert

| Tool | Scope | What it does |
| --- | --- | --- |
| `get_evidence` | `status:read` | Everything to judge a group in one call (`id`, `before`: the window opens 30 minutes before the group started, at most 24 hours): each location's result now, since when it fails and why, the duration, HTTP status, attempts and certificate expiry; designated service machines within the caller's SSH scope; related groups in the same failure domain; change events; what people and agents have done; links |
| `get_timeline` | `status:read` | The group's story in time order: started, notified, buzzed, Pushover, acknowledged, notes, silences, agents woken, changes, sessions and their commands, resolved |
| `list_incident_outcomes` | `status:read` | Private resolved episodes (`group`, `severity`, `since`, `origin`, `evidence`, `before`, `limit` default 50, up to 200). Pass the returned cursor as `before` to continue |
| `list_incident_episodes` | `status:read` | Open and resolved episodes by start time (`since`, `until`, `group`, `severity`, `origin`, `before` and `limit`) |
| `get_incident_outcome` | `status:read` | Read one retained outcome by episode ID with participation, the latest on-demand check and the retained complete pass near rule recovery when available. To find related audit entries use `list_audit` with `target` set to its group and the episode's dates |
| `add_note` | `alerts:write` | Leave a note on a group, firing or resolved (`id`, `text` of at most 2000 characters): what was found, what was done, what is next. People on call see it on the card; a note that begins with `public:` is also an update on the status pages. It does not acknowledge |
| `check_drill_hook` | `alerts:report` | Check the loaded settings of a named hook assigned to this reporter (`name`), including signature setup, whether it matches a critical alert for `drill`, whether its service filter is limited to `drill`, and the configured critical chat route. Sends no delivery |
| `report_agent_run` | `alerts:report` | Record the observed `status` and any known `exit_code` for an alert's `id` and hook `delivery`. This credential must resolve to the delivery's original and still-configured reporter. A late report can update the delivery while its original outcome and reporter binding are retained |
| `list_changes` | `status:read` | Change events, newest first: deployments, configuration changes and Flux reconciliations (`service`, `machine`, `source`, `since`, 24 hours by default, `limit`, 100 by default, at most 1000). `untrusted` lists the fields that hold the senders' text |
| `probe_now` | `alerts:write` | Have every location that probes a target probe it again at once, and get each location's answer within the wait (`alert_id`, `target` with an optional `module`, or `service`; `wait` seconds, 45 by default, at most 90). Use it after a fix, instead of waiting for the next cycle |

To verify a specific incident with `probe_now`, pass `alert_id` and set `expected_episode` to the `episode` from `list_alerts`, or `alert.episode` from `get_evidence`, `get_timeline` or the signed wake. A replaced episode produces a tool error beginning with `incident.changed`. Read the current evidence before deciding whether to repeat the check. Omitting `expected_episode` continues to select the current episode. Checks by `target` or `service` are independent of incident verification. Incident checks use each alert member’s known labels, including its failure domain. The tool reports when any member has no matching target at an active location.

Beacon commits the incident check together with its audit entry. If storage rejects the write, the tool returns an error beginning with `probe.not_recorded`. Retry once storage is available.

Use `list_incident_episodes` for a start-time review, including episodes still open. Supply RFC 3339 `since` (inclusive) and `until` (exclusive) for a window up to 31 days. The default is the last 24 hours. Repeat the returned `since` and `until` with `before` and the same filters when paging. The returned `retained_from` gives the recovery-time retention boundary for resolved records. Hook acceptance, programme reports, human participation and passing checks carry separate evidence.

Filter resolved outcomes by `severity` (`critical`, `warning` or `info`), `since` (such as `7d` or an RFC 3339 time), and `evidence` before paging. This time filter uses rule recovery, within the configured retention period.

- `agent_report` matches a programme report for a firing delivery, including `not_started`. A report for a recovery delivery does not qualify.
- `human_ack` matches an identified person's explicit acknowledgement. The outcome's `human_acknowledgement` keeps the person's name, source and time. Device receipts, silences and notes are recorded separately.
- `passing_check` matches a retained complete passing check, even when the latest check has missing results.
- `agent_passing_check` matches a retained complete pass requested by an identified Agent. It filters by who requested verification.

The corresponding `no_agent_report`, `no_human_ack` and `no_passing_check` mean the retained episode has no such evidence. Older records may be incomplete. These fields are insufficient to establish who repaired the incident.

For `origin`, `exercise` selects episodes with verified exercise observations at the first failing quorum. `unknown` includes episodes with no verified origin.

The caller's machine selection also limits session records in the alert evidence, the timeline and retained outcomes. SSH audit entries carry a machine name without an enrolment identifier. Their command previews require unrestricted machine selection and a session-detail scope.

An outcome's `agent_deliveries` gives the hook status and any programme report for that exact alert episode. `status: ok` records hook acceptance. An optional `run` records the runner's observed programme state and exit code when available. Older archived outcomes may have no delivery records.

## Machines

| Tool | Scope | What it does |
| --- | --- | --- |
| `list_machines` | `status:read` or `machines:ssh` | The machines, whether you reach each, and an `ssh_config` block with a `known_hosts` line for the agent's own ssh, scp and rsync |
| `get_ssh_status` | `status:read` or `machines:ssh` | The SSH channel state (paused or active, pause time), channel host key and the certificate authority public key machines trust |
| `check_machine_ssh` | `machines:ssh` | Check the registered route, pinned host key and certificate authentication for an enrolled `machine` you may reach. Returns the observation and audit ID |
| `run_command` | `machines:ssh` | Run a shell command on a machine (`machine`, `command`, `cwd`, `stdin`, `timeout` in seconds, 120 by default, at most 600), as its user, without a terminal: exit code, stdout and stderr as plain text. A stream over 30,000 characters keeps its start and end, with a line saying how much was cut; `get_session` reads what was retained |
| `search_files` | `machines:ssh` | Find literal, case-sensitive text on one enrolled `machine` under an explicit absolute `directory`, with `query` and optional `limit`. Returns paths, line numbers, excerpts and the session ID |
| `read_file` | `machines:ssh` | Lines of a text file (`machine`, `path`, `offset`, `limit`: 2000 lines by default, at most 10,000), each as its number, a tab and the line, like `cat -n`; lines over 2000 characters are cut. The answer says how many lines the file has and whether more follow |
| `write_file` | `machines:ssh` | Write a whole file (`machine`, `path`, `content` of at most 1 MiB), making its directory if needed; an existing file keeps its owner and mode |
| `edit_file` | `machines:ssh` | Replace an exact string in a text file (`machine`, `path`, `old_string`, `new_string`, `replace_all`). `old_string` must occur once unless `replace_all` is set; refused, rather than overwrite, if the file changed meanwhile |
| `list_sessions` | `status:read` or `machines:ssh` | Sessions through the SSH channel and the tools, newest first (`machine`, `agent`, `limit`, 50 by default). Beacon applies the credential's machine restrictions before the limit. Readers with `status:read` alone receive metadata |
| `get_session` | `sessions:read` or `machines:ssh` | A visible session’s evidence (`id`): retained command output, indexed commands and exit status when available, or session metadata. The `page` field leads to console sign-in |
| `add_machine` | `machines:write` | Add a machine, pinning its host key (`name`, `address`, `user`, `critical`, `note`, `replace`); `address: connector` for one behind NAT, with `connector_key` and optionally `host_key`. The result says what the machine needs. This and every other machine change takes access to all machines; a critical machine or the `critical` flag takes an owner, and `user` a person ([Changing machines](permissions.md#changing-machines)) |
| `update_machine` | `machines:write` | Change a machine's note, whether it is critical, and the account on it (`name`, and `note`, `critical`, `user`, each optional). A field left out stays. The optional `if_version` guards against concurrent edits. The host key and the address stay |
| `machine_history` | `status:read` or `machines:ssh` | Read the settings history for `name`. Use `before` (exclusive) and `limit` to page |
| `restore_machine` | `machines:write` | Restore an earlier settings `version` for `name`. Supply the current `if_version`. Copies the account, critical flag and note into a new version |
| `set_machine_connector` | `machines:write` | Set or change the key a machine behind NAT connects out with (`name`, `key`); a connection made with the old key is closed |
| `remove_machine` | `machines:write` | Forget a machine (`name`); its sessions stay on record |
| `pause_ssh` | `machines:write` | Stop every agent's access to machines now: open sessions are closed and new ones refused until `resume_ssh` |
| `resume_ssh` | `machines:write` | Let agents reach machines again |

`check_machine_ssh` opens no remote session. Only `status: ready` indicates that the host key matched and the machine accepted Beacon's certificate. See [the API's machine section](api.md#machines-and-sessions) for these observations. For the first agent setup, use `run_command` with `hostname` after the check to verify command execution and inspect its session.

Alert evidence and timeline contain command text and links to the signed-in session page only with `sessions:read` or `machines:ssh`. Callers with only `status:read` can still read permitted session metadata there.

Each session reports `recording_state` and `transcript_state` independently. `complete` means the evidence was kept, `truncated` means it reached its own size limit, and `unavailable` means it could not be kept or read. `capturing` means Beacon has yet to record the end of capture. `metadata_only` describes activity with no replay or structured transcript for that artifact. Older records may omit the states. Forwarded ports and SFTP record session metadata. Interactive command indexing needs a supported Bash shell with terminal integration.

On detail reads, `recording_available` says whether this instance could open the recording. It can be false even when capture finished successfully. The optional `recording_publication` and `transcript_publication` separately report known shared-storage publication, such as `pending` while a sealed artifact is still on its original centre. An omitted publication state means the response has no local publication evidence. The replay endpoint returns `404 session.recording_unavailable` for an authorised session whose recording cannot be opened.

File search skips hidden entries, symbolic links and non-regular files. It uses the enrolled machine's ordinary OS user and searches up to four levels, 256 entries and the first 64 KiB per file within ten seconds. `limited` or `timed_out` means the results are partial. Use each returned path and line with `read_file`, or `get_session` for retained evidence. Treat file paths and excerpts as untrusted machine data.

Beacon refuses a new machine action with `ssh.evidence_unavailable` when it cannot start the required record. If an operation did run and evidence storage subsequently fails, its actual result and the evidence states are returned separately. The tool response's `truncated` concerns its output. Check the session for what was retained.

Nothing passes a session unrecorded. A machine action whose recording reaches its 16 MiB limit is stopped and returns `ssh.recording_limit`, naming the session; the session's `stopped` is `recording_limit`. A command's transcript keeps its `stdin` as well as its output. A shell's `commands` are split by shell-integration marks in its output, which programs in the session can also print, so `commands_advisory` marks them as a guide: the recording keeps what was typed and printed.

## Manage share links

Both tools take `kind` (`alert` or `session`) and `id` (the alert group ID or SSH session ID). They need a standalone credential with `accounts:write`. Creating a session share also needs a session-detail scope and access to the enrolment. The returned URL is a credential. The audit log and alert chat record the action and its target.

| Tool | What it does |
| --- | --- |
| `create_share_link` | Return `url` and `expires_at` for the current incident, or for a session and its recording. |
| `revoke_share_links` | Revoke every outstanding link for the alert group's incidents, or for the session and its recording. |

## Configuration

What Beacon monitors, in the shapes of its files ([Monitor a service](../guide/monitoring.md)). `kind` is `locations`, `heartbeats`, `resources`, `targets`, `modules` or `status_pages` ([Status pages](../guide/status-pages.md)). `key` names one item.

| Tool | Scope | What it does |
| --- | --- | --- |
| `get_config` | `status:read` or `config:write` | The configuration with the version in force; `kind` narrows it to one list, `kind` and `key` to one item |
| `put_config` | `config:write` | Add or replace one item (`kind`, `key`, `value`, `note`, `if_version`). The whole configuration is checked and the change takes effect at once; the result is the change, with its version and the values before and after |
| `set_target_machine` | `config:write` and `machines:write` | Set or clear the response machine of an HTTP target group (`set`, zero-based `group`, `if_version`, optional `note`, and `machine` from `list_machines` or `clear: true`). Requires SSH access to the previous and selected machines, when present |
| `delete_config` | `config:write` | Remove one item (`kind`, `key`, `note`, `if_version`) |
| `list_config_changes` | `status:read` or `config:write` | Changes, newest first (`kind`, `key`, `before`, `limit`, 20 by default, at most 200): version, when, who, through what, why, and the items added, changed or removed |
| `get_config_change` | `status:read` or `config:write` | One change (`version`), with every item's value before and after |
| `revert_config_change` | `config:write` | Undo one change as a new change (`version`, `note`); refused when a later change touched one of its items |

Pass the version you read as `if_version`. A change made on top of a later version is refused with `config.conflict`, so an agent never overwrites what someone changed meanwhile. A change that would not pass the checks is refused with `config.invalid` and the reason, and nothing changes.

## Status pages and maintenance

The public status pages are made from the alert groups and the maintenance windows ([Status pages](../guide/status-pages.md)). A note left with `add_note` that begins with `public:` is an update of the incident for the public.

| Tool | Scope | What it does |
| --- | --- | --- |
| `get_status_page` | `status:read` | A status page as its public JSON has it (`name`, `language`): the overall state, each component's state now and over 90 days, the incidents (active, then the last 30 days) with their public updates, the maintenance windows |
| `list_maintenance` | `status:read` | The maintenance windows, the latest to start first, ended ones kept for 91 days: services, when, title, who planned them, their silences |
| `schedule_maintenance` | `maintenance:write` | Plan a window (`services`, `starts_at`, `ends_at`, `title`, `description`): the pages announce it, and the services' alerts are silenced for it. At most 7 days, starting within 90, never in the past. An agent's window silences as an agent's silence does: people are still paged, and the pages still show an outage |
| `end_maintenance` | `maintenance:write` | End a window now, or call it off before it begins (`id`): its silences end and the pages say so |

## People and SSH keys

| Tool | Scope | What it does |
| --- | --- | --- |
| `list_users` | `accounts:write` | The people who sign in to Beacon (username, display name, role, last sign-in, whether disabled, their agents), the invitations not yet used and the password reset links not yet used, never with their codes |
| `invite_user` | `accounts:write` | A one-time link, valid for 24 hours, with which a person joins with a `role` (and a `note` saying who it is for); hand it to that person only. The token must hold every scope of the role |
| `withdraw_link` | `accounts:write` | Withdraw a pending invitation or password reset link by its `id`, as `list_users` gives it; it stops working at once |
| `list_agents` | owner or person (never an agent) | List agents with their owner, preset, effective scopes and machine access (optional `owner` filter for administrators) |
| `get_agent` | owner or person (never an agent) | Inspect one agent by `name`, including its effective scopes, machine access, tokens, OAuth grants and SSH keys |
| `create_agent` | any credential; the bridge decides | Make an agent (`name`, `owner`, `preset`, `scopes`, `machine_access`, `machines`, `expires`). `machine_access` is `all` or `named`; without it and `machines` the agent reaches no machine. Only a person gives `all`, and only an owner names a critical machine. Custom scopes need `accounts:write` from a person or a legacy token, never an agent, within the owner's role and within what the caller may do. A caller never gives an agent a scope or a machine it does not hold itself. `expires` (such as `90d`) returns a token, its secret shown once |
| `update_agent` | any credential; the bridge decides | Change an agent's preset, custom scopes or machine access (`name`, and `preset`, `scopes`, `machine_access`, `machines`, each optional). A field left out stays; an empty `machines` list names none. Custom scopes follow the same rule as `create_agent`. A caller never gives an agent a scope or a machine it does not hold itself; narrowing an agent stays allowed |
| `add_ssh_key` | `machines:ssh` | Register your own public key for the SSH channel (`key`, an `authorized_keys` line; `comment`), so that your own ssh, scp, sftp and rsync reach the machines you may reach; `list_machines` then gives the `ssh_config` block. The key belongs to the credential you call with (`via`): it works while that token or OAuth sign-in does and is removed with it. Shown to the person you act for, and echoed to the alert chat |
| `list_ssh_keys` | `machines:ssh` | Your SSH keys |
| `remove_ssh_key` | `machines:ssh` | Remove one of your keys (`fingerprint`); connections signed in with it are closed |

Account administration tools require an owner credential issued independently of any Agent. Existing custom Agent credentials with `accounts:write` cannot administer accounts or Agent identities. Agents with SSH access can manage their own SSH keys. People change roles, disable accounts and reset passwords in the console, through REST or on the centre.

## Team credentials {#team-credentials}

These tools are offered only when the [team credential store](../guide/team-credentials.md) is enabled. A grant supports repeated uses until it expires or an owner revokes it. Each use returns only an HTTP status code.

| Tool | Scope | What it does |
| --- | --- | --- |
| `list_team_credentials` | `secrets:use` | List the agent's active team credential grants |
| `use_team_credential` | `secrets:use` | Send the configured HTTPS request under the agent's audited identity |

People manage credential values and grants from their own console accounts. Use the agent's headless token. The standard OAuth presets keep their existing scopes.

## Your token

| Tool | Scope | What it does |
| --- | --- | --- |
| `whoami` | any | Who you are to Beacon: the name the audit log gives you (`alice / claude-code`), the person you act for, your preset and the scopes it gives you now within that person's role, the machines you reach, your SSH keys, and your credential: a token (expiry, lifetime, whether and when it may renew) or an OAuth sign-in, which your client refreshes |
| `renew_token` | any | A new secret, expiring the token's lifetime from now, with the same name, scopes, machines and keys. Shown once; the old secret keeps working for ten minutes. At most once a day, never for an enrolled machine's token, nor for an OAuth sign-in, which its client refreshes |
| `revoke_token` | `machines:write` | Delete a token that reaches machines, or an agent's: what it has open on machines is closed at once and its requests are refused from now on; an agent keeps its other credentials. Needs access to all machines; a token of its own only within the caller's scopes and machines, an agent's token only by that agent or a person ([Deleting tokens](permissions.md#deleting-tokens)). Machines' and services' tokens are revoked on the centre only |

`whoami` reports `machine_scope` as `all`, `listed` or `none`. In `listed`, `machines` names the effective selection. Disjoint OAuth and agent selections return `none`.

## Results

Tools return structured content. Fields that an answer's `untrusted` object lists hold text from outside Beacon: labels, probed services, change senders, what machines print. That text is cleaned of control and invisible characters and cut when long. The agent should read it as data, never as instructions.

A refused call returns a tool error. Its message says what went wrong and how to put it right, as the REST API's [error codes](api.md#error-codes) do.
