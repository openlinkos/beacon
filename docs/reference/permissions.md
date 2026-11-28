# Roles, presets and scopes

What people, their agents and machines' tokens may do in Beacon. Whoever acts is one of three things:

- **A person**, signed in with an account and carrying a **role**.
- **An agent**, owned by a person and carrying a **preset**.
- **A token of its own**, carrying scopes by name. It is a machine's (a probe, a host, a heartbeat, the rule engine, a change sender, a metrics reader), or a legacy token from before accounts.

What each may do is resolved afresh on every request, so demoting or disabling a person limits their agents at once. A person does not type a list of scopes. An agent's come from its preset, or from a custom list an owner set. Only machines' tokens and experts' are made with scopes by name.

The workflows are in [Invite your team](../guide/people.md). Every route and the scope it needs is in the [REST API](api.md) and [MCP tools](mcp.md) references.

## Roles

A person's role, weakest first. Each may do what the one before it may, and more:

| Role | Scopes | What it is for |
| --- | --- | --- |
| `viewer` | `status:read`, `sessions:read` | Seeing what happens: alerts, evidence, sessions, the console |
| `oncall` | + `alerts:write`, `machines:ssh` | Working an alert: acknowledging, silencing, notes, and reaching machines through the SSH channel |
| `operator` | + `maintenance:write`, `incidents:write`, `machines:write` | Changing what is run: maintenance windows, machines and their keys |
| `owner` | + `config:write`, `accounts:write`, `secrets:use`, `alerts:report` | Deployment configuration, people, team credentials and delegation of execution reporting |

## Presets

An agent never does more than its owner's role allows. It may use a scope when its **preset**, or its custom list, gives it *and* its owner's role does too. A caller never gives an agent a scope or a machine it does not hold itself.

Owner accounts, standalone administration tokens and the local command line administer accounts. An Agent identity cannot use `accounts:write` for account or Agent administration, including when an older custom scope contains it.

A standalone token is held to the roles its own scopes cover. It invites someone, makes a person's password reset link or changes a person (role, disabling, display name, contact) only when it holds every scope of that person's role, and gives a role only when it holds every scope of the role. A token with `accounts:write` alone invites, resets and changes nobody; one with an on-call person's scopes and `accounts:write` reaches viewers and on-call people. Only a token with every scope of an owner reaches owners. The local command line is not limited, and an owner account holds every scope already. Withdrawing a pending link only takes access away, so any credential that administers accounts may.

An invitation or a reset link is checked again when it is opened and when it is used: it works only while whoever made it could still make it under these rules.

| Preset | Scopes it asks for | Whose agents get it |
| --- | --- | --- |
| `readonly` | `status:read`, `sessions:read` | the strongest a viewer's agents may have |
| `oncall` | + `alerts:write`, `machines:ssh` | the strongest an on-call person's agents may have |
| `operator` | + `maintenance:write`, `incidents:write`, `machines:write` | the strongest an operator's or an owner's agents may have |
| `custom` | A list of its own from the scopes of people and agents, at least one | chosen on purpose: in the console by an owner who is a person, through the API and MCP by a person or a legacy token with `accounts:write` (never an agent), with `beacon agent add -scopes`, or a claimed legacy token whose scopes no preset matches exactly. Never beyond the owner's role or what the caller may do. Claiming never widens what a token may do |

A person's agents get the strongest preset their owner's role allows, unless they choose less (`beacon agent add -preset`, or the console). When an owner is demoted, their agents are held to the new role at once, on the next request.

An OAuth sign-in (Claude Code, Codex) is also held to the preset and the machines the person allowed at the consent page. It can ask for less than the agent may. An SSH key an agent added through an OAuth sign-in carries that sign-in's ceiling too.

## Scopes

People's and agents' scopes cover the REST API, MCP and the SSH channel. Machine scopes cover the metrics gateway ([Sources](sources.md)).

