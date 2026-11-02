# People, agents and signing in

Beacon is for a team: people sign in with accounts of Beacon's own, and each person's agents act for that person, with no more than that person may do. The audit log and the chat name both: `alice` for what Alice did on the console, `alice / claude-code` for what her agent did. Beacon is the only place anyone signs in to Beacon: there is no sign-in through GitHub, Feishu, OIDC, LDAP or anything else.

Agents connect the way agents do: an interactive client such as Claude Code or Codex signs in through the browser (MCP's OAuth), and a headless agent or a CI job gets a token with the commands that use it ready to paste. Nobody copies a scope string anywhere: people have roles, agents have presets.

## People and roles

A person has a username (2 to 32 lower-case letters, digits, dots, dashes and underscores, starting with a letter or a digit; it never changes), a display name, a role and a password. Each role can do what the one before it can, and more:

| Role | Adds | Scopes it gives |
| --- | --- | --- |
| Viewer | Sees everything: alerts, machines, sessions and their replays, the configuration, the audit log | `status:read`, `sessions:read` |
| On call | Acts on alerts; their agents reach machines through the SSH channel | + `alerts:write`, `machines:ssh` |
| Operator | Adds and removes machines, pauses agents, schedules maintenance | + `maintenance:write`, `incidents:write`, `machines:write` |
| Owner | Changes the configuration, invites people, sets roles, manages every agent | + `config:write`, `accounts:write` |

Roles are what the console, the command line and the API speak; the scopes stay underneath, as the [REST API](reference/api.md#scopes) lists them. A person's role is read again on every request, so a change takes effect at once. Beacon always keeps at least one enabled owner: it refuses the change that would leave it without one.

## The first owner

A Beacon without accounts makes a one-time link to set itself up, valid for 30 minutes, and writes it to its log when it starts:

```json
{"level":"WARN","msg":"Beacon has no accounts yet: open this one-time link within 30 minutes to make the first, an owner; beacon setup prints a new one","setup_link":"https://status.example.com/console/setup?code=…","expires":"…"}
```

`docker exec beacon beacon setup` prints another whenever you need it. The link opens `/console/setup`, where you choose your username, display name and password: that account is an owner, and you are signed in. A setup link works once, and none works once Beacon has an account. The link is built from `public_url`; without it, the command prints the path alone, to put Beacon's address in front of.

## Inviting people

An owner invites people on the console's **People** page, choosing the role and a note saying who it is for, or on the host:

```bash
docker exec beacon beacon user invite -role oncall -note "Bob, on call from Monday"
```

Either way Beacon shows a link once (`/console/join?code=…`), valid for 24 hours and for one use: send it to that person only. They choose their username and password and are signed in with the role the invitation gives. The **People** page lists the invitations not yet used, and withdraws one. The chat hears that someone was invited and that someone joined, never the link.

## Signing in

`/console/sign-in` asks for a username and a password; every page of the console sends whoever is not signed in there, and back afterwards.

- A sign-in lasts 12 hours. The browser holds only the session's id, in a cookie that is `HttpOnly`, `Secure` and `SameSite=Strict`; the database keeps its SHA-256.
- 20 failed sign-ins from one address within 10 minutes block that address for the rest of those minutes, as the API's tokens do; 10 wrong passwords for one username within 10 minutes, from anywhere, block that username for the rest of them, the right password included.
- A wrong username and a wrong password get the same answer, and take as long.
- **Sign in with a token instead**, below the form, still takes a pasted Beacon token, for one more release: for scripts, and for whoever runs a Beacon that has no account yet.

## Passwords

A password has at least 10 characters, is not the username, and is not one of the commonest passwords; there are no other rules. Beacon keeps only its argon2id hash (64 MiB, 3 passes, 4 lanes), compared in constant time, and makes the hash again at the next sign-in when those parameters change.

### Changing your password

On `/console/account` (**Your account** in the menu at the foot of the sidebar), with your current password; a wrong one counts against the same limits as a failed sign-in. Every other sign-in of yours ends; the one you changed it from stays.

### A forgotten password

An owner makes a reset link on the **People** page, or on the host:

```bash
docker exec beacon beacon user reset bob
```

The link (`/console/reset?code=…`) is shown once and works once, for 30 minutes. With it the person chooses a new password, and every sign-in of theirs ends.

## Changing a role, disabling someone

On the **People** page, or with `beacon user role bob operator`, `beacon user disable bob` and `beacon user enable bob`. A lower role limits the person's sign-ins and agents at once; disabling ends the person's sign-ins, stops their agents, and closes what those agents have open on machines. Nobody disables themself on the console.

## Agents

An agent acts for a person, its owner. It has:

- a **name**, 2 to 40 lower-case letters, digits, dots, dashes and underscores, unique in Beacon; the audit log calls it `owner / name`;
- a **preset**, what it may do;
- the **machines** it may reach through the SSH channel, all of them or a list;
- its **SSH keys**, for its own ssh, scp, sftp and rsync;
- its **credentials**: tokens (`lbt_…`), for headless agents and CI, and OAuth sign-ins, for interactive clients. An agent may have several of each.

The console's **Agents** page lists your agents (an owner sees everyone's), and each agent's page changes its preset and machines, lists its tokens, OAuth sign-ins (each with what it was allowed) and keys, revokes any of them, and deletes the agent with all its credentials. A person makes agents for themself; an owner for anyone.

### Presets

| Preset | Asks for |
| --- | --- |
| Read only | A viewer's scopes |
| On call | An on-call person's scopes |
| Operator | An operator's scopes |

An agent may do what its preset asks for **and** its owner's role allows, worked out again on every request: an on-call person's agent never gets more than on call, and demoting or disabling the person limits or stops their agents at once, on the very tokens they already hold. No preset gives `config:write` or `accounts:write`. A preset above the owner's role is refused when the agent is made or changed.

A **custom** agent lists scopes of its own instead: a claimed legacy token keeps its scopes this way, and an owner can make one on the command line (`beacon agent add … -scopes status:read,config:write`), still within the owner's role.

## Connecting Claude Code or Codex

An interactive client signs in through the browser with MCP's OAuth: you add Beacon's `/mcp`, sign in from the client, and allow it on a page of Beacon's. No token is copied, stored by hand or pasted into a chat.

### Claude Code

```bash
claude mcp add --transport http beacon https://status.example.com/mcp
claude mcp login beacon
```

Or run `/mcp` inside Claude Code and choose Beacon. Claude Code names itself with its client ID metadata document (`https://claude.ai/oauth/claude-code-client-metadata`), which Beacon fetches, so it registers nothing; it listens for the answer on `http://localhost:<port>/callback`. The browser opens Beacon's consent page; once you allow it, Claude Code keeps its tokens and refreshes them by itself. Where no browser is at hand (over ssh), `claude mcp login beacon --no-browser`, in an interactive terminal, prints the address to open elsewhere and asks for the address you are sent back to, pasted in. `claude mcp logout beacon` ends the sign-in on Beacon's side too; `claude mcp remove beacon` alone forgets Claude Code's tokens without revoking them, so log out first. A server added with an `Authorization` header uses that token and does not sign in with OAuth.

### Codex

```bash
codex mcp add beacon --url https://status.example.com/mcp
codex mcp login beacon
```

Codex names itself with its own client ID metadata document (`https://chatgpt.com/oauth/codex/client.json`), which Beacon fetches, so it registers nothing either; it listens on `http://127.0.0.1:<port>/callback`. A client without a metadata document registers itself (dynamic client registration).

### Without the internet

Beacon fetches the metadata documents of Claude Code and Codex from the internet (`https://claude.ai/…`, `https://chatgpt.com/…`). A Beacon that cannot reach the internet turns that off:

```yaml
oauth:
  client_id_documents: false     # true by default
```

Its metadata then says `client_id_metadata_document_supported: false`, it refuses a `client_id` that is a URL, and the clients register themselves with dynamic registration instead (`codex mcp login beacon --oauth-client-registration dcr` makes Codex do so).

### Allowing a client

Signing in from a client sends your browser to `/oauth/authorize`: you sign in to the console if you have not, and see:

- who asks, by the name the client gives, and the address Beacon sends you back to (a `127.0.0.1` or `localhost` port on your machine, for these clients);
- the **agent name**: your agent for this client if you have one, otherwise one made from the client's name (`claude-code`). The same name later reuses that agent; another person's agent's name is refused;
- **what it may do**: the presets within your role, and for a reused agent none stronger than the agent's own; the strongest one chosen, or the strongest the client asked for; you may choose less;
- the **machines** it may reach: all, or only the ones you tick; for a reused agent, only among the agent's own;
- **for how long**: 1 day, 30 days or 90 days, 30 by default. The client gets a refresh token valid that long, whether or not it asked for `offline_access`, and asks again after.

**Allow** makes a new agent with the preset and machines you allowed, or reuses yours as it is, and sends a one-time code back to the client; **Deny** tells the client you said no. Either is in the audit log (`oauth_allow`, `oauth_deny`) and allowing is echoed to the chat.

Each sign-in keeps what you allowed as a ceiling of its own: the client may do what its sign-in's preset and machines allow, within what the agent may do, within your role, read afresh on every request. Allowing a client never changes an existing agent, so another client's sign-in, the agent's tokens and its keys are never widened by it; to give a reused agent more, change the agent on its page first, then sign in again from the client. Each sign-in is listed on the agent's page under **OAuth sign-ins**, with the client, what it was allowed (**Allowed**: the preset and machines), when it was made, when it was last used and when it ends; **Revoke** ends it. A person signed in to the console with a token cannot allow a client: an OAuth client always acts for a person.

## Headless agents and CI

A program that runs alone gets a token. On the console, **Agents** → **A headless agent or a CI job**: the name, the preset, the machines, and how long its token lasts (365 days by default). On the host:

```bash
docker exec beacon beacon agent add ci-deploy -owner alice -preset oncall -machines edge-1,edge-2 -expires 365d
```

The token is shown once, alone on standard output for the command line, with what uses it ready to paste: Claude Code with `--header "Authorization: Bearer …"`, Codex with `--bearer-token-env-var BEACON_TOKEN`, curl, and the `~/.ssh/config` block once the agent has a key. Beacon keeps only its SHA-256. The token renews itself before it expires ([Tokens that renew themselves](agents.md#tokens-that-renew-themselves)), so nobody hands it a new one; another one comes from **New token** on the agent's page. `beacon agent list` shows the agents and `beacon agent remove NAME` deletes one.

## SSH keys

The SSH channel signs an agent in by its public key. An agent registers its own key through MCP, with the credential it already has:

| Tool | Route | What |
| --- | --- | --- |
| `add_ssh_key` | `POST /api/v1/ssh-keys` | Register your public key (an `authorized_keys` line) with a comment |
| `list_ssh_keys` | `GET /api/v1/ssh-keys` | Your keys |
| `remove_ssh_key` | `DELETE /api/v1/ssh-keys` | Remove one, by its fingerprint |

They take `machines:ssh`. A key an agent registers itself belongs to the credential it came with, a token or an OAuth sign-in (`via`: `token:NAME` or `grant:ID`): it signs in only while that credential works, with what that credential may do, and it is removed when the credential is revoked or the agent deleted. The owner sees each key on the agent's page, with its comment and when it was added, and can add or remove keys there (or `-ssh-key` with `beacon agent add`); a key added that way stays with the agent, and signs in while the agent has a credential that has not expired. The chat hears of every key added or removed. A key belongs to one agent or token and is never a machine's connector key, and it reaches only the machines its holder may reach.

## Legacy tokens

Every token made before accounts keeps working as it did, with its own scopes and the audit log calling it by its name. The console's **Tokens** page marks a token **Legacy** when it carries only people's scopes and belongs to no agent; an owner can claim it as an agent of a person, on that page or on the host:

```bash
docker exec beacon beacon token claim claude-code -owner alice
```

The token keeps its secret and its expiry, so whatever uses it goes on working; its machines and SSH keys move to the agent; and from then on it may do what the agent's preset allows within the person's role. The preset is the one with exactly the token's scopes, or custom (the token's scopes, kept) when none matches, so a claim never widens what a token may do; `-preset` chooses another, and `-agent` another name, or an existing agent of the same person to add the token to; that is refused when the agent may do more than the token may (its scopes or machines), and the token is then claimed as an agent of its own. A console sign-in made with the token acts as `alice / claude-code` from then on. Machines' tokens (probes, hosts, heartbeats, the rule engine, change senders, metrics readers) are never legacy and never claimed: they stay as they are. Nothing breaks if nobody claims anything.

## Who the audit log names

| Who acted | Named |
| --- | --- |
| A person on the console | `alice` |
| An agent, through any of its credentials or its SSH key | `alice / claude-code` |
| A legacy token, or a machine's | the token's name, as before |
| The command line on the host | `cli:` and the operating system's user |
| Beacon itself | `beacon` |

The chat echoes use the same names, as do the sessions on machines. Entries made before keep their old names.

## Stopping an agent

A revoked or narrowed credential is refused from the next request on, and what the agent has open on machines through the SSH channel (connections, port forwards, tool calls) is closed at once when it may no longer reach the machine:

- a token revoked: what was opened with it, the keys the agent registered with it (and the sessions signed in with them), and the sessions signed in with the agent's other keys when that was its last credential;
- an OAuth sign-in revoked, on the agent's page or by the client logging out (`claude mcp logout beacon`): likewise;
- the agent deleted;
- the machine taken from its list, or a preset without `machines:ssh`;
- its owner disabled, or demoted below on call;
- the key a session signed in with removed.

Beacon checks again every minute, for credentials that expired. Pausing the whole channel still stops every agent at once.

## Moving an existing deployment

Nothing breaks: every token keeps working. To bring people and agents in:

1. Start this version. With no account yet, it writes the setup link to its log; `beacon setup` prints another.
2. Open it and make your owner account.
3. Invite the people (**People**, or `beacon user invite`), each with their role.
4. Claim the legacy tokens that agents use as agents of their people (**Tokens**, or `beacon token claim`), or leave them as they are. Tokens people signed in with are better revoked once they have accounts.
5. Move interactive agents to OAuth when convenient: `claude mcp remove beacon`, then add it again without a header and sign in.

## The command line

| Command | What |
| --- | --- |
| `beacon setup` | Print a new setup link, while Beacon has no account |
| `beacon user list` | People, their roles, last sign-in and agents, and the invitations not yet used |
| `beacon user invite -role ROLE [-note TEXT]` | Print an invitation link |
| `beacon user role NAME ROLE` | Change a person's role |
| `beacon user disable NAME`, `beacon user enable NAME` | Stop or allow a person and their agents |
| `beacon user reset NAME` | Print a password reset link |
| `beacon agent add NAME -owner USER [-preset P] [-machines a,b] [-expires 365d] [-ssh-key KEY]` | Make an agent and its token (`-ssh-key @FILE` reads the key from a file); the token on stdout, the snippets on stderr |
| `beacon agent list`, `beacon agent remove NAME` | List agents; delete one with its credentials |
| `beacon token claim NAME -owner USER [-agent NAME] [-preset P]` | Make a legacy token an agent's |

`beacon token create` stays for machines' tokens and for experts. The same operations are on the REST API under `/api/v1/users` and `/api/v1/ssh-keys`, and as the MCP tools `list_users` and `invite_user` ([REST API](reference/api.md#people-and-ssh-keys), [MCP tools](reference/mcp.md#people-and-ssh-keys)). They need `accounts:write`, which no preset gives: an agent manages people only if an owner gave it that scope on the command line. Password resets are not offered through MCP.

## Security

- Beacon is the only sign-in. It keeps hashes, never secrets: argon2id for passwords, SHA-256 for tokens, OAuth access and refresh tokens, the codes of setup, invitation and reset links, and console session ids.
- The setup link is written to the log on purpose, so the first owner can be made without a token: it lasts 30 minutes, works once, and works only while Beacon has no account.
- What an agent may do is decided on every request from its preset and its owner's role, and what an OAuth client may do also from the preset and machines its sign-in was allowed. Allowing a client never widens an existing agent, and a key an agent registers itself lasts only as long as the credential it registered it with.
- OAuth access tokens (`lba_…`) last an hour and are accepted wherever a bearer token is (the REST API and MCP; the SSH channel signs in by key). A refresh token (`lbr_…`) lasts as long as the person allowed and is replaced at every use; sending one that was already replaced revokes the whole sign-in, and the chat hears of it. Revoking any token of a sign-in at `/oauth/revoke`, as a client does when it logs out, ends the whole sign-in. An authorization code works once, for 60 seconds, only with PKCE (`S256`): a wrong verifier spends it, and a code sent twice revokes what it gave.
- Clients are public: no client secrets. A client registers itself with redirect URIs on the loopback interface (`http://127.0.0.1`, `http://localhost`, `http://[::1]`, any port) or `https` ones, nothing else; registrations are limited to 10 per address in 10 minutes, and one never used is forgotten after a day. Claude Code and Codex register nothing: they name themselves with client ID metadata documents. A client that names itself with a client ID metadata document (an `https` URL) has it fetched, unless `oauth.client_id_documents` is false, with timeouts, at most 64 KiB and three redirects, all `https`, never from a private address whatever the name resolves to, and kept for 10 minutes.
- Beacon names itself (`iss`) in every answer to an authorization request, as the issuer its metadata gives, and accepts only its own `/mcp`, or itself, as the `resource` asked for. The consent page lets its form lead only to the client's redirect address; its forms carry the session's CSRF secret.
- The console's cookie is `Secure`: serve Beacon over HTTPS, under `public_url`, which is also the issuer the clients check. Claude Code sends credentials only to an `https` token endpoint, or one on `localhost`.

### The OAuth endpoints

| Route | What |
| --- | --- |
| `GET /.well-known/oauth-protected-resource/mcp`, `GET /.well-known/oauth-protected-resource` | The protected resource metadata (RFC 9728) of `/mcp`, and of Beacon as a whole. A `401` from `/mcp` names the first in its `WWW-Authenticate` |
| `GET /.well-known/oauth-authorization-server` | The authorization server's metadata (RFC 8414): the endpoints, `S256`, the presets and `offline_access` as scopes, public clients, client ID metadata documents |
| `POST /oauth/register` | Dynamic client registration (RFC 7591), public clients only |
| `GET /oauth/authorize`, `POST /oauth/authorize` | The consent page, for a person signed in to the console |
| `POST /oauth/token` | Codes and refresh tokens for tokens |
| `POST /oauth/revoke` | Revocation (RFC 7009): any token of a sign-in, access or refresh, ends the whole sign-in and closes what it has open on machines |
