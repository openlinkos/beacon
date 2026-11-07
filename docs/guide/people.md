# Invite your team

By the end of this page everyone on your team signs in with their own account and role. Each person's agents act for that person, and never do more than that person's role allows.

Everything is named after a person. The audit log, the chat and the sessions on machines say:

- `alice` for what Alice did on the console;
- `alice / claude-code` for what Alice's agent did.

People sign in only with Beacon's own accounts. There is no sign-in through GitHub, Feishu, OIDC, LDAP or anything else.

You need the first account, the owner you made in [On a server](../self-hosting/install.md), step 4. This page goes on from there. The commands run on the centre, as `docker exec beacon beacon …` in the reference deployment.

## Roles {#roles}

Each role may do what the one above it may, and more:

| Role | May | Adds |
| --- | --- | --- |
| Viewer | See everything: alerts, machines, sessions and their replays, the configuration, the audit log | `status:read`, `sessions:read` |
| On call | Also act on alerts; their agents reach machines through the SSH channel | + `alerts:write`, `machines:ssh` |
| Operator | Also add and remove machines, pause agents, schedule maintenance | + `maintenance:write`, `incidents:write`, `machines:write` |
| Owner | Also change the configuration, invite people, set roles, manage every agent | + `config:write`, `accounts:write` |