| Scope | Who holds it | What it allows |
| --- | --- | --- |
| `status:read` | every role, every preset | Reading: alerts, evidence and timeline, changes, silences, audit, machines and sessions, configuration, status pages |
| `sessions:read` | every role, every preset | Sessions' commands, transcripts and recordings, for the sessions the credential may see. `machines:ssh` shows the same, so `sessions:read` adds nothing to a credential that has `machines:ssh`. Both are held to the machines the credential reaches |
| `alerts:write` | oncall and above | Acknowledging, silencing, notes, probing on demand |
| `machines:ssh` | oncall and above | The SSH channel and its tools, on the machines the credential reaches |
| `maintenance:write` | operator and above | Planning and ending maintenance windows |
| `incidents:write` | operator and above | Reserved for incidents written by hand; no route takes it yet |
| `machines:write` | operator and above | Adding, changing and removing machines and connector keys, with access to all machines; deleting tokens within the caller's own access; pausing agents' access |
| `config:write` | owner | Changing what Beacon monitors: the catalogue, target sets, modules, status pages |
| `accounts:write` | owner account or standalone administration token | Managing people, within the roles the credential's own scopes cover, and everyone's agents |
| `secrets:use` | owner; custom agents only | Listing and using the team credentials explicitly granted to that agent |
| `alerts:report` | standalone token or custom agent of an owner | Reporting execution for the deliveries assigned to this reporter in `agents[].reporter` |
| `probe:write` | a machine's token, from `beacon enroll probe <location>` | Pushing results, fetching targets and settings; needs a subject, writes only as its own location |
| `host:write` | a machine's token, from `beacon enroll host <name>` | Pushing host metrics; needs a subject, writes only as its own host |
| `heartbeat:write` | a machine's token, from `beacon enroll heartbeat <name>` | Pushing one sample per success; needs a subject, writes only its own heartbeat |
| `metrics:read` | a machine's or an expert's token | Metric queries through the gateway |
| `alerts:send` | a machine's or an expert's token | Posting alerts of its own, as the rule engine does, labelled with the token's name |
| `changes:write` | a machine's or an expert's token | Posting change events |

A token of its own carries its scopes by name, fixed when it was made. An agent's credentials follow the agent instead. Whichever of its tokens or OAuth sign-ins it presents, it may do what the agent asks for, its preset or its custom list, and what its owner's role allows now.

`GET /api/v1/token` and `POST /api/v1/token/renew` take any scope: the token itself is the credential.

## Machines

`probe:write`, `host:write` and `heartbeat:write` need a **subject**: the location, host or heartbeat the token writes as. The gateway labels everything they write with it, and with `job` set to the kind of token (`probe`, `host`, `heartbeat`), in place of anything the sender says about itself. So a probe cannot speak for another, a host cannot pass for a probe, and a heartbeat token writes only its own heartbeat. A token whose machine has expired (a `vps` in the catalogue past its date) can no longer write.

### Machine access {#machine-access}

Every token of its own, agent and OAuth sign-in says which machines it reaches on the SSH channel. The choice is always explicit:

| Machine access | Reaches |
| --- | --- |
| `all` | Every machine that is not critical, including machines added later, and the critical machines it names |
| `named` | Only the machines it names |

Nothing else reaches a machine. A credential given no machine access, an empty list or a value Beacon does not know reaches none. `beacon token create` and `beacon agent add` make a credential with no machines unless `-machines` names them or says `all`.

A machine marked **critical** is reached only by a credential that names it: `all` alone never reaches one. A critical machine named for an agent's credential (an agent, a token of its own or an OAuth sign-in) is reached for 30 days unless whoever names it gives another end. The command line takes `name@72h`, `name@2026-12-31` or `name@never`. A person's own named machines do not end unless an end is given. Access stops at the end on the next request, and within a minute Beacon removes the name, records it in the audit log (`machine_access_end`) and closes what was open on it.

When a machine becomes critical (it is marked, or added or registered again as critical), the tokens, agents and OAuth sign-ins that named it before keep it for 30 days from then at the latest; a shorter end stays. Each change is in the audit log (`machine_access_critical`), and the alert chat lists the credentials that keep it. An owner names it again, with another end, for those that should keep it longer. People's own accounts are not changed, and what credentials with `all` had open on it closes.

