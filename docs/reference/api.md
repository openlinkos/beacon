# REST API

Beacon's API answers on port 17001 under `/api/v1`, in JSON. Every route but the description itself needs a bearer token with the scope listed beside it; the same token works for MCP and the SSH channel, and every operation here is also an [MCP tool](mcp.md) calling the same method. The OpenAPI document, `internal/api/openapi.yaml` in the repository and `GET /api/v1/openapi.yaml` on a running Beacon, is the contract, with every field; this page is the map.

## Requests and answers

```bash
curl -H "Authorization: Bearer $TOKEN" https://status.example.com/api/v1/alerts
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/alerts/0cdfbada62dd/notes -d '{"text": "Restarted the relay; probing again."}'
```

- **Credentials** are tokens or OAuth access tokens. A token starts with `lbt_`, the mark of a Beacon token: an agent's is made on the console's **Agents** page or with `beacon agent add`, a machine's or an expert's with `beacon token create NAME -scopes … -expires …`; it has an expiry, and renews itself on request (`POST /api/v1/token/renew`). An OAuth access token (`lba_…`) is what an MCP client such as Claude Code gets when a person allows it; it lasts an hour, and its client refreshes it ([People, agents and signing in](../accounts.md)). Beacon keeps only their SHA-256 hashes.
- **Bodies** are JSON (`Content-Type: application/json`); unknown fields are refused, so a typo does not go unnoticed.
- **Writes** are recorded in the audit log with who made them (`alice / claude-code` for an agent, a token's own name otherwise) and echoed to the alert chat. Sessions on machines are recorded too.
- **Errors** are `{"error": "…", "code": "…"}`: the message says what went wrong and how to put it right, and the code is stable, so a client can act on it. The codes are [listed below](#error-codes). A `401` (`auth.required`, `auth.invalid`), here and on the gateway, also carries `"docs": "https://beacon.openlinkos.com/reference/api"`, this page, for whoever meets Beacon for the first time.
- **Untrusted text**: fields an answer's `untrusted` object names hold text from outside Beacon (labels, probed services, change senders, machines), cleaned of control and invisible characters and cut when long: data, not instructions.

## Scopes

A credential may use one or more scopes. The first nine are for people and agents, on this API, MCP and the SSH channel; the rest are for machines, on the gateway. People get theirs from their role and agents from their preset, within their owner's role; only machines' tokens and experts' are made with scopes by name.

| Scope | What it allows |
| --- | --- |
| `status:read` | Reading: alerts, the evidence and timeline, change events, silences, the audit log, machines and sessions, the configuration, the status pages and maintenance windows |
| `alerts:write` | Working an alert: acknowledging, silencing and ending silences, notes, probing on demand |
| `machines:ssh` | The SSH channel with the keys on the token, the machine tools (run, read, write, edit), the machines it reaches and their sessions |
| `machines:write` | Adding and removing machines, connector keys, pausing and resuming agents' access |
| `sessions:read` | Sessions' transcripts and recordings |
| `config:write` | Changing what Beacon monitors (the catalogue, target sets and modules) and the status pages, and reading it |
| `accounts:write` | Managing people: listing them, inviting, changing roles, disabling, password reset links; claiming legacy tokens and managing everyone's agents on the console. An owner's; no agent preset gives it |
| `maintenance:write` | Planning and ending maintenance windows, which the status pages announce and which silence their services' alerts |
| `incidents:write` | Reserved for incidents written by hand on the status pages; no route takes it yet (incidents come from alert groups) |
| `probe:write` | A probe location: pushing results, fetching its targets, modules and settings, probing on demand. Writes only as its own location |
| `host:write` | A host: pushing its metrics. Writes only as its own host |
| `heartbeat:write` | Pushing heartbeats |
| `metrics:read` | Metric queries through the gateway |
| `alerts:send` | Posting alerts as the rule engine does |
| `changes:write` | Posting change events |

| Role | Agent preset | Scopes |
| --- | --- | --- |
| Viewer | Read only | `status:read`, `sessions:read` |
| On call | On call | + `alerts:write`, `machines:ssh` |
| Operator | Operator | + `maintenance:write`, `incidents:write`, `machines:write` |
| Owner | | + `config:write`, `accounts:write` |

An agent may use a scope when its preset gives it and its owner's role does too, checked on every request: demoting or disabling the owner limits the agent at once. An OAuth sign-in is held, besides, to the preset and machines the person allowed it. A refusal (`auth.scope`) says what the agent's preset within its owner's role allows.

`GET /api/v1/token` and `POST /api/v1/token/renew` take any scope: the token itself is the credential.

## Alerts

| Route | Scope | What |
| --- | --- | --- |
| `GET /api/v1/bridge` | `status:read` | Whether alerts can reach people now (and why not), groups firing by severity, how many are acknowledged, the Feishu API calls this month against the budget |
| `GET /api/v1/alerts` | `status:read` | Alert groups, firing first, newest first; `?state=all` adds those resolved in the last seven days |
| `GET /api/v1/alerts/{id}` | `status:read` | One group: labels, runbook and dashboard, acknowledgement, silence, escalation, notes, the agents woken, and `page`, the signed link to its page for people |
| `POST /api/v1/alerts/{id}/ack` | `alerts:write` | Acknowledge a firing group, with an optional `note`: escalation to phones stops for four hours |
| `GET /api/v1/silences` | `status:read` | Silences active or pending, ending soonest first |
| `POST /api/v1/silences` | `alerts:write` | Silence a firing group: `alert`, `duration` (at most 24 hours) and a required `comment`. Also acknowledges it |
| `DELETE /api/v1/silences/{id}` | `alerts:write` | End a silence now |
| `GET /api/v1/audit` | `status:read` | The latest writes, newest first, with who made them and through what (`limit`, at most 1000) |

## Working an alert

| Route | Scope | What |
| --- | --- | --- |
| `GET /api/v1/alerts/{id}/evidence` | `status:read` | Everything to judge a group in one answer: each location's result and failure reason, related groups, change events, what was done, links ([Agents and alerts](../agents.md#the-evidence)). `before` widens the window, at most `24h` |
| `GET /api/v1/alerts/{id}/timeline` | `status:read` | The group's story in time order, over the evidence's window |
| `POST /api/v1/alerts/{id}/notes` | `alerts:write` | Leave a note, `text` of at most 2000 characters, on a firing or resolved group. It does not acknowledge |
| `GET /api/v1/changes` | `status:read` | Change events, newest first, by `service`, `machine`, `source` and `since` (`2h`, `7d` or a time; the last day by default) |
| `POST /api/v1/probe-now` | `alerts:write` | Have every location that probes a target probe it again now: `alert_id`, `target` (with an optional `module`) or `service`; `wait` seconds, 45 by default, at most 90 |

## Machines and sessions

| Route | Scope | What |
| --- | --- | --- |
| `GET /api/v1/ssh` | `status:read` | The SSH channel's state (paused or not), its host key, and the certificate authority's key that machines trust |
| `POST /api/v1/ssh/pause` | `machines:write` | Close every agent's session on machines and refuse new ones |
| `POST /api/v1/ssh/resume` | `machines:write` | Let agents reach machines again |
| `GET /api/v1/machines` | `status:read` or `machines:ssh` | The machines, whether the token reaches each, connectors' state, and an `ssh_config` block with a `known_hosts` line for the agent's own ssh |
| `POST /api/v1/machines` | `machines:write` | Add a machine, pinning its host key; `address: connector` for one behind NAT, with the key it connects out with. Returns what the machine needs |
| `DELETE /api/v1/machines/{name}` | `machines:write` | Forget a machine; its sessions stay on record |
| `PUT /api/v1/machines/{name}/connector` | `machines:write` | Set or change the key a machine behind NAT connects out with |
| `POST /api/v1/machines/{name}/run` | `machines:ssh` | Run a command in `sh`, as the machine's user, without a terminal: exit code, stdout and stderr |
| `GET /api/v1/machines/{name}/file` | `machines:ssh` | Lines of a text file, numbered, with `offset` and `limit` |
| `PUT /api/v1/machines/{name}/file` | `machines:ssh` | Write a whole file, at most 1 MiB, making its directory if needed |
| `PATCH /api/v1/machines/{name}/file` | `machines:ssh` | Replace an exact string, once or everywhere; refused if the file changed since it was read |
| `GET /api/v1/sessions` | `status:read` or `machines:ssh` | Sessions on machines, newest first: who, where, what, when, the exit status |
| `GET /api/v1/sessions/{id}` | `sessions:read` or `machines:ssh` | A session's transcript: a command's output, or an interactive shell's commands with their output and exit status |
| `GET /api/v1/sessions/{id}/recording` | `sessions:read` or `machines:ssh` | The recording, asciicast v3, to replay with asciinema |

## Configuration

What Beacon monitors, each item in the shape of its file ([Catalogue, targets and modules](../catalog.md)). Reads take `status:read` or `config:write`; changes take `config:write`. `PUT` and `DELETE` take `?note=` (kept with the change and shown in the chat) and `?if_version=` (refused with `config.conflict` when a later version is in force).

| Route | Scope | What |
| --- | --- | --- |
| `GET /api/v1/config` | `status:read` or `config:write` | Everything, with the version in force |
| `GET /api/v1/config/locations` | `status:read` or `config:write` | Probe locations, by id |
| `GET /api/v1/config/locations/{id}` | `status:read` or `config:write` | One location |
| `PUT /api/v1/config/locations/{id}` | `config:write` | Add or replace a location |
| `DELETE /api/v1/config/locations/{id}` | `config:write` | Remove a location; refused while a target set is named after it alone |
| `GET /api/v1/config/heartbeats` | `status:read` or `config:write` | Heartbeats, by name |
| `GET /api/v1/config/heartbeats/{name}` | `status:read` or `config:write` | One heartbeat |
| `PUT /api/v1/config/heartbeats/{name}` | `config:write` | Add or replace a heartbeat |
| `DELETE /api/v1/config/heartbeats/{name}` | `config:write` | Remove a heartbeat |
| `GET /api/v1/config/resources` | `status:read` or `config:write` | Expiring resources, by name |
| `GET /api/v1/config/resources/{name}` | `status:read` or `config:write` | One resource |
| `PUT /api/v1/config/resources/{name}` | `config:write` | Add or replace a resource |
| `DELETE /api/v1/config/resources/{name}` | `config:write` | Remove a resource |
| `GET /api/v1/config/targets` | `status:read` or `config:write` | Probe target sets, by name, with who probes each |
| `GET /api/v1/config/targets/{name}` | `status:read` or `config:write` | One target set's groups |
| `PUT /api/v1/config/targets/{name}` | `config:write` | Add or replace a target set; probes have it within a minute |
| `DELETE /api/v1/config/targets/{name}` | `config:write` | Remove a target set |
| `GET /api/v1/config/modules` | `status:read` or `config:write` | Probe modules, by name |
| `GET /api/v1/config/modules/{name}` | `status:read` or `config:write` | One module |
| `PUT /api/v1/config/modules/{name}` | `config:write` | Add or replace a module; the module file keeps its other modules and their comments |
| `DELETE /api/v1/config/modules/{name}` | `config:write` | Remove a module |
| `GET /api/v1/config/status_pages` | `status:read` or `config:write` | The public status pages, by name ([Status pages](../status.md)) |
| `GET /api/v1/config/status_pages/{name}` | `status:read` or `config:write` | One status page's definition |
| `PUT /api/v1/config/status_pages/{name}` | `config:write` | Add or replace a status page; components whose services no target has are taken, with a warning |
| `DELETE /api/v1/config/status_pages/{name}` | `config:write` | Remove a status page |
| `GET /api/v1/config/history` | `status:read` or `config:write` | Changes, newest first, without their values (`kind`, `key`, `before`, `limit`); the latest 10,000 are kept |
| `GET /api/v1/config/history/{version}` | `status:read` or `config:write` | One change, with every item's value before and after |
| `POST /api/v1/config/history/{version}/revert` | `config:write` | Undo one change as a new change; `config.conflict` when a later change touched one of its items |

## Status pages and maintenance

The public pages themselves need no token ([below](#other-routes-on-port-17001)); these routes give agents the same and let them plan work ([Status pages](../status.md)).

| Route | Scope | What |
| --- | --- | --- |
| `GET /api/v1/status/{name}` | `status:read` | A status page as its public JSON has it: each component's state now and over 90 days, the incidents and their public updates, the maintenance windows; `?lang=` |
| `GET /api/v1/maintenance` | `status:read` | The maintenance windows, the latest to start first, ended ones kept for 91 days |
| `POST /api/v1/maintenance` | `maintenance:write` | Plan a window: `services`, `starts_at` (now when left out), `ends_at` (at most 7 days later), `title`, `description`. The pages announce it, and a silence per service keeps its alerts from paging people |
| `DELETE /api/v1/maintenance/{id}` | `maintenance:write` | End a window now, or call it off before it begins |

## People and SSH keys

People, for an owner's credential, and the caller's own SSH keys for the SSH channel ([People, agents and signing in](../accounts.md)). No agent preset gives `accounts:write`, so an agent manages people only if an owner gave it that scope on the command line; password resets have no MCP tool.

| Route | Scope | What |
| --- | --- | --- |
| `GET /api/v1/users` | `accounts:write` | The people (username, display name, role, last sign-in, whether disabled, their agents; never a password or its hash) and the invitations not yet used |
| `POST /api/v1/users/invitations` | `accounts:write` | Invite someone with a `role` and a `note`: a one-time link, valid for 24 hours, in the answer only |
| `PATCH /api/v1/users/{name}` | `accounts:write` | Change a person's `role`, `display_name` or `feishu_open_id` (a contact field, never used to sign in), or set `disabled`; it takes effect at once on their sessions and agents. Beacon always keeps an enabled owner (`user.last_owner`) |
| `POST /api/v1/users/{name}/reset` | `accounts:write` | A one-time password reset link, valid for 30 minutes, in the answer only |
| `GET /api/v1/ssh-keys` | `machines:ssh` | The caller's own SSH keys; an agent's are its agent's, whichever of its credentials asks, and a key the agent registered itself says through which (`via`: `token:NAME` or `grant:ID`) |
| `POST /api/v1/ssh-keys` | `machines:ssh` | Register a public key: `key` (an `authorized_keys` line) and `comment`. An agent's key belongs to the credential it came with: it works while that token or OAuth sign-in does, and is removed with it. A key belongs to one agent or token, never to a machine's connector (`key.exists`) |
| `DELETE /api/v1/ssh-keys` | `machines:ssh` | Remove one, `?fingerprint=SHA256:…`; connections signed in with it are closed |

## The token

| Route | Scope | What |
| --- | --- | --- |
| `GET /api/v1/token` | any | The caller: who it is (`actor`, `user`, `agent`, `preset`), its credential (`token`, or `oauth` with the `client`), what it may do now, machines, SSH key fingerprints, expiry, lifetime, the last renewal, whether and when it may renew; never the secret |
| `POST /api/v1/token/renew` | any | A new secret, shown once, expiring the token's lifetime from now; the old secret keeps working for ten minutes. At most once every 24 hours; never for an enrolled machine's token, nor for an OAuth access token, which its client refreshes |
| `DELETE /api/v1/tokens/{name}` | `machines:write` | Revoke a token that reaches machines (`machines:ssh`), or any agent's: the pause switch for one agent, whose open sessions on machines made with it are closed at once. The tokens of probes, hosts, heartbeats, the rule engine, change senders and metrics readers are revoked on the host only (`token.not_revocable`) |
| `GET /api/v1/openapi.yaml` | none | The OpenAPI document |

## The gateway

The gateway answers on port 17006, for machines: probes, hosts, heartbeats, the rule engine and senders of change events. [Sources](../sources.md) describes what each sends.

| Route | Scope | What |
| --- | --- | --- |
| `POST /api/v1/import/prometheus` | `probe:write`, `host:write`, `heartbeat:write` | Samples in the Prometheus text format (what `beacon-probe` pushes), and heartbeats |
| `POST /api/v1/write` | `probe:write`, `host:write` | Prometheus remote write |
| `GET /api/v1/targets` | `probe:write` | The targets this probe's location checks |
| `GET /api/v1/modules` | `probe:write` | The probe module file, byte for byte |
| `GET /api/v1/agent` | `probe:write` | The location's settings: its id, probe timeout and retries |
| `GET /api/v1/agent/requests` | `probe:write` | Probing on demand: a long-poll (`?wait=25s`, at most 55 seconds) for targets to probe now |
| `POST /api/v1/agent/requests/{id}` | `probe:write` | What the probe found for a request |
| `POST /api/v2/alerts` | the rules token, or `alerts:send` | Alerts, in Alertmanager's API v2 form |
| `POST /api/v1/changes` | `changes:write` | A change event: a deployment, a configuration change. At most 60 a minute per token |
| `POST /api/v1/changes/flux` | `changes:write` | Flux's notification-controller events, from its `generic` provider |
| `GET /api/v1/query`, `query_range`, `series`, `labels`, `label/{name}/values`, `metadata`, `status/{what}`, `export`, `rules`, `alerts` | `metrics:read` | Queries passed to VictoriaMetrics and vmalert, without the token |

## Other routes on port 17001

| Route | What |
| --- | --- |
| `/mcp` | The MCP server, streamable HTTP, with the same bearer tokens or OAuth access tokens; a `401` names where to sign in (`WWW-Authenticate: Bearer resource_metadata="…"`) ([MCP tools](mcp.md)) |
| `GET /` | Public: the default status page, as `/status` serves it; without status pages, a page that names the deployment, with **Sign in** ([Status pages](../status.md#branding)) |
| `GET /console`, `GET /console/sign-in` and the pages under `/console` | The console, for people signed in with their account, or for one more release with a token ([The console](../pages.md)) |
| `GET /.well-known/oauth-protected-resource/mcp`, `GET /.well-known/oauth-protected-resource`, `GET /.well-known/oauth-authorization-server` | The OAuth metadata MCP clients discover Beacon by (RFC 9728, RFC 8414) |
| `POST /oauth/register`, `GET /oauth/authorize`, `POST /oauth/token`, `POST /oauth/revoke` | Beacon as the authorization server for MCP clients: registration, the consent page, tokens and revocation ([People, agents and signing in](../accounts.md#the-oauth-endpoints)) |
| `GET /console/alerts/{id}`, `GET /console/sessions/{id}` | The pages people open from a card, with a signed link or a token ([The console](../pages.md#signed-links)) |
| `GET /alerts/{id}`, `GET /sessions/{id}`, `GET /sign-in` and the console's other old paths | `308` to the same path under `/console`, with the query; a link signed for the old path gets a signature for the new one, expiring when it did |
| `GET /status`, `GET /status/{name}`, `GET /status/{name}/incidents/{id}` | The public status pages and their incidents, without a token ([Status pages](../status.md)); on a page's own host name, `/` and `/incidents/{id}` |
| `GET /status.json`, `GET /status/{name}.json`, `GET /status/{name}/feed.atom`, `GET /status/{name}/badge.svg`, `GET /status/{name}/components/{component}/badge.svg` | A page's JSON, Atom feed and badges, public and cacheable |
| `GET /robots.txt` | Lets search engines read `/`, `/status` and `/static/`, and keeps them out of `/console`, `/api/`, `/mcp`, `/alerts/` and `/sessions/` |
| `GET /healthz` | Liveness: `ok` |
| `GET /readyz` | Readiness: the Watchdog seen, the Feishu connection up, budget left |
| `GET /metrics` | Beacon's own metrics in the Prometheus format, for direct connections only |
| `POST /hooks/feishu` | Feishu's card callbacks, when `feishu.callback: http` |
| `GET /drill/healthz` | 200, or 503 during a drill (`beacon drill fail 15m`) |

## Error codes

| Code | Status | Meaning |
| --- | --- | --- |
| `auth.required` | 401 | No bearer token was sent |
| `auth.invalid` | 401 | The token is unknown or has expired |
| `auth.scope` | 403 | The credential lacks the scope the route needs; the message names it, and for an agent says what its preset within its owner's role allows |
| `auth.too_many_failures` | 429 | Too many failed attempts from this address; try again later |
| `bad_input` | 400, 415 | The request is not what the route takes: not JSON, an unknown field, a value out of range. The message says which |
| `upstream` | 502, 503 | Something Beacon relies on did not answer (VictoriaMetrics, the store), or this Beacon does not take the request |
| `alert.not_found` | 404 | No alert group with this id |
| `alert.not_firing` | 409 | The group is not firing, so it cannot be acknowledged or silenced |
| `ssh.disabled` | 503 | The SSH channel is not configured |
| `ssh.paused` | 409 | Agents' access to machines is paused |
| `machine.not_found` | 404 | No machine with this name |
| `machine.forbidden` | 403 | The token may not reach this machine |
| `machine.unreachable` | 502 | Beacon could not connect to the machine |
| `machine.disconnected` | 502 | The machine is behind NAT and its connector is not connected |
| `machine.exists` | 409 | A machine with this name exists; replace it explicitly |
| `file.not_found` | 404 | No such file |
| `file.not_regular` | 400 | The path is not a regular file |
| `file.binary` | 415 | The file is not text |
| `file.too_large` | 413 | The file or the content is over the limit |
| `file.changed` | 409 | The file changed since it was read; read it again |
| `edit.no_match` | 422 | `old_string` does not occur in the file |
| `edit.ambiguous` | 422 | `old_string` occurs more than once; give more context, or set `replace_all` |
| `session.not_found` | 404 | No session with this id |
| `config.not_found` | 404 | No such configuration item or change |
| `config.invalid` | 422 | The change would not pass the checks; the message says why, and nothing changed |
| `config.conflict` | 409 | A later version is in force, or a later change touched the same item; read again and decide afresh |
| `status.not_found` | 404 | No status page of that name |
| `maintenance.not_found` | 404 | No maintenance window with this id |
| `maintenance.ended` | 409 | The maintenance window has ended, or was called off, already |
| `token.too_soon` | 429 | The token renewed itself less than 24 hours ago, and is still good |
| `token.not_renewable` | 403 | An enrolled machine's token, which `beacon enroll … -rotate` replaces; or an OAuth access token, which its client refreshes |
| `token.not_found` | 404 | No token of that name to revoke |
| `token.not_revocable` | 403 | The token neither reaches machines nor is an agent's; machines' and services' tokens are revoked with `beacon token revoke` on the host |
| `user.not_found` | 404 | No person of that name |
| `user.last_owner` | 409 | The change would leave Beacon without an enabled owner; make someone else owner first |
| `key.not_found` | 404 | The caller has no SSH key with that fingerprint |
| `key.exists` | 409 | The key belongs to another agent or token, or is a machine's connector key |
| `probe.busy` | 429 | The target was probed on demand less than 15 seconds ago, or the token has three requests in flight |
| `probe.not_found` | 404 | No active location probes this target |
| `probe.unavailable` | 503 | This Beacon has no gateway to probe through |
| `location.inactive` | 403 | Gateway: the probe's location is not in the catalogue, or its machine has expired |
| `changes.too_many` | 429 | Gateway: the token posted more than 60 change events this minute |
| `request.not_found` | 404 | Gateway: the probing request is unknown or has expired |
| `not_found` | 404 | Gateway: no such route |