The console, the command line and the API speak of roles. The scopes stay underneath, as the [REST API](../reference/api.md#scopes) lists them. A role is read again on every request, so a change takes effect at once, for the person and for their agents. Beacon always keeps at least one enabled owner, and refuses the change that would leave it without one.

## The first owner {#the-first-owner}

1. **Find the setup link.** A Beacon without accounts writes a one-time setup link to its log when it starts: `docker compose logs beacon | grep setup_link` ([On a server](../self-hosting/install.md), step 4, shows it). Once it has expired, print another on the centre, while Beacon still has no account:

   ```bash
   docker exec beacon beacon setup
   ```

2. **Open the link.** It opens **Set Beacon up**. Choose a username, a display name and a password. That account is an owner, and you are signed in.

A link works once, and none works once Beacon has an account. From then on, people join by invitation. Until the first account exists, the sign-in page offers only a token, and says where to find the setup link.

## Invite people {#invite-people}

1. **Invite each person with the role they need.** On the console's **People** page, use **Invite someone** with the role and a note saying who it is for. Or on the centre:

   ```bash
   docker exec beacon beacon user invite -role oncall -note "Bob, on call from Monday"
   ```

   You will see:

   ```
   An invitation to join as oncall, until 2026-11-03 09:00:00. Send the link to that person only; it works once:
   https://status.example.com/console/join?code=…
   ```

2. **Send the link to that person only.** It is shown only then, and works once, for 24 hours. The person opens it, chooses a username and a password, and is signed in with the role the invitation gives.

**People** lists the invitations not yet used, and **Withdraw** ends one. The chat hears that someone was invited and that someone joined, never the link.

A username:

- is 2 to 32 lower-case letters, digits, dots, dashes and underscores, starting with a letter or a digit;
- names the person in the audit log, so it never changes;
- cannot be `beacon`, `root`, `rules` or `unknown`, which are reserved.

The display name is only how the console shows the person.

## Passwords {#passwords}

A password has at least 10 characters, is not the username, and is not one of the commonest passwords. There are no other rules. Beacon keeps only its argon2id hash, compared in constant time.

- **To change your own**, open **Your account** in the menu at the foot of the console's sidebar. Give the current password with the new one. Your other sign-ins end.
- **A forgotten password** is reset by an owner, with **Password reset link** on the **People** page, or on the centre with `beacon user reset bob`. That prints a link that is shown once, works once and lasts 30 minutes. With it the person chooses a new password, and every sign-in of theirs ends.

## Change a role, disable someone {#change-a-role-disable-someone}

On the **People** page, or on the centre:

```bash
docker exec beacon beacon user role bob operator   # bob is operator now; their sessions and agents follow at once
docker exec beacon beacon user disable bob         # bob is disabled
docker exec beacon beacon user enable bob          # bob is enabled
```

A lower role limits the person's sign-ins and their agents at once. Disabling someone ends their sign-ins, stops their agents and closes what those agents have open on machines. Nobody disables themself on the console.

`beacon user list` shows everyone, with their role, status, last sign-in and agents. The invitations not yet used follow.

## Agents {#agents}

An agent is a program that acts for one person, its owner. It may be Claude Code on someone's workstation, the agent `beacon-oncall` starts when a hook wakes it, or a CI job. An agent has:

- a **name**: 2 to 40 lower-case letters, digits, dots, dashes and underscores, unique in Beacon. The audit log names it `owner / name`.
- a **preset** (below);
- the **machines** it may reach through the SSH channel: all of them, or a list;
- its **SSH keys**;
- its **credentials**: tokens (`lbt_…`) for headless agents and CI, OAuth sign-ins for interactive clients.

| Preset | Asks for |
| --- | --- |
| Read only | A viewer's scopes |
| On call | An on-call person's scopes: also working alerts and reaching machines |
| Operator | An operator's scopes |

An agent may do what its preset asks for **and** its owner's role allows, worked out again on every request. Demoting or disabling the person limits or stops their agents at once, on the tokens they already hold.

- No preset gives `config:write` or `accounts:write`.
- A preset above the owner's role is refused when the agent is made or changed.
- A **custom** agent lists scopes of its own instead, still within the owner's role (`beacon agent add … -scopes status:read,config:write`).

The console's **Agents** page lists your agents; an owner sees everyone's. Each agent's page:

- shows what it may do now;
- changes its preset and machines;
- lists its OAuth sign-ins, tokens and keys, and revokes or removes any of them;
- deletes the agent with all its credentials.

People make and manage their own agents. An owner manages anyone's.

## Connect Claude Code {#connect-claude-code}

Claude Code signs in through the browser, with MCP's OAuth. It reaches machines with its own ssh, scp and rsync as well as Beacon's tools: see [Claude Code](../integrations/claude-code.md). Codex signs in the same way: [Codex](../integrations/codex.md).

## What a sign-in may do {#what-a-sign-in-may-do}

Each OAuth sign-in keeps what you allowed as a ceiling of its own. The client may do what its sign-in's preset and machines allow, within what the agent may do, within your role. This is read afresh on every request.

Allowing a client never changes an existing agent. So it never widens another client's sign-in, the agent's tokens or its keys. To give an agent you reuse more, change the agent on its page first, then sign in again from the client.

The agent's page lists each sign-in under **OAuth sign-ins**: the client, what it was allowed, when it was last used. **Revoke** ends it.

## Headless agents and CI {#headless-agents-and-ci}

A program that runs alone gets a token, made on the console's **Agents** page or with `beacon agent add`. It renews the token by itself. `beacon-oncall` is the reference harness for the agent a hook wakes. See [Headless agents and CI](../integrations/headless.md).

## SSH keys {#ssh-keys}

The SSH channel signs an agent in by its public key. An agent registers its own key through MCP or the API, with the credential it already has, so nobody copies a key anywhere. The tools all need `machines:ssh`:

- `add_ssh_key` (`POST /api/v1/ssh-keys`);
- `list_ssh_keys`;
- `remove_ssh_key`.

Ask Claude Code *"Register my SSH key ~/.ssh/id_ed25519.pub with Beacon"* and it calls `add_ssh_key`. `list_machines` then gives the `ssh_config` block for its own ssh ([Claude Code](../integrations/claude-code.md#connect-with-your-own-ssh-scp-and-rsync)).

Who a key belongs to:

- **A key the agent registers itself** belongs to the credential it came with (`via`: `token:NAME` or `grant:ID`). It signs in only while that credential works. It goes when the credential is revoked or the agent is deleted.
- **A key the owner adds** stays with the agent. The owner adds it with **Add an SSH key** on the agent's page, or `-ssh-key` on `beacon agent add`.

An agent holds at most 20 keys, and a key reaches only the machines its holder may reach. The chat hears of every key added or removed.

## Stop an agent {#stop-an-agent}

A revoked or narrowed credential is refused from its next request on. What the agent has open on machines is closed at once. Every switch that does this is in [Stop an agent, pause everything](stop.md), with the one that stops every agent at once:

- revoking a token or a sign-in;
- deleting the agent;
- narrowing its machines or preset;
- disabling or demoting its owner.

## Who the audit log names {#who-the-audit-log-names}

The audit log, the chat's echoes and the sessions on machines name each actor this way:

| Who acted | Named |
| --- | --- |
| A person | `alice` |
| An agent of theirs, whatever credential it used | `alice / claude-code` |
| A legacy or machine token | By its name |
| The command line on the centre | `cli:` and the operating system's user |
| Beacon itself | `beacon` |

Entries made before keep the names they had. The full table is in the permissions reference ([Roles, presets and scopes](../reference/permissions.md)).

## Moving from tokens {#moving-from-tokens}

On a Beacon that ran before accounts, every token keeps working as it did, with its own scopes. The audit log calls it by its name. Nothing breaks if nobody does anything. To bring people and agents in:

1. **Start this version.** With no account yet, it writes the setup link to its log, and how many legacy tokens it found (`legacy_tokens`). Open the link and make your owner account. If the link has expired, `beacon setup` prints a new one, but only while Beacon has no account.
2. **Invite the people** (**People**, or `beacon user invite`), each with their role.
3. **Claim the tokens agents use** as agents of their people ([below](#claiming-a-token)), or leave them as they are.
4. **Revoke the tokens people signed in to the console with**, once those people have accounts.
5. **Move interactive agents to OAuth** when convenient: `claude mcp remove beacon`, then add it again without a header and sign in.

### Claiming a token {#claiming-a-token}

The console's **Tokens** page marks a token **Legacy** when it carries only people's scopes and belongs to no agent. An owner claims it as an agent of a person, with **Claim** on that page, or on the centre:

```bash
docker exec beacon beacon token claim claude-code -owner alice
```

```
token claude-code is agent alice / claude-code's now (oncall, may do status:read,sessions:read,alerts:write,machines:ssh); it keeps its secret and expiry
```

The token keeps its secret and its expiry, so whatever uses it goes on working. Its machines and SSH keys move to the agent.

The preset is the one with exactly the token's scopes, or custom when none matches. So a claim never widens what a token may do.

- `-preset` chooses another preset.
- `-agent` names an existing agent of the same person to add the token to. It is refused when that agent may do more than the token may now.

Machines' tokens are never legacy and never claimed: those of probes, hosts, heartbeats, the rule engine, change senders and metrics readers.

## The command line {#the-command-line}

The commands for people and agents:

- `beacon user list`
- `beacon user invite -role ROLE [-note TEXT]`
- `beacon user role NAME ROLE`
- `beacon user disable` and `beacon user enable NAME`
- `beacon user reset NAME`
- `beacon agent add NAME -owner USER [-preset P] [-machines a,b] [-expires 365d] [-ssh-key KEY]`
- `beacon agent list`
- `beacon agent remove NAME`
- `beacon token claim NAME -owner USER`

`ROLE` is `viewer`, `oncall`, `operator` or `owner`. A preset is `readonly`, `oncall` or `operator`. Every subcommand and flag is in the command-line reference: [Command line](../reference/cli.md).

The same operations are on the REST API under `/api/v1/users` ([REST API](../reference/api.md#people-and-ssh-keys)), with `accounts:write`, which no preset gives. Changing roles, disabling people and password resets are not offered through MCP.

## Security {#security}

How the accounts, the tokens and the OAuth sign-ins are kept is in the [security model](../self-hosting/security.md). It covers argon2id and SHA-256, the one-time links, and how what an agent may do is decided on every request.

### The OAuth endpoints {#the-oauth-endpoints}

The OAuth routes an MCP client uses are listed in the [REST API](../reference/api.md#other-routes-on-port-17001): the metadata documents, registration, the consent page, and the token and revocation endpoints.

## Check it

1. **Have the person you invited sign in.** `beacon user list` shows them, their role and their last sign-in. The audit log names them (`alice`) on whatever they do.
2. **Make one of their agents and ask it for `GET /api/v1/token`.** The answer names `alice / name`, its preset and what it may do now.
3. **Demote the person to viewer and ask again.** The agent may do less at once, on the token it already holds.

## Next

- [Put an agent on call](on-call.md): what those agents do when a service breaks.
- [Review what agents did](console.md): the audit log, the sessions and the replays.
- [Stop an agent, pause everything](stop.md): the brakes, and what each closes.
