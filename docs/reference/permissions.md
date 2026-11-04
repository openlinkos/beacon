# Roles, presets and scopes

Whoever acts in Beacon is one of three things: a **person**, signed in with an account and carrying a **role**; an **agent**, owned by a person and carrying a **preset**; or a **token of its own**, carrying scopes by name — a machine's (a probe, a host, a heartbeat, the rule engine, a change sender, a metrics reader), or a legacy token from before accounts. What each may do is resolved afresh on every request, so demoting or disabling a person limits their agents at once. Nobody types a list of scopes for a person or an agent; only machines' tokens and experts' are made with scopes by name. The workflows are in [People and their agents](../guide/people.md); every route and the scope it needs is in the [REST API](api.md) and [MCP tools](mcp.md) references.

## Roles

A person's role, weakest first; each may do what the one before it may, and more:

| Role | Scopes | What it is for |
| --- | --- | --- |
| `viewer` | `status:read`, `sessions:read` | Seeing what happens: alerts, evidence, sessions, the console |
| `oncall` | + `alerts:write`, `machines:ssh` | Working an alert: acknowledging, silencing, notes, and reaching machines through the SSH channel |
| `operator` | + `maintenance:write`, `incidents:write`, `machines:write` | Changing what is run: maintenance windows, machines and their keys |
| `owner` | + `config:write`, `accounts:write` | The deployment itself: what is monitored, and the people on it |

## Presets

An agent never does more than its owner's role allows: it may use a scope when its **preset** gives it *and* its owner's role does too. No preset gives `accounts:write` — an agent manages people only when an owner gave it that scope on the command line, as a custom agent.

| Preset | Scopes it asks for | Whose agents get it |
| --- | --- | --- |
| `readonly` | `status:read`, `sessions:read` | the strongest a viewer's agents may have |
| `oncall` | + `alerts:write`, `machines:ssh` | the strongest an on-call person's agents may have |
| `operator` | + `maintenance:write`, `incidents:write`, `machines:write` | the strongest an operator's or an owner's agents may have |
| `custom` | A list of its own, from the nine scopes of people and agents, at least one | chosen on purpose: `beacon agent add -scopes`, or a claimed legacy token whose scopes no preset matches exactly; claiming never widens what a token may do |

A person's agents get the strongest preset their owner's role allows unless they choose less (`beacon agent add -preset`, or the console); a demoted owner's agents are held to the new role at once, on the next request.

An OAuth sign-in (Claude Code, Codex) is held, besides, to the preset and the machines the person allowed at the consent page — it can ask for less than the agent may. An SSH key an agent added through an OAuth sign-in carries that sign-in's ceiling too.

## Scopes

The first nine are for people and agents, on the REST API, MCP and the SSH channel; the five `secrets:` scopes govern the vault ([Secrets for agents](../guide/secrets.md)); the rest are for machines, on the metrics gateway ([Sources](sources.md)).

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
| `secrets:describe` | a secrets preset, or `custom` | Searching within entitlements and seeing summaries ([the secrets](#the-secrets-levels-entitlements-and-actions)) |
| `secrets:read` | the same, one step up | Reading values through `get_secret`, within entitlements and grants |
| `secrets:use` | the same, one step up | Using values without seeing them: `beacon run`, `run_command`, `http_request`, file delivery |
| `secrets:review` | a review agent's own | Deciding requests on the review chain; reviewing is not reading |
| `secrets:admin` | owner; grantable to an agent | Levels, tags, entitlements and [settings](settings.md) |
| `probe:write` | a machine's token, from `beacon enroll probe <location>` | Pushing results, fetching targets and settings; needs a subject, writes only as its own location |
| `host:write` | a machine's token, from `beacon enroll host <name>` | Pushing host metrics; needs a subject, writes only as its own host |
| `heartbeat:write` | a machine's token, from `beacon enroll heartbeat <name>` | Pushing one sample per success |
| `metrics:read` | a machine's or an expert's token | Metric queries through the gateway |
| `alerts:send` | a machine's or an expert's token | Posting alerts as the rule engine does |
| `changes:write` | a machine's or an expert's token | Posting change events |

A token of its own carries its scopes by name, fixed when it was made; an agent's credentials follow the agent instead — whichever of its tokens or OAuth sign-ins it presents, it may do what the agent's preset and its owner's role both allow now. `GET /api/v1/token` and `POST /api/v1/token/renew` take any scope: the token itself is the credential.

## The secrets: levels, entitlements and actions

The vault — collections, items and their grants — is governed by two axes, and the stricter one decides. A scope of the five above lets an agent in the door; what it may then ask for is the second axis. [Secrets for agents](../guide/secrets.md) walks the workflow; [the design](../about/secrets.md) says why.

**The level of an item** — what a request for it takes. Set on a collection, a tag or the item; an item's own level overrides, otherwise the strictest of the collection's default and the item's tags applies:

| Level | A request for the item takes |
| --- | --- |
| `auto` | An entitlement: granted on asking |
| `review` | The review chain — rules, a review agent, people, as [configured](settings.md#review) |
| `person` | A person, always; a review agent cannot approve these |
| `never` | No agent at all; people can still read the item in the console |

**The entitlement of an agent** — what it may even ask for. Default deny: an agent arrives with nothing, and an item outside its entitlements is invisible to it, search included. An entitlement names principals (agents, agent tags, or all of a person's), resources (collections, tag globs, items, optionally fields), the actions below, a minimum `approval`, settings overrides of its own, and its own reviewers and expiry. A `deny` entitlement is checked before every allow; several entitlements matching one request combine to the widest approval with the highest limits; and no entitlement lifts an agent past what its owner may read in that collection. Changing an item's tags needs `manage`, because tags change permissions.

**The actions** — what the holder receives:

| Action | The agent gets | The tool |
| --- | --- | --- |
| `describe` | Names, categories, tags, URLs, field names, the level asked, whether it holds a grant | `search_secrets` |
| `read` | Field values; an `otp` field the current code and its seconds | `get_secret` |
| `use` | Nothing seen: the value injected into a command, a process, a request or a file on a bound machine | `beacon run`, `run_command`, `http_request`, file delivery |

An item, tag or collection may restrict its actions to `use` only; the actions available are the entitlement's and the item's in common.

**People's permissions per collection** — four grades, beside the global roles:

| Permission | What it allows |
| --- | --- |
| `describe` | Seeing an item's summary |
| `read` | Reading values, with a password re-check inside `reveal.reauth_window`; every read is recorded like an agent's use |
| `edit` | Creating, changing, archiving and removing items |
| `manage` | Levels, tags, members and entitlements of the collection, and its settings |

An owner holds `manage` on every collection. An agent never gets more than its owner's grade, and when the owner loses it, the settings say what happens to the grants ([settings](settings.md)).

## Machines

`probe:write` and `host:write` need a **subject** — the location or host the token writes as — and the gateway labels everything they write with it, in place of anything the sender says about itself, so a probe cannot speak for another. A token whose machine has expired (a `vps` in the catalogue past its date) can no longer write. The machines an agent or a token may reach on the SSH channel are a list of their own; an empty list means all of them, and an OAuth sign-in's list is intersected with the agent's, so it reaches only what both allow.

Tokens are `lbt_` and 52 more characters; Beacon keeps only their SHA-256. A token expires between 1 hour and 2 years from its making. Its holder renews it before it does — a new secret, the same name, scopes, subject, machines and keys, the old secret working for ten minutes more — at most once a day; an enrolled machine's token is replaced with `beacon enroll -rotate` instead. A revoked token stops working at once.