People reach machines by their account. An owner reaches every machine, critical ones included. Anyone else reaches every machine that is not critical, and the critical machines an owner named on their account (`beacon user machines`). An agent never reaches more than its owner, and an OAuth sign-in never more than its agent. What each reaches is what every layer allows, read afresh on every request.

Who may give machine access:

- Only a person, within their role, or `beacon` on the host gives `all`. A token or an agent names machines, and only machines it reaches itself.
- Only an owner or `beacon` on the host names a critical machine, for an agent, a token, an OAuth sign-in or a person. On the consent page critical machines are offered to owners alone, apart from the others and unticked, and ticking one takes a confirmation.
- A change that gives nothing new (fewer machines, or an earlier end) is not held to the caller.

### Changing machines {#changing-machines}

Using a machine takes `machines:ssh` and access to it. Changing a machine takes `machines:write` and access to all machines, so a credential that names its machines changes none of them, its own included. This applies to adding a machine, adding it again with `replace`, editing it, restoring earlier settings, removing it and setting its connector key.

Marking or unmarking a machine critical, and any change to a critical machine, takes an owner or `beacon machine` on the host. The account Beacon signs in to a machine with is changed only by a person or `beacon` on the host, and `root` only by `beacon` on the host. A new machine reached as an account other than `beacon` takes a person or `beacon` on the host too, so removing a machine and adding it again changes nothing a change could not. The machine itself trusts Beacon's certificate authority for that one account ([Add a machine](../guide/machines.md#add-a-machine)), so a change of the account in Beacon also needs that line run again on the machine.

One machine has one record. A machine is not added, or added again with `replace`, at another machine's address or with another machine's host key, whoever asks (`machine.alias`). Addresses are compared with the host in lower case and IP addresses in their usual form. A connector machine whose connection forwards another machine's SSH server pins nothing, and the refusal is in the audit log (`machine_host_key_refused`). A host key given for a connector machine by an agent or a token is an Ed25519 key, the kind Beacon pins itself. A machine at a loopback, unspecified or link-local address, or at `localhost`, is added only by `beacon machine` on the host, whether the address is given or a name leads there: Beacon's own host holds its certificate authority.

Adding a machine takes `machines:write` and access to all machines, not an owner: agents that provision machines may enrol them. A machine is reached only once it trusts Beacon's certificate authority, which takes root on it. When you retire a machine, remove that trust from it as well as its record in Beacon.

A target set's response machine (the `machine` of a group) is set, cleared or moved only with `machines:write`, access to all machines and SSH access to the machine before and after, whichever route changes it: the dedicated route, a put of the whole target set, a delete or a revert. A designation is the machine with the group it answers for: its file, targets and labels. A change that keeps the machine in its place but gives its group another service's labels moves it. Moving an unchanged group within its file moves nothing.

### Deleting tokens {#deleting-tokens}

`DELETE /api/v1/tokens/{name}`, `revoke_token` and the console's **Tokens** page delete a token that reaches machines, or an agent's token. They need `machines:write` and access to all machines. A token of its own is deleted only by a caller that holds every scope it holds and reaches every machine it reaches. An agent's token is deleted by the agent itself, a person, or an administrator who manages agents; never by another agent or token. Deleting ends that token at once; the agent keeps its other credentials. An agent's owner deletes its tokens on the agent's page without `machines:write`.

Credentials from before machine access was explicit keep what they reached. On its first start an upgraded Beacon gives each token, agent and OAuth sign-in that named no machines `all`, with every machine critical then named. A list that named machines stays named. Each person who is not an owner gets the critical machines of then named on their account. Each change is in the audit log (`machine_access_migrate`).

Tokens are `lbt_` and 52 more characters, and Beacon keeps only their SHA-256. A token expires between 1 hour and 2 years from its making. A revoked token stops working at once.

Its holder renews it before it expires, at most once a day. Renewing gives a new secret with the same name, scopes, subject, machine access and keys, and the old secret works for ten minutes more. An enrolled machine's token is replaced with `beacon enroll -rotate` instead.
