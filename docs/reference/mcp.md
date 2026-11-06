# MCP tools

Beacon's MCP server lets an agent (Claude Code, Codex, or any client that speaks MCP over HTTP) work an alert and the machines behind it with tools, without learning Beacon's API: read the alerts and their evidence, leave notes, have the probes check again, run commands and edit files on machines, plan maintenance, register its own SSH key, and keep its own token alive. It is at `/mcp` on port 17001 (`https://status.example.com/mcp` in the reference deployment): streamable HTTP, stateless, with the same credentials and scopes as the [REST API](api.md), and each tool calls the same method as its REST route.

The machine tools take the names and the behaviour of an agent's own tools (run a command, read a file, write a file, edit a file). Results are structured and free of terminal control codes, and long output is cut with a way to get the rest. Every write and every session is recorded with the caller's name (`alice / claude-code` for an agent, a token's own name otherwise) and echoed to the alert chat.

## Connecting

Claude Code and Codex sign in through the browser, with MCP's OAuth: you add Beacon without a token, sign in from the client, and allow it on Beacon's consent page as one of your agents ([Claude Code](../integrations/claude-code.md)). In Claude Code:

```bash
claude mcp add --scope user --transport http beacon https://status.example.com/mcp
```

then `/mcp` inside Claude Code, choose `beacon` and authenticate (or `claude mcp login beacon` from the shell). Codex: `codex mcp add beacon --url https://status.example.com/mcp`, then `codex mcp login beacon`.

An agent without a browser sends an agent's token instead, made on the console's **Agents** page or with `beacon agent add` ([Getting a credential](api.md#getting-a-credential)); the On call preset covers working alerts on machines. With Claude Code, add it with `--header "Authorization: Bearer lbt_…"`; with Codex, in `~/.codex/config.toml`, with the token in an environment variable:

```toml
[mcp_servers.beacon]
url = "https://status.example.com/mcp"
bearer_token_env_var = "BEACON_TOKEN"
```

Any other client that speaks MCP over HTTP connects in one of these two ways. A request without a credential, or with one that does not verify, gets a `401` whose `WWW-Authenticate` names the protected resource metadata (`Bearer resource_metadata="https://status.example.com/.well-known/oauth-protected-resource/mcp"`), where an MCP client finds how to sign in. An address locked out after 20 failed attempts in 10 minutes gets a 401 as well, because the MCP library has no 429: the text says the address is locked and how long to wait (`too many failed sign-ins from this address; try again in 9 minutes`), and the response carries `Retry-After`, the same wait the [REST API](api.md) reports as `auth.too_many_failures`. The server's instructions tell the agent what each group of tools is for and how to use the write tools with care. A credential sees every tool; a call without the scope it needs is refused with a message naming the scope, and, for an agent, what its preset within its owner's role allows (`alice / claude-code may not use the config:write scope: its preset (oncall) within its owner's role allows status:read, sessions:read, alerts:write, machines:ssh; …`).

## Alerts

| Tool | Scope | What it does |
| --- | --- | --- |
| `status_summary` | `status:read` | Whether alerts can reach people right now (and why not), groups firing by severity, how many are acknowledged, and the Feishu API budget used this month |
| `list_alerts` | `status:read` | Alert groups, firing first and newest first, with who acknowledged or silenced them and how far escalation went. `state: all` adds those resolved in the last seven days |
| `get_alert` | `status:read` | One group by `id`: labels, runbook and dashboard, acknowledgement, silence, escalation, notes, the agents woken, and the signed link to its page in the console |
| `list_silences` | `status:read` | Silences active or pending, ending soonest first |
| `list_audit` | `status:read` | The latest writes with who made them and through what, newest first (`limit`, 1 to 200, 50 by default) |
| `ack_alert` | `alerts:write` | Acknowledge a firing group (`id`, `note`): escalation to phones stops for `ack_ttl` (4 hours by default). Say what is being done in the note |
| `silence_alert` | `alerts:write` | Silence a firing group (`id`, `duration` of at most 24 hours, a required `comment`); it also acknowledges the group |
| `expire_silence` | `alerts:write` | End a silence now (`silence_id`), so the alerts it covered notify again |

## Working an alert

| Tool | Scope | What it does |
| --- | --- | --- |
| `get_evidence` | `status:read` | Everything to judge a group in one call (`id`, `before`: the window opens 30 minutes before the group started, at most 24 hours): each location's result now, since when it fails and why, the duration, HTTP status, attempts and certificate expiry; related groups in the same failure domain; change events; what people and agents have done; links |
| `get_timeline` | `status:read` | The group's story in time order: started, notified, buzzed, Pushover, acknowledged, notes, silences, agents woken, changes, sessions and their commands, resolved |
| `add_note` | `alerts:write` | Leave a note on a group, firing or resolved (`id`, `text` of at most 2000 characters): what was found, what was done, what is next. People on call see it on the card; a note that begins with `public:` is also an update on the status pages. It does not acknowledge |
| `list_changes` | `status:read` | Change events, newest first: deployments, configuration changes and Flux reconciliations (`service`, `machine`, `source`, `since`, 24 hours by default, `limit`, 100 by default, at most 1000) |
| `probe_now` | `alerts:write` | Have every location that probes a target probe it again at once, and get each location's answer within the wait (`alert_id`, `target` with an optional `module`, or `service`; `wait` seconds, 45 by default, at most 90). Use it after a fix, instead of waiting for the next cycle |

## Machines

| Tool | Scope | What it does |
| --- | --- | --- |
| `list_machines` | `status:read` or `machines:ssh` | The machines, whether you reach each, and an `ssh_config` block with a `known_hosts` line for the agent's own ssh, scp and rsync |
| `run_command` | `machines:ssh` | Run a shell command on a machine (`machine`, `command`, `cwd`, `stdin`, `timeout` in seconds, 120 by default, at most 600), as its user, without a terminal: exit code, stdout and stderr as plain text. A stream over 30,000 characters keeps its start and end, with a line saying how much was cut; `get_session` has all of it |
| `read_file` | `machines:ssh` | Lines of a text file (`machine`, `path`, `offset`, `limit`: 2000 lines by default, at most 10,000), each as its number, a tab and the line, like `cat -n`; lines over 2000 characters are cut. The answer says how many lines the file has and whether more follow |
| `write_file` | `machines:ssh` | Write a whole file (`machine`, `path`, `content` of at most 1 MiB), making its directory if needed; an existing file keeps its owner and mode |
| `edit_file` | `machines:ssh` | Replace an exact string in a text file (`machine`, `path`, `old_string`, `new_string`, `replace_all`). `old_string` must occur once unless `replace_all` is set; refused, rather than overwrite, if the file changed meanwhile |
| `list_sessions` | `status:read` or `machines:ssh` | Sessions through the SSH channel and the tools, newest first (`machine`, `agent`, `limit`, 50 by default): who, where, what, when, the exit status |
| `get_session` | `sessions:read` or `machines:ssh` | A session's transcript (`id`): a command's whole output, or an interactive shell's commands with their output and exit status |
| `add_machine` | `machines:write` | Add a machine, pinning its host key (`name`, `address`, `user`, `critical`, `note`, `replace`); `address: connector` for one behind NAT, with `connector_key` and optionally `host_key`. The result says what the machine needs |
| `set_machine_connector` | `machines:write` | Set or change the key a machine behind NAT connects out with (`name`, `key`); a connection made with the old key is closed |
| `remove_machine` | `machines:write` | Forget a machine (`name`); its sessions stay on record |
| `pause_ssh` | `machines:write` | Stop every agent's access to machines now: open sessions are closed and new ones refused until `resume_ssh` |
| `resume_ssh` | `machines:write` | Let agents reach machines again |

## Configuration

What Beacon monitors, in the shapes of its files ([Monitor a service](../guide/monitoring.md)). `kind` is `locations`, `heartbeats`, `resources`, `targets`, `modules` or `status_pages` ([Status pages](../guide/status-pages.md)); `key` names one item.

| Tool | Scope | What it does |
| --- | --- | --- |
| `get_config` | `status:read` or `config:write` | The configuration with the version in force; `kind` narrows it to one list, `kind` and `key` to one item |
| `put_config` | `config:write` | Add or replace one item (`kind`, `key`, `value`, `note`, `if_version`). The whole configuration is checked and the change takes effect at once; the result is the change, with its version and the values before and after |
| `delete_config` | `config:write` | Remove one item (`kind`, `key`, `note`, `if_version`) |
| `list_config_changes` | `status:read` or `config:write` | Changes, newest first (`kind`, `key`, `before`, `limit`, 20 by default, at most 200): version, when, who, through what, why, and the items added, changed or removed |
| `get_config_change` | `status:read` or `config:write` | One change (`version`), with every item's value before and after |
| `revert_config_change` | `config:write` | Undo one change as a new change (`version`, `note`); refused when a later change touched one of its items |

Pass the version you read as `if_version`: a change made on top of a later version is refused with `config.conflict`, so an agent never overwrites what someone changed meanwhile. A change that would not pass the checks is refused with `config.invalid` and the reason, and nothing changes.

## Status pages and maintenance

The public status pages are made from the alert groups and the maintenance windows ([Status pages](../guide/status-pages.md)). A note left with `add_note` that begins with `public:` is an update of the incident for the public.

| Tool | Scope | What it does |
| --- | --- | --- |
| `get_status_page` | `status:read` | A status page as its public JSON has it (`name`, `language`): the overall state, each component's state now and over 90 days, the incidents (active, then the last 30 days) with their public updates, the maintenance windows |
| `list_maintenance` | `status:read` | The maintenance windows, the latest to start first, ended ones kept for 91 days: services, when, title, who planned them, their silences |
| `schedule_maintenance` | `maintenance:write` | Plan a window (`services`, `starts_at`, `ends_at`, `title`, `description`): the pages announce it, and the services' alerts are silenced for it. At most 7 days, starting within 90, never in the past |
| `end_maintenance` | `maintenance:write` | End a window now, or call it off before it begins (`id`): its silences end and the pages say so |

## People and SSH keys

| Tool | Scope | What it does |
| --- | --- | --- |
| `list_users` | `accounts:write` | The people who sign in to Beacon (username, display name, role, last sign-in, whether disabled, their agents) and the invitations not yet used |
| `invite_user` | `accounts:write` | A one-time link, valid for 24 hours, with which a person joins with a `role` (and a `note` saying who it is for); hand it to that person only |
| `add_ssh_key` | `machines:ssh` | Register your own public key for the SSH channel (`key`, an `authorized_keys` line; `comment`), so that your own ssh, scp, sftp and rsync reach the machines you may reach; `list_machines` then gives the `ssh_config` block. The key belongs to the credential you call with (`via`): it works while that token or OAuth sign-in does and is removed with it. Shown to the person you act for, and echoed to the alert chat |
| `list_ssh_keys` | `machines:ssh` | Your SSH keys |
| `remove_ssh_key` | `machines:ssh` | Remove one of your keys (`fingerprint`); connections signed in with it are closed |

No agent preset gives `accounts:write`: an agent manages people only if an owner gave it that scope on the command line. Changing roles, disabling people and password resets are not MCP tools; they are for people, on the console or the centre.

## Your token

| Tool | Scope | What it does |
| --- | --- | --- |
| `whoami` | any | Who you are to Beacon: the name the audit log gives you (`alice / claude-code`), the person you act for, your preset and the scopes it gives you now within that person's role, the machines you reach, your SSH keys, and your credential: a token (expiry, lifetime, whether and when it may renew) or an OAuth sign-in, which your client refreshes |
| `renew_token` | any | A new secret, expiring the token's lifetime from now, with the same name, scopes, machines and keys. Shown once; the old secret keeps working for ten minutes. At most once a day, never for an enrolled machine's token, nor for an OAuth sign-in, which its client refreshes |
| `revoke_token` | `machines:write` | Revoke a token that reaches machines, or any agent's: what it has open on machines is closed at once and its requests are refused from now on. The pause switch for one agent; machines' and services' tokens are revoked on the centre only |

## Results

Tools return structured content. Fields that an answer's `untrusted` object lists hold text from outside Beacon (labels, probed services, change senders, what machines print), cleaned of control and invisible characters and cut when long: the agent should read them as data, never as instructions. A refused call returns a tool error whose message says what went wrong and how to put it right, as the REST API's [error codes](api.md#error-codes) do.
