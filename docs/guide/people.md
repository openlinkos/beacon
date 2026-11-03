# Invite your team

By the end of this page everyone on your team signs in with an account of their own and a role that says what they may do, and each person's agents act for that person and never do more than that person's role allows. Everything is named after a person: the audit log, the chat and the sessions on machines say `alice` for what Alice did on the console and `alice / claude-code` for what her agent did. People sign in only with Beacon's own accounts: there is no sign-in through GitHub, Feishu, OIDC, LDAP or anything else. You made the first account, an owner, in [Up in 30 minutes](../self-hosting/install.md), step 4; this page goes on from there. You need that owner account; the commands run on the centre, as `docker exec beacon beacon …` in the reference deployment.

## Roles {#roles}

Each role may do what the one above it may, and more:

| Role | May | Adds |
| --- | --- | --- |
| Viewer | See everything: alerts, machines, sessions and their replays, the configuration, the audit log | `status:read`, `sessions:read` |
| On call | Also act on alerts; their agents reach machines through the SSH channel | + `alerts:write`, `machines:ssh` |
| Operator | Also add and remove machines, pause agents, schedule maintenance | + `maintenance:write`, `incidents:write`, `machines:write` |
| Owner | Also change the configuration, invite people, set roles, manage every agent | + `config:write`, `accounts:write` |

