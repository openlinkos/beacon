# The console

The console is where the people on your team look at what Beacon and the agents are doing: the alerts firing and what has been done about each, every session an agent opened on a machine with its replay, the tokens, the configuration and its history, and the switch that stops agents' access to machines. It is for whoever is on call or answers for the machines; the agents themselves use the [API](reference/api.md) and the [MCP tools](reference/mcp.md). A link on an alert's card opens that alert's page without signing in; signing in shows the rest.

Beacon serves the console itself, at `/console` on port 17001 beside the API and MCP, under `public_url`. The site's root `/` is public: the default [status page](status.md), or, without one, a page that names the deployment.

## Signing in

Today a person signs in with a Beacon token, pasted like a password; what the console then shows and allows is what that token's scopes allow. Make a token for yourself on the centre (the machine Beacon runs on):

```bash
docker exec beacon beacon token create you -scopes status:read,sessions:read,alerts:write -expires 30d
```

The secret is printed once. Open `https://status.example.com/console`: you are sent to `/console/sign-in`, and back to the page you asked for once the token is accepted. The public pages at `/` have **Sign in** at the right of their header (**Console** once you are signed in); a status page on a host name of its own has neither, since the console does not answer there.

A token needs `status:read` at least. When the sign-in fails, the form says why:

| Message | What to do |
| --- | --- |
| "The token is unknown or has expired." | Make a new token, or check that you pasted all of it (it starts with `lbt_`) |
| "Token … lacks status:read, which the console needs to show anything." | Make one with `status:read` |
| "Too many failed sign-ins from here; try again in a few minutes." | 20 failures from one client address within 10 minutes block it for the rest of those 10 minutes, as on the API |
| "The form has expired; paste the token again." | Reload the page. If it keeps saying so, the browser is not keeping the form's cookie: the console needs HTTPS (or `localhost`), as below |

- A sign-in is a session kept in Beacon's database for 12 hours, or until the token expires if that comes sooner. The browser holds only the session's id, in a cookie that is `HttpOnly`, `Secure` and `SameSite=Strict` (`__Host-beacon-console`, on the path `/` as a `__Host-` cookie must be); the database keeps only its SHA-256, and the token itself is not stored.
- The session is bound to that token: revoking the token, or replacing it with another of the same name (revoked and made again), ends it at once. Its scopes are read again on every request.
- The sign-in form carries a secret of its own, so no other site can sign a person in with a token of its choosing. Signing in is recorded in the audit log (`console_sign_in`).
- **Sign out** is in the menu at the foot of the sidebar.
- The cookie is `Secure`, so the console needs HTTPS (or `localhost`): put it behind the TLS proxy that serves `public_url`, as the reference deployment does.

## The pages

Every page has the same frame: the sections in a narrow sidebar, with counts; a top bar with the breadcrumb, the heading and a count; on lists, the filters (facets) on the left; and at the foot Beacon's version with links to the documentation, the code on GitHub and where to report an issue. Lists show the newest first, 50 to a page, and every filter is a link, so every view has a URL you can share.

