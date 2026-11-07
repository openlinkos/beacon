# Roles, presets and scopes

What people, their agents and machines' tokens may do in Beacon. Whoever acts is one of three things:

- **A person**, signed in with an account and carrying a **role**.
- **An agent**, owned by a person and carrying a **preset**.
- **A token of its own**, carrying scopes by name. It is a machine's (a probe, a host, a heartbeat, the rule engine, a change sender, a metrics reader), or a legacy token from before accounts.

What each may do is resolved afresh on every request, so demoting or disabling a person limits their agents at once. Nobody types a list of scopes for a person or an agent. Only machines' tokens and experts' are made with scopes by name.

The workflows are in [Invite your team](../guide/people.md). Every route and the scope it needs is in the [REST API](api.md) and [MCP tools](mcp.md) references.

## Roles

A person's role, weakest first. Each may do what the one before it may, and more:

| Role | Scopes | What it is for |
| --- | --- | --- |
| `viewer` | `status:read`, `sessions:read` | Seeing what happens: alerts, evidence, sessions, the console |
| `oncall` | + `alerts:write`, `machines:ssh` | Working an alert: acknowledging, silencing, notes, and reaching machines through the SSH channel |
| `operator` | + `maintenance:write`, `incidents:write`, `machines:write` | Changing what is run: maintenance windows, machines and their keys |
| `owner` | + `config:write`, `accounts:write` | The deployment itself: what is monitored, and the people on it |

## Presets

An agent never does more than its owner's role allows. It may use a scope when its **preset** gives it *and* its owner's role does too.

No preset gives `accounts:write`. An agent manages people only when an owner gave it that scope on the command line, as a custom agent.

| Preset | Scopes it asks for | Whose agents get it |
| --- | --- | --- |
| `readonly` | `status:read`, `sessions:read` | the strongest a viewer's agents may have |
| `oncall` | + `alerts:write`, `machines:ssh` | the strongest an on-call person's agents may have |
| `operator` | + `maintenance:write`, `incidents:write`, `machines:write` | the strongest an operator's or an owner's agents may have |
| `custom` | A list of its own, from the nine scopes of people and agents, at least one | chosen on purpose: `beacon agent add -scopes`, or a claimed legacy token whose scopes no preset matches exactly. Claiming never widens what a token may do |

A person's agents get the strongest preset their owner's role allows, unless they choose less (`beacon agent add -preset`, or the console). When an owner is demoted, their agents are held to the new role at once, on the next request.

An OAuth sign-in (Claude Code, Codex) is also held to the preset and the machines the person allowed at the consent page. It can ask for less than the agent may. An SSH key an agent added through an OAuth sign-in carries that sign-in's ceiling too.

## Scopes

The first nine are for people and agents, on the REST API, MCP and the SSH channel. The rest are for machines, on the metrics gateway ([Sources](sources.md)).

| Scope | Who holds it | What it allows |
| --- | --- | --- |
| `status:read` | every role, every preset | Reading: alerts, evidence and timeline, changes, silences, audit, machines and sessions, configuration, status pages |
| `sessions:read` | every role, every preset | Sessions' transcripts and recordings |
| `alerts:write` | oncall and above | Acknowledging, silencing, notes, probing on demand |
| `machines:ssh` | oncall and above | The SSH channel and its tools, on the machines the credential reaches |
| `maintenance:write` | operator and above | Planning and ending maintenance windows |
| `incidents:write` | operator and above | Reserved for incidents written by hand; no route takes it yet |
| `machines:write` | operator and above | Adding and removing machines, connector keys, pausing agents' access |
| `config:write` | owner | Changing what Beacon monitors: the catalogue, target sets, modules, status pages |
| `accounts:write` | owner; no preset | Managing people and everyone's agents; an owner's, by hand only |
| `probe:write` | a machine's token, from `beacon enroll probe <location>` | Pushing results, fetching targets and settings; needs a subject, writes only as its own location |
| `host:write` | a machine's token, from `beacon enroll host <name>` | Pushing host metrics; needs a subject, writes only as its own host |
| `heartbeat:write` | a machine's token, from `beacon enroll heartbeat <name>` | Pushing one sample per success |
| `metrics:read` | a machine's or an expert's token | Metric queries through the gateway |
| `alerts:send` | a machine's or an expert's token | Posting alerts as the rule engine does |
| `changes:write` | a machine's or an expert's token | Posting change events |

A token of its own carries its scopes by name, fixed when it was made. An agent's credentials follow the agent instead. Whichever of its tokens or OAuth sign-ins it presents, it may do what the agent's preset and its owner's role both allow now.

`GET /api/v1/token` and `POST /api/v1/token/renew` take any scope: the token itself is the credential.

## Machines

`probe:write` and `host:write` need a **subject**: the location or host the token writes as. The gateway labels everything they write with it, in place of anything the sender says about itself. So a probe cannot speak for another. A token whose machine has expired (a `vps` in the catalogue past its date) can no longer write.

The machines an agent or a token may reach on the SSH channel are a list of their own. An empty list means all of them. An OAuth sign-in's list is intersected with the agent's, so it reaches only what both allow.

Tokens are `lbt_` and 52 more characters, and Beacon keeps only their SHA-256. A token expires between 1 hour and 2 years from its making. A revoked token stops working at once.

Its holder renews it before it expires, at most once a day. Renewing gives a new secret with the same name, scopes, subject, machines and keys, and the old secret works for ten minutes more. An enrolled machine's token is replaced with `beacon enroll -rotate` instead.