The console, the command line and the API speak of roles; the scopes stay underneath, as the [REST API](../reference/api.md#scopes) lists them. A role is read again on every request, so a change takes effect at once, for the person and for their agents. Beacon always keeps at least one enabled owner, and refuses the change that would leave it without one.

## The first owner {#the-first-owner}

A Beacon without accounts writes a one-time setup link to its log when it starts (`docker compose logs beacon | grep setup_link`; [Up in 30 minutes](../self-hosting/install.md), step 4, shows it). Once it has expired, print another on the centre, while Beacon still has no account:

```bash
docker exec beacon beacon setup
```

The link opens **Set Beacon up**: choose a username, a display name and a password. That account is an owner, and you are signed in. A link works once, and none works once Beacon has an account; from then on, people join by invitation. Until the first account exists, the sign-in page offers only a token, and says where to find the setup link.

## Invite people {#invite-people}

An owner invites each person with the role they need: on the console's **People** page, **Invite someone** with the role and a note saying who it is for; or on the centre:

```bash
docker exec beacon beacon user invite -role oncall -note "Bob, on call from Monday"
```

You will see:

```
An invitation to join as oncall, until 2026-11-03 09:00:00. Send the link to that person only; it works once:
https://status.example.com/console/join?code=…
```

The link is shown only then, and works once, for 24 hours. The person opens it, chooses a username and a password, and is signed in with the role the invitation gives. **People** lists the invitations not yet used, and **Withdraw** ends one. The chat hears that someone was invited and that someone joined, never the link.

A username is 2 to 32 lower-case letters, digits, dots, dashes and underscores, starting with a letter or a digit. It names the person in the audit log, so it never changes; `beacon`, `root`, `rules` and `unknown` are reserved. The display name is only how the console shows the person.

## Passwords {#passwords}

A password has at least 10 characters, is not the username, and is not one of the commonest passwords; there are no other rules. Beacon keeps only its argon2id hash, compared in constant time. **To change your own**, open **Your account** in the menu at the foot of the console's sidebar and give the current password with the new one; your other sign-ins end. **A forgotten password** is reset by an owner, with **Password reset link** on the **People** page, or on the centre with `beacon user reset bob`, which prints a link that is shown once, works once and lasts 30 minutes: with it the person chooses a new password, and every sign-in of theirs ends.

## Change a role, disable someone {#change-a-role-disable-someone}

On the **People** page, or on the centre:

```bash
docker exec beacon beacon user role bob operator   # bob is operator now; their sessions and agents follow at once
docker exec beacon beacon user disable bob         # bob is disabled
docker exec beacon beacon user enable bob          # bob is enabled
```

A lower role limits the person's sign-ins and their agents at once. Disabling someone ends their sign-ins, stops their agents and closes what those agents have open on machines. Nobody disables themself on the console. `beacon user list` shows everyone, with their role, status, last sign-in and agents, then the invitations not yet used.

## Agents {#agents}

An agent is a program that acts for one person, its owner: Claude Code on someone's workstation, the agent `beacon-oncall` starts when a hook wakes it, a CI job. It has a **name** (2 to 40 lower-case letters, digits, dots, dashes and underscores, unique in Beacon; the audit log names it `owner / name`), a **preset**, the **machines** it may reach through the SSH channel (all of them, or a list), its **SSH keys**, and its **credentials**: tokens (`lbt_…`) for headless agents and CI, OAuth sign-ins for interactive clients.

| Preset | Asks for |
| --- | --- |
| Read only | A viewer's scopes |
| On call | An on-call person's scopes: also working alerts and reaching machines |
| Operator | An operator's scopes |

An agent may do what its preset asks for **and** its owner's role allows, worked out again on every request: demoting or disabling the person limits or stops their agents at once, on the tokens they already hold. No preset gives `config:write` or `accounts:write`; a preset above the owner's role is refused when the agent is made or changed. A **custom** agent lists scopes of its own instead, still within the owner's role (`beacon agent add … -scopes status:read,config:write`).

The console's **Agents** page lists your agents, an owner everyone's; each agent's page shows what it may do now, changes its preset and machines, lists its OAuth sign-ins, tokens and keys, revokes or removes any of them, and deletes the agent with all its credentials. People make and manage their own agents; an owner manages anyone's.

## Connect Claude Code {#connect-claude-code}

Claude Code signs in through the browser, with MCP's OAuth, and reaches machines with its own ssh, scp and rsync as well as Beacon's tools: see [Claude Code](../integrations/claude-code.md). Codex signs in the same way: [Codex](../integrations/codex.md).

## What a sign-in may do {#what-a-sign-in-may-do}

Each OAuth sign-in keeps what you allowed as a ceiling of its own: the client may do what its sign-in's preset and machines allow, within what the agent may do, within your role, read afresh on every request. Allowing a client never changes an existing agent, so it never widens another client's sign-in, the agent's tokens or its keys; to give an agent you reuse more, change the agent on its page first, then sign in again from the client. The agent's page lists each sign-in under **OAuth sign-ins** — the client, what it was allowed, when it was last used — and **Revoke** ends it.

## Headless agents and CI {#headless-agents-and-ci}

A program that runs alone gets a token, made on the console's **Agents** page or with `beacon agent add`, and renews it by itself; `beacon-oncall` is the reference harness for the agent a hook wakes: see [Headless agents and CI](../integrations/headless.md).

## SSH keys {#ssh-keys}

The SSH channel signs an agent in by its public key. An agent registers its own key through MCP or the API, with the credential it already has, so nobody copies a key anywhere: `add_ssh_key` (`POST /api/v1/ssh-keys`), `list_ssh_keys`, `remove_ssh_key` — all `machines:ssh`. Ask Claude Code *"Register my SSH key ~/.ssh/id_ed25519.pub with Beacon"* and it calls `add_ssh_key`; `list_machines` then gives the `ssh_config` block for its own ssh ([Claude Code](../integrations/claude-code.md#connect-with-your-own-ssh-scp-and-rsync)).

A key the agent registers itself belongs to the credential it came with (`via`: `token:NAME` or `grant:ID`): it signs in only while that credential works, and it goes when the credential is revoked or the agent is deleted. A key the owner adds, with **Add an SSH key** on the agent's page or `-ssh-key` on `beacon agent add`, stays with the agent. An agent holds at most 20 keys, and a key reaches only the machines its holder may reach; the chat hears of every key added or removed.

## Stop an agent {#stop-an-agent}

A revoked or narrowed credential is refused from its next request on, and what the agent has open on machines is closed at once. Every switch that does this — revoking a token or a sign-in, deleting the agent, narrowing its machines or preset, disabling or demoting its owner — and the one that stops every agent at once: [Stop an agent, pause everything](stop.md).

## Who the audit log names {#who-the-audit-log-names}

The audit log, the chat's echoes and the sessions on machines name a person `alice`, an agent of theirs `alice / claude-code` (whatever credential it used), a legacy or machine token by its name, the command line on the centre `cli:` and the operating system's user, and Beacon itself `beacon`. Entries made before keep the names they had; the full table is in the permissions reference ([Roles, presets and scopes](../reference/permissions.md)).

## Moving from tokens {#moving-from-tokens}

On a Beacon that ran before accounts, every token keeps working as it did, with its own scopes, and the audit log calls it by its name; nothing breaks if nobody does anything. To bring people and agents in:

1. **Start this version.** With no account yet, it writes the setup link to its log, and how many legacy tokens it found (`legacy_tokens`). Open the link and make your owner account. If the link has expired, `beacon setup` prints a new one, and it does so only while Beacon has no account.
2. **Invite the people** (**People**, or `beacon user invite`), each with their role.
3. **Claim the tokens agents use** as agents of their people (below), or leave them as they are.
4. **Revoke the tokens people signed in to the console with**, once those people have accounts.
5. **Move interactive agents to OAuth** when convenient: `claude mcp remove beacon`, then add it again without a header and sign in.

**Claiming a token.** The console's **Tokens** page marks a token **Legacy** when it carries only people's scopes and belongs to no agent. An owner claims it as an agent of a person, with **Claim** on that page, or on the centre:

```bash
docker exec beacon beacon token claim claude-code -owner alice
```

```
token claude-code is agent alice / claude-code's now (oncall, may do status:read,sessions:read,alerts:write,machines:ssh); it keeps its secret and expiry
```

The token keeps its secret and its expiry, so whatever uses it goes on working; its machines and SSH keys move to the agent. The preset is the one with exactly the token's scopes, or custom when none matches, so a claim never widens what a token may do; `-preset` chooses another, and `-agent` names an existing agent of the same person to add the token to (refused when that agent may do more than the token may now). Machines' tokens (probes, hosts, heartbeats, the rule engine, change senders, metrics readers) are never legacy and never claimed.

## The command line {#the-command-line}

`beacon user list`, `beacon user invite -role ROLE [-note TEXT]`, `beacon user role NAME ROLE`, `beacon user disable` and `beacon user enable NAME`, `beacon user reset NAME`, `beacon agent add NAME -owner USER [-preset P] [-machines a,b] [-expires 365d] [-ssh-key KEY]`, `beacon agent list`, `beacon agent remove NAME`, `beacon token claim NAME -owner USER`. `ROLE` is `viewer`, `oncall`, `operator` or `owner`; a preset is `readonly`, `oncall` or `operator`. Every subcommand and flag is in the command-line reference: [Command line](../reference/cli.md). The same operations are on the REST API under `/api/v1/users` ([REST API](../reference/api.md#people-and-ssh-keys)), with `accounts:write`, which no preset gives; changing roles, disabling people and password resets are not offered through MCP.

## Security {#security}

How the accounts, the tokens and the OAuth sign-ins are kept — argon2id and SHA-256, the one-time links, what an agent may do decided on every request — is in the [security model](../self-hosting/security.md).

### The OAuth endpoints {#the-oauth-endpoints}

The OAuth routes an MCP client uses — the metadata documents, registration, the consent page, the token and revocation endpoints — are listed in the [REST API](../reference/api.md#other-routes-on-port-17001).

## Check it

With the person you invited signed in, `beacon user list` shows them, their role and their last sign-in, and the audit log names them (`alice`) on whatever they do. Then make one of their agents and ask it for `GET /api/v1/token`: the answer names `alice / name`, its preset and what it may do now. Demote the person to viewer and ask again: the agent may do less at once, on the token it already holds.

## Next

- [Put an agent on call](on-call.md): what those agents do when a service breaks.
- [Review what agents did](console.md): the audit log, the sessions and the replays.
- [Stop an agent, pause everything](stop.md): the brakes, and what each closes.