| Page | What it shows | Needs |
| --- | --- | --- |
| `/console` overview | Firing alerts by severity; probe locations expected and reporting through the gateway in the last 5 minutes; machines (how many are critical, how many of those behind NAT are connected); the SSH channel (running, or paused since when); agents woken today and how that went; sessions today; the configuration's version and who made it; Beacon's version. Each figure links to its list | `status:read` |
| `/console/alerts` | Every alert group Beacon holds (resolved ones for 7 days), with facets for state, severity, service, acknowledged and silenced, and search over titles, services and labels | `status:read` |
| `/console/alerts/{id}` | The alert's page (below), with the forms to acknowledge it with a note, silence it for 1, 4 or 24 hours, and add a note | `status:read`; the forms `alerts:write` |
| `/console/machines` | The machines the SSH channel reaches: address, or whether a machine behind NAT is connected; critical; sessions in the last 24 hours; the last session. Above them, the pause switch for the whole channel | `status:read` or `machines:ssh`; the switch `machines:write` |
| `/console/machines/{name}` | One machine: its address, account, host key and note, its recent sessions, and the commands run on it | `status:read` or `machines:ssh` |
| `/console/sessions` | Every session, with facets for agent, machine, kind, via, exit status and day, and search over commands | `status:read` or `machines:ssh` |
| `/console/sessions/{id}` | The session's page (below), with its replay | `sessions:read` or `machines:ssh` |
| `/console/tokens` | Every token: name, scopes, the machines it reaches, the fingerprints of its SSH keys, when it was made and by whom, when it expires, when it last renewed itself. Never a secret | `status:read`; revoking `machines:write` |
| `/console/config` | The configuration Beacon holds, read-only: probe locations (expected, reporting), heartbeats, resources, target sets and modules; then the history of versions: who, when, through what and why | `status:read` or `config:write` |
| `/console/config/history/{version}` | What a version changed, item by item, as a diff, with the form to revert it | `status:read` or `config:write`; reverting `config:write` |
| `/console/agents` | The agent hooks configured (the address's host, never its path or query) and every call made: hook, event, status, HTTP status, attempts, when, for which alert | `status:read` |
| `/console/audit` | The audit log, with facets for who, action and via, and search | `status:read` |
| `/console/changes` | Change events from CI, Flux and the like, with facets for source, kind, service and machine | `status:read` |

A page whose scope the token lacks says "Not with this token", with the scope it needs and the scopes the token has. A page that does not exist, or no longer does (an alert group resolved more than 7 days ago), says "Not found" to someone signed in and is a plain 404 for anyone else.

## What the console may do

The console's forms are the API's operations, on the same scopes, done as the signed-in token with `via` `console`: recorded in the audit log and echoed to the alert chat like every other write.

| Form | Scope | The same as |
| --- | --- | --- |
| Acknowledge an alert, with a note | `alerts:write` | `POST /api/v1/alerts/{id}/ack` |
| Silence an alert for 1, 4 or 24 hours, saying why | `alerts:write` | `POST /api/v1/silences` |
| Add a note to an alert | `alerts:write` | `POST /api/v1/alerts/{id}/notes` |
| Pause or resume agents' access to machines | `machines:write` | `POST /api/v1/ssh/pause`, `/resume` |
| Revoke a token that reaches machines | `machines:write` | `beacon token revoke` |
| Revert a configuration change | `config:write` | `POST /api/v1/config/history/{version}/revert` |

Every form carries the session's CSRF secret, and one the browser says came from another site is refused. A form the token's scopes do not allow is not offered, and is refused if sent anyway.

**Revoking from the console.** Tokens are made on the centre, through the admin socket, and most are revoked there too. The console revokes only tokens that reach machines through the SSH channel (`machines:ssh`): that is the pause switch for one agent: its new connections and tool calls are refused from then on, without stopping the others, so it takes the pause switch's scope, `machines:write`. A connection the agent already has open stays until it ends; pause the channel to close that too. Revoking also ends the token's console sign-ins; revoking your own signs you out. The tokens of machines and services (probes, hosts, heartbeats, the rule engine, change senders, metrics readers) are not revoked from a page: they keep monitoring running, and no alert may be lost.

## The alert's page: `/console/alerts/{id}`

The card's **Timeline** button opens it. From top to bottom:

- the alert: title, status, severity, since when (or when it resolved and after how long), description, service, labels, the runbook and the dashboard;
- what people were told and did: acknowledged, silenced, buzzed, paged;
- **Probes**: for the alert's service, each location's latest result and reason, as `get_evidence` returns it: OK, failing since when, stale, the failure reason, the duration, the HTTP status, the attempts, the certificate's expiry;
- **Related alerts**: others firing in the same failure domain or for the same service, each linking to its own page;
- **Agents**: whether agents reach machines or the channel is paused and since when; which hooks were woken and when; one line "N sessions on M machines, K commands";
- **Sessions**: every session in the alert's window on the machines its labels name, or by the agents that acknowledged it or left notes: who, how, where, what, the exit status, and for an interactive shell each command with its exit status and duration; each session with a **Replay** link;
- **Notes**, **Changes** (the change events around the alert), **Labels**;
- **Timeline**: everything above in time order, as `get_timeline` returns it.

The window opens 30 minutes before the alert started and closes now, or 30 minutes after it resolved, as for the [evidence](agents.md#the-evidence).

## The session's page: `/console/sessions/{id}`

Who, on which machine, through what (ssh, MCP or the API), what kind of session, when it started and how long it took, the exit status, and the recording replayed in the page with [asciinema-player](https://github.com/asciinema/asciinema-player). The recording is asciicast v3, as the SSH channel records it: output, input, resizes, a marker at each command, the exit status. Below the player come an interactive shell's commands with their exit statuses, or a command's output. The recording can also be downloaded. A session still open shows its record; the recording plays once it ends.

## Signed links

The links Beacon puts in the chat open one page without signing in. Such a link ends in `?s=<expiry>.<signature>`: the expiry as Unix seconds, and the base64url HMAC-SHA256 of the method, the path and the expiry (each followed by a newline), under a 32-byte key Beacon makes at its first start and keeps in its database. Links are valid for `pages.link_ttl`, 30 days by default:

```yaml
public_url: https://beacon.example.com   # where the console is; without it there are no links
pages:
  link_ttl: 720h
```

A link grants exactly one page, view-only, until it expires: the alert's page, or the session's page with its recording. The links inside a page (to sessions, to the recording) last as long as the link that opened it. A link for another path, another method or a changed expiry fails, and so does an expired one. Every failure is a plain 404, the same as for a page that does not exist, so a wrong link reveals nothing.

**Anyone holding a link can see that page, recording included, without signing in anywhere.** The chat the links go to must be private, and a link should not be forwarded further than the recording may travel. There is no command yet to revoke links before they expire (that would mean replacing the key); keep `link_ttl` as short as the chat's habits allow.

A page opened by a link shows that page alone, with **Sign in** in the sidebar for the rest. Signed in, the same link opens the page among the console's others, with the forms the token's scopes allow.

**Links made before 0.3.0.** Until then the console was at the site's root, and the links already in the chat name `/alerts/{id}` and `/sessions/{id}` (and `/sessions/{id}/recording`), inside their signatures. Those paths still answer: a link whose signature verifies for its old path is sent on (`308`) to the page's path under `/console`, with a signature for that path which expires when the old one would have. A forged or expired link is sent on without its signature, and then gets the same 404 as before; signed in, it opens the page as it always did. Every other old path of the console (`/sign-in`, `/alerts`, `/machines`, `/sessions`, `/tokens`, `/config`, `/agents`, `/audit`, `/changes` and the pages under them) is sent on to its place under `/console` with its query, so bookmarks keep working. The forms post to `/console` only.

Beacon hands out links wherever a person might pick one up:

- the card: the **Timeline** button and the agents' block;
- the chat's echoes of what agents did on machines: a **Replay** link per session;
- the API and MCP answers, so that an agent can hand a person a link: `page` on an alert (`GET /api/v1/alerts/{id}`, `get_alert`), `links.page` in the evidence, `url` on a timeline's session events, `page` on a session (`list_sessions`, `get_session`, `done.sessions[]` in the evidence).

## Tokens instead

A bearer token opens the alert's and the session's pages as it opens the matching API routes, view-only: `status:read` for an alert's page, `sessions:read` or `machines:ssh` for a session's page and its recording. Send it as `Authorization: Bearer lbt_…`; a page opened this way links to its sessions and recording for an hour. A token without the scope, an unknown token and no token all get the same 404.

## How it is built

- On the OpenLinkOS design system, [`@openlinkos/ui`](https://github.com/openlinkos/design/tree/master/packages/ui) 0.7.0, with its workbench shell and layouts and Beacon's brand (`data-brand="beacon"`), as Origin's admin workbench is, so the family looks alike. Its stylesheet, icons and the Geist fonts are vendored under `internal/web/static/ol`, with their licences and hashes in its `NOTICE`; `scripts/update-ui.sh` refreshes them from npm.
- One page for every screen: a full sidebar from 1200px, a rail of icons from 860px, a sheet below that, and lists restacked as cards at phone widths, all by CSS. The server never asks how wide the screen is.
- Nothing is fetched from anywhere but Beacon: the stylesheet, the fonts, the player's JavaScript and CSS are embedded in the binary, the icons are a sprite in the page, the only script is the player's, and there is no inline script or style. The `Content-Security-Policy` allows scripts, styles, fonts, fetches and forms from Beacon only (`'wasm-unsafe-eval'` because the player's terminal is WebAssembly). Pages are marked `noindex` (and `/robots.txt` keeps search engines out of `/console`) and send no referrer, since their links carry signatures.
- Beacon is open source, and the console says so: the foot of every page has Beacon's version and links to the documentation ([beacon.openlinkos.com](https://beacon.openlinkos.com/), in the console's language), the code on [GitHub](https://github.com/openlinkos/beacon) and where to [report an issue](https://github.com/openlinkos/beacon/issues/new/choose); the sign-in page says it under its form. These are the only links that leave Beacon by themselves. `branding.powered_by` ([Status pages](status.md#branding)) does not touch them.
- Text from outside Beacon (alert annotations, notes, commands, change events) is cleaned by the bridge and escaped on the way out.
- The console is in the configured `language`, English or Chinese, and follows the system's light or dark scheme.

## What the console does not do

- No approval workflow: the forms are what any holder of the token could already do through the API. The console makes them visible and quick, not gated. People see, agents act, and the pause switch stops them.
- It does not edit the configuration, beyond reverting a change: changes go through the API, MCP and the command line, which check each one as the files are checked. Nor does it make tokens, which the centre's admin socket does.
