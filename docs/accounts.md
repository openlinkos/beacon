# People and their agents

This page is for whoever owns a Beacon. At the end of it, everyone on your team signs in with an account of their own and a role that says what they may do; each person's agents act for that person and never do more than that person's role allows; Claude Code and Codex sign in through the browser, without anyone copying a token; and a headless agent or a CI job has a token that renews itself. You made the first account in [Up in 30 minutes](install.md#4-the-first-agent), step 4; this page goes on from there.

Two ideas carry the rest. **People have roles, agents have presets**, so nobody types a list of scopes. And **everything is named after a person**: the audit log, the chat and the records of sessions say `alice` for what Alice did on the console and `alice / claude-code` for what her agent did. People sign in only with Beacon's own accounts: there is no sign-in through GitHub, Feishu, OIDC, LDAP or anything else.

## Roles

Each role may do what the one above it may, and more:

| Role | May | Scopes it gives |
| --- | --- | --- |
| Viewer | See everything: alerts, machines, sessions and their replays, the configuration, the audit log | `status:read`, `sessions:read` |
| On call | Also act on alerts; their agents reach machines through the SSH channel | + `alerts:write`, `machines:ssh` |
| Operator | Also add and remove machines, pause agents, schedule maintenance | + `maintenance:write`, `incidents:write`, `machines:write` |
| Owner | Also change the configuration, invite people, set roles, manage every agent | + `config:write`, `accounts:write` |

The console, the command line and the API speak of roles; the scopes stay underneath, as the [REST API](reference/api.md#scopes) lists them. A role is read again on every request, so a change takes effect at once, for the person and for their agents. Beacon always keeps at least one enabled owner, and refuses the change that would leave it without one.

## The first owner

A Beacon without accounts writes a one-time setup link to its log when it starts:

```json
{"time":"…","level":"WARN","msg":"Beacon has no accounts yet: open this one-time link within 30 minutes to make the first, an owner; beacon setup prints a new one","setup_link":"https://status.example.com/console/setup?code=…","expires":"…"}
```

`docker compose logs beacon | grep setup_link` finds it. Once it has expired, print another on the centre:

```bash
docker exec beacon beacon setup
```

```
Open this link within 30 minutes (until 2026-11-02 10:30:00) to make the first account, an owner. It works once:
https://status.example.com/console/setup?code=…
```

The link opens **Set Beacon up**: choose a username, a display name and a password. That account is an owner, and you are signed in. A link works once, and none works once Beacon has an account; from then on, people join by invitation. The link is built from `public_url`; without it, `beacon setup` prints the path alone, for you to put Beacon's address in front of.

Until the first account exists, the sign-in page offers only a token, and says where to find the setup link.

## Invite people

An owner invites each person with the role they need: on the console's **People** page, **Invite someone** with the role and a note saying who it is for; or on the centre:

```bash
docker exec beacon beacon user invite -role oncall -note "Bob, on call from Monday"
```

```
An invitation to join as oncall, until 2026-11-03 09:00:00. Send the link to that person only; it works once:
https://status.example.com/console/join?code=…
```

The link is shown only then, and works once, for 24 hours. The person opens it, chooses a username and a password, and is signed in with the role the invitation gives. **People** lists the invitations not yet used, and **Withdraw** ends one. The chat hears that someone was invited and that someone joined, never the link.

A username is 2 to 32 lower-case letters, digits, dots, dashes and underscores, starting with a letter or a digit. It names the person in the audit log, so it never changes; `beacon`, `root`, `rules` and `unknown` are reserved. The display name is only how the console shows the person.

## Sign in

`/console/sign-in` asks for a username and a password. Every page of the console sends whoever is not signed in there, and back to that page afterwards. A sign-in lasts 12 hours; [The console](pages.md#signing-in) has the cookie and what each message on the form means.

- 20 failed sign-ins from one address within 10 minutes block that address for the rest of those minutes, as for the API's tokens. 10 wrong passwords for one username within 10 minutes, from anywhere, block that username for the rest of them, the right password included.
- A wrong username and a wrong password get the same answer, `Wrong username or password.`, and take as long.
- **Sign in with a token instead**, below the form, still takes a pasted Beacon token, for one more release: for scripts, and for a Beacon that has no account yet.

## Passwords

A password has at least 10 characters, is not the username, and is not one of the commonest passwords; there are no other rules. Beacon keeps only its argon2id hash (64 MiB, 3 passes, 4 lanes), compared in constant time, and makes the hash again at the next sign-in when those parameters change.

**To change your own**, open **Your account** in the menu at the foot of the sidebar (`/console/account`) and give the current password with the new one. A wrong current password counts against the same limits as a failed sign-in. Your other sign-ins end; the one you changed it from stays.

**A forgotten password** is reset by an owner, with **Password reset link** on the **People** page, or on the centre:

```bash
docker exec beacon beacon user reset bob
```

```
With this link bob chooses a new password, once, until 2026-11-02 10:30:00; their sign-ins end then:
https://status.example.com/console/reset?code=…
```

The link is shown once and works once, for 30 minutes. With it the person chooses a new password, and every sign-in of theirs ends.

## Change a role, disable someone

On the **People** page, or on the centre:

```bash
docker exec beacon beacon user role bob operator   # bob is operator now; their sessions and agents follow at once
docker exec beacon beacon user disable bob         # bob is disabled
docker exec beacon beacon user enable bob          # bob is enabled
```

A lower role limits the person's sign-ins and their agents at once. Disabling someone ends their sign-ins, stops their agents and closes what those agents have open on machines. Nobody disables themself on the console. `beacon user list` shows everyone, with their role, status, last sign-in and agents, then the invitations not yet used.

## Agents

An agent is a program that acts for one person, its owner: Claude Code on someone's workstation, the agent `beacon-oncall` starts when a hook wakes it, a CI job. It has:

- a **name**, 2 to 40 lower-case letters, digits, dots, dashes and underscores, unique in Beacon; the audit log names it `owner / name`;
- a **preset**, what it may do;
- the **machines** it may reach through the SSH channel: all of them, or a list;
- its **SSH keys**, for its own ssh, scp, sftp and rsync;
- its **credentials**: tokens (`lbt_…`) for headless agents and CI, OAuth sign-ins for interactive clients; several of each if need be.

| Preset | Asks for |
| --- | --- |
| Read only | A viewer's scopes |
| On call | An on-call person's scopes: also working alerts and reaching machines |
| Operator | An operator's scopes |

An agent may do what its preset asks for **and** its owner's role allows, worked out again on every request. An on-call person's agent never does more than on call, and demoting or disabling the person limits or stops their agents at once, on the tokens they already hold. No preset gives `config:write` or `accounts:write`. A preset above the owner's role is refused when the agent is made or changed.

A **custom** agent lists scopes of its own instead of a preset: a claimed legacy token keeps its scopes that way ([Moving from tokens](#moving-from-tokens)), and an owner can make one on the command line (`beacon agent add … -scopes status:read,config:write`), still within the owner's role.

The console's **Agents** page lists your agents; an owner sees everyone's. Each agent's page shows what it may do now, changes its preset and machines, lists its OAuth sign-ins, tokens and keys, revokes or removes any of them, makes a new token, and deletes the agent with all its credentials. People make and manage their own agents; an owner manages anyone's.

## Connect Claude Code

An interactive client signs in through the browser, with MCP's OAuth: you add Beacon's `/mcp` without a token, sign in from the client, and allow it on Beacon's consent page. On the workstation where Claude Code runs:

```bash
claude mcp add --scope user --transport http beacon https://status.example.com/mcp
```

Then run `/mcp` in Claude Code, choose `beacon` and authenticate; or, from the shell, `claude mcp login beacon`. The browser opens Beacon's consent page, **Claude Code wants to use Beacon as you**, after you sign in to the console if you were not signed in. It asks four things:

- **Agent name**: `claude-code`, or your existing agent for Claude Code. The same name later reuses that agent. Another person's agent's name is refused, so when someone else already has a `claude-code`, the form offers `claude-code-alice`.
- **What it may do**: the presets within your role, the strongest chosen, or the strongest the client asked for; for an agent you reuse, none stronger than the agent's own. You may choose less.
- **Machines**: all of them, or only the ones you tick; for an agent you reuse, only among its own.
- **For how long**: 1 day, 30 days or 90 days; 30 by default.

The foot of the page says where Beacon sends you back to: a `localhost` port on your machine, where Claude Code is listening. **Allow** makes the agent, or reuses yours as it is, and sends Claude Code a one-time code, which it exchanges for its tokens; `/mcp` then shows `beacon` connected. **Deny** tells Claude Code you said no. Both are in the audit log (`oauth_allow`, `oauth_deny`), and allowing is echoed to the chat.

From then on Claude Code holds an access token valid for an hour and a refresh token valid for as long as you allowed, and refreshes by itself; when the time you allowed is up, it asks you to sign in again. No token is copied, stored by hand or pasted into a chat.

- **No browser at hand**, as over ssh: `claude mcp login beacon --no-browser`, in an interactive terminal, prints the address to open elsewhere and asks for the address you are sent back to, pasted in.
- **Signing out**: `claude mcp logout beacon` ends the sign-in on Beacon's side too. `claude mcp remove beacon` alone forgets Claude Code's tokens without revoking them, so log out first, or revoke the sign-in on the agent's page.
- **A header wins**: a server added with `--header "Authorization: Bearer …"` uses that token and never signs in with OAuth.

Claude Code names itself with its client ID metadata document, `https://claude.ai/oauth/claude-code-client-metadata`, which Beacon fetches, so it registers nothing; it listens for the answer on `http://localhost:<port>/callback`.

## Connect Codex

```bash
codex mcp add beacon --url https://status.example.com/mcp
codex mcp login beacon
```

The browser opens the same consent page, and the agent is named after the client unless you name it otherwise. Codex names itself with its own client ID metadata document (`https://chatgpt.com/oauth/codex/client.json`), so it registers nothing either; it listens on `http://127.0.0.1:<port>/callback`. A client without a metadata document registers itself first (dynamic client registration), then signs in the same way.

### Without the internet

Beacon fetches the metadata documents of Claude Code and Codex from the internet (`https://claude.ai/…`, `https://chatgpt.com/…`). A Beacon that cannot reach the internet turns that off in `config.yaml`, and restarts:

```yaml
oauth:
  client_id_documents: false     # true by default
```

Its metadata then says `client_id_metadata_document_supported: false`, it refuses a `client_id` that is a URL, and the clients register themselves instead (`codex mcp login beacon --oauth-client-registration dcr` makes Codex do so).

## What a sign-in may do

Each sign-in keeps what you allowed as a ceiling of its own: the client may do what its sign-in's preset and machines allow, within what the agent may do, within your role, read afresh on every request. Allowing a client never changes an existing agent, so it never widens another client's sign-in, the agent's tokens or its keys. To give an agent you reuse more, change the agent on its page first, then sign in again from the client.

The agent's page lists each sign-in under **OAuth sign-ins**: the client, what it was allowed (**Allowed**: the preset and machines), when it was made, when it was last used and when it ends; **Revoke** ends it. A person signed in to the console with a token cannot allow a client: an OAuth client always acts for a person.

## Headless agents and CI

A program that runs alone, such as the agent `beacon-oncall` starts or a CI job, has no browser to sign in with: it gets a token. On the console, **Agents**, then **A headless agent or a CI job**: the name, the owner (for an owner), the preset, the machines, and how long its token lasts (30, 90 or 365 days; 365 by default). Or on the centre:

```bash
docker exec beacon beacon agent add ci-runner -owner alice -preset oncall -machines edge-1,edge-2 > ci-runner.token
```

The token alone goes to standard output, so `> ci-runner.token` keeps it in a file; the rest goes to standard error:

```
agent alice / ci-runner (oncall, may do status:read,sessions:read,alerts:write,machines:ssh) made; its token ci-runner expires 2027-11-02. The secret is shown only now:

Claude Code:
claude mcp add --transport http beacon https://status.example.com/mcp --header "Authorization: Bearer lbt_…"

Codex:
export BEACON_TOKEN=lbt_…
codex mcp add beacon --url https://status.example.com/mcp --bearer-token-env-var BEACON_TOKEN

curl:
curl -H "Authorization: Bearer lbt_…" https://status.example.com/api/v1/token
```

The console shows the token once, with the same commands ready to paste. Beacon keeps only its SHA-256.

- `-preset` is the strongest the owner's role allows when left out; `-expires` is `365d` when left out, at most two years.
- `-ssh-key KEY`, or `-ssh-key @FILE` to read it from a file, gives the agent a key at once; the `~/.ssh/config` block for its own ssh then follows the commands.
- The token renews itself before it expires ([Tokens that renew themselves](agents.md#tokens-that-renew-themselves)), so nobody hands the agent a new one. When one is needed after all, **New token** on the agent's page makes it.
- `beacon agent list` shows every agent with its owner, preset, what it may do now, machines, and how many tokens, OAuth sign-ins and keys it has; `beacon agent remove NAME` deletes one with all its credentials.

## SSH keys

The SSH channel signs an agent in by its public key. An agent registers its own key through MCP or the API, with the credential it already has, so nobody copies a key anywhere:

| Tool | Route | What |
| --- | --- | --- |
| `add_ssh_key` | `POST /api/v1/ssh-keys` | Register a public key (an `authorized_keys` line), with a comment |
| `list_ssh_keys` | `GET /api/v1/ssh-keys` | The agent's keys |
| `remove_ssh_key` | `DELETE /api/v1/ssh-keys` | Remove one, by its fingerprint; connections signed in with it are closed |

They take `machines:ssh`. Ask Claude Code *"Register my SSH key ~/.ssh/id_ed25519.pub with Beacon"* and it calls `add_ssh_key`; `list_machines` then gives the `ssh_config` block for its own ssh ([The SSH channel](ssh.md#connect-with-your-own-ssh-scp-and-rsync)).

- **A key the agent registers itself** belongs to the credential it came with, a token or an OAuth sign-in (`via`: `token:NAME` or `grant:ID`). It signs in only while that credential works, with what that credential may do, and it goes when the credential is revoked or the agent is deleted.
- **A key the owner adds**, with **Add an SSH key** on the agent's page or `-ssh-key` on `beacon agent add`, stays with the agent, and signs in while the agent has a credential that has not expired.
- An agent holds at most 20 keys. A key belongs to one agent or token, never to a machine's connector, and reaches only the machines its holder may reach. The chat hears of every key added or removed.

## Stop an agent

A revoked or narrowed credential is refused from its next request on, and what the agent has open on machines through the SSH channel (connections, forwarded ports, tool calls) is closed at once when it may no longer reach the machine. That happens when:

- a token is revoked (**Revoke** on the agent's page, or `beacon token revoke NAME`): what was opened with it, the keys the agent registered with it and the sessions signed in with those, and, when it was the agent's last credential, the sessions signed in with the agent's other keys;
- an OAuth sign-in is revoked, on the agent's page or by the client logging out (`claude mcp logout beacon`): likewise;
- the agent is deleted;
- a machine is taken from its list, or its preset no longer gives `machines:ssh`;
- its owner is disabled, or demoted below on call;
- the key a session signed in with is removed.

Beacon also checks every minute, for credentials that expired meanwhile. To stop every agent at once, pause the channel ([Pausing](ssh.md#pausing)).

## Who the audit log names

| Who acted | Named |
| --- | --- |
| A person on the console | `alice` |
| An agent, through any of its credentials or its SSH key | `alice / claude-code` |
| A legacy token, or a machine's | the token's name, as before |
| The command line on the centre | `cli:` and the operating system's user |
| Beacon itself | `beacon` |

The chat's echoes and the sessions on machines use the same names. Entries made before keep the names they had.

## Moving from tokens

On a Beacon that ran before accounts, every token keeps working as it did, with its own scopes, and the audit log calls it by its name. Nothing breaks if nobody does anything. To bring people and agents in:

1. **Start this version.** With no account yet, it writes the setup link to its log, and how many legacy tokens it found (`legacy_tokens`). Open the link and make your owner account ([The first owner](#the-first-owner)).
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

- The token keeps its secret and its expiry, so whatever uses it goes on working. Its machines and SSH keys move to the agent, and from then on it may do what the agent's preset allows within the person's role. A console sign-in made with the token acts as `alice / claude-code` from then on.
- The preset is the one with exactly the token's scopes, or custom (the token's scopes, kept) when none matches, so a claim never widens what a token may do. `-preset` chooses another.
- `-agent` gives the agent another name, or names an existing agent of the same person to add the token to. That is refused when the agent may do more than the token may now (its scopes or its machines): claim the token as an agent of its own then.
- Machines' tokens (probes, hosts, heartbeats, the rule engine, change senders, metrics readers) are never legacy and never claimed: they stay as they are.

## The command line

| Command | What |
| --- | --- |
| `beacon setup` | Print a new setup link, while Beacon has no account |
| `beacon user list` | People, their roles, status, last sign-in and agents, and the invitations not yet used |
| `beacon user invite -role ROLE [-note TEXT]` | Print an invitation link |
| `beacon user role NAME ROLE` | Change a person's role |
| `beacon user disable NAME`, `beacon user enable NAME` | Stop or allow a person and their agents |
| `beacon user reset NAME` | Print a password reset link |
| `beacon agent add NAME -owner USER [-preset P] [-machines a,b] [-expires 365d] [-ssh-key KEY]` | Make an agent and its token (`-ssh-key @FILE` reads the key from a file; `-scopes a,b` instead of `-preset` makes it custom); the token on stdout, the commands on stderr |
| `beacon agent list`, `beacon agent remove NAME` | List agents; delete one with its credentials |
| `beacon token claim NAME -owner USER [-agent NAME] [-preset P]` | Make a legacy token an agent's |

`ROLE` is `viewer`, `oncall`, `operator` or `owner`; a preset is `readonly`, `oncall` or `operator`. `beacon token create` stays, for machines' tokens and for experts. The same operations are on the REST API under `/api/v1/users` and `/api/v1/ssh-keys`, and the MCP tools `list_users` and `invite_user` ([REST API](reference/api.md#people-and-ssh-keys), [MCP tools](reference/mcp.md#people-and-ssh-keys)). They need `accounts:write`, which no preset gives: an agent manages people only when an owner gave it that scope on the command line. Changing roles, disabling people and password resets are not offered through MCP.

## Security

- **Beacon is the only sign-in, and it keeps hashes, never secrets**: argon2id for passwords; SHA-256 for tokens, OAuth access and refresh tokens, the codes of setup, invitation and reset links, and console session ids.
- **The setup link is written to the log on purpose**, so that the first owner can be made without a token. It lasts 30 minutes, works once, and works only while Beacon has no account.
- **What an agent may do is decided on every request**, from its preset and its owner's role, and for an OAuth client also from the preset and machines its sign-in was allowed. Allowing a client never widens an existing agent, and a key an agent registers itself lasts only as long as the credential it registered it with.
- **OAuth access tokens** (`lba_…`) last an hour and are accepted wherever a bearer token is: the REST API and MCP; the SSH channel signs in by key. A **refresh token** (`lbr_…`) lasts as long as the person allowed and is replaced at every use; sending one that was already replaced revokes the whole sign-in, and the chat hears of it. Revoking any token of a sign-in at `/oauth/revoke`, as a client does when it logs out, ends the whole sign-in. An **authorization code** works once, for 60 seconds, only with PKCE (`S256`): a wrong verifier spends it, and a code sent twice revokes what it gave.
- **Clients are public**: no client secrets. A client registers itself with redirect URIs on the loopback interface (`http://127.0.0.1`, `http://localhost`, `http://[::1]`, any port) or `https` ones, nothing else; registrations are limited to 10 per address in 10 minutes, and one never used is forgotten after a day. A client that names itself with a client ID metadata document (an `https` URL) has it fetched, unless `oauth.client_id_documents` is false: with timeouts, at most 64 KiB and three redirects, all `https`, never from a private address whatever the name resolves to, and kept for 10 minutes.
- **Beacon names itself** (`iss`) in every answer to an authorization request, as the issuer its metadata gives, and accepts only its own `/mcp`, or itself, as the `resource` asked for. The consent page lets its form lead only to the client's redirect address, and its forms carry the session's CSRF secret.
- **HTTPS throughout**: the console's cookie is `Secure`, so serve Beacon over HTTPS under `public_url`, which is also the issuer the clients check. Claude Code sends credentials only to an `https` token endpoint, or one on `localhost`.

### The OAuth endpoints

| Route | What |
| --- | --- |
| `GET /.well-known/oauth-protected-resource/mcp`, `GET /.well-known/oauth-protected-resource` | The protected resource metadata (RFC 9728) of `/mcp`, and of Beacon as a whole. A `401` from `/mcp` names the first in its `WWW-Authenticate` |
| `GET /.well-known/oauth-authorization-server` | The authorization server's metadata (RFC 8414): the endpoints, `S256`, the presets and `offline_access` as scopes, public clients, client ID metadata documents |
| `POST /oauth/register` | Dynamic client registration (RFC 7591), public clients only |
| `GET /oauth/authorize`, `POST /oauth/authorize` | The consent page, for a person signed in to the console |
| `POST /oauth/token` | Tokens for a code or a refresh token |
| `POST /oauth/revoke` | Revocation (RFC 7009): any token of a sign-in, access or refresh, ends the whole sign-in and closes what it has open on machines |
