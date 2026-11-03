# Review what agents did

By the end of this page you can find, in one place, what Beacon and the agents are doing: the alerts firing and what has been done about each, every session an agent opened on a machine with its replay, the people and their agents with their credentials, the configuration and its history — and the switch that stops agents' access to machines. The console is for whoever is on call or answers for the machines; the agents themselves use the [REST API](../reference/api.md) and the [MCP tools](../reference/mcp.md). A link on an alert's card opens that alert's page without signing in; signing in shows the rest.

You need an account ([Invite your team](people.md)); a viewer sees everything, and acting on what you see takes the role for it. The console is served by Beacon itself, at `/console` on port 17001 beside the API and MCP, under `public_url`; the site's root `/` is public ([Publish a status page](status-pages.md)).

## Sign in {#signing-in}

Open `https://status.example.com/console`: you are sent to `/console/sign-in`, asked for your username and password, and sent back to the page you asked for. A sign-in lasts 12 hours; the browser holds only the session's id, in a cookie that is `HttpOnly`, `Secure` and `SameSite=Strict`, and Beacon keeps only its SHA-256. What the session may do is what the person's role allows, read again on every request: a lower role takes effect at once, and disabling the account or a new password ends its sessions. The cookie is `Secure`, so the console needs HTTPS (or `localhost`); when the sign-in fails, the form says why — [Signing in to the console fails](../self-hosting/troubleshooting.md#signing-in-to-the-console-fails) lists every message and what to do about it.

**Sign in with a token instead**, below the form, still takes a pasted Beacon token, for one more release: for scripts, and on a Beacon that has no account yet, where it is the only form. The token needs `status:read` at least; the session lasts 12 hours and is bound to the token, so revoking the token ends it. A token's sign-in cannot allow an MCP client: an OAuth client always acts for a person.

## The pages {#the-pages}

Every page has the same frame: the sections in a narrow sidebar with counts, the filters (facets) on the left of lists, and at the foot Beacon's version with links to the documentation, the code and where to report an issue. Lists show the newest first, 50 to a page, and every filter is a link, so every view has a URL you can share.

| Page | What it shows |
| --- | --- |
| `/console` overview | Firing alerts by severity; locations expected and reporting; machines, the channel and whether it is paused; agents woken today; sessions today; the configuration's version. Each figure links to its list |
| `/console/alerts`, `/console/alerts/{id}` | Every alert group (resolved ones for 7 days) with facets for state, severity, service, acknowledged and silenced; the alert's page (below) with the forms to acknowledge, silence and add a note |
| `/console/machines`, `/console/machines/{name}` | The machines the channel reaches, whether one behind NAT is connected, sessions in the last 24 hours; one machine's address, host key, recent sessions and the commands run on it. Above them the pause switch for the whole channel |
| `/console/sessions`, `/console/sessions/{id}` | Every session with facets for agent, machine, kind, via, exit status and day, and search over commands; the session's page with its replay (below) |
| `/console/agents`, `/console/agents/{name}` | Your agents (an owner sees everyone's): what each may do now, credentials, keys; the commands to connect a client by OAuth, the form for a headless agent's token, and each agent's page to change and revoke |
| `/console/tokens` | Every token: scopes, machines, keys, expiry, whose agent it is, and **Legacy** for those from before accounts, with the form to claim one. Never a secret |
| `/console/config`, `/console/config/history/{version}` | The configuration read-only (locations, heartbeats, resources, target sets, modules) and its history: who, when, through what and why, each version item by item as a diff, with the form to revert it |
| `/console/hooks` | The agent hooks configured and every call made: hook, event, status, attempts, when, for which alert |
| `/console/people`, `/console/account` | The people and their roles; inviting, changing a role, disabling, password resets ([Invite your team](people.md)); your account and your password |
| `/console/audit`, `/console/changes` | The audit log with facets for who, action and via; the change events from CI and Flux |
| `/console/setup`, `/console/join`, `/console/reset`, `/oauth/authorize` | The pages a one-time link opens, and the consent page an MCP client sends a person to |

The console's forms are the API's and the command line's operations, on the same scopes, done as the signed-in person with `via` `console`: recorded in the audit log and echoed to the alert chat like every other write. A form the person's role does not allow is not offered, and is refused if sent anyway. A page your role does not allow says "Not with your role", with the scope it needs and your role.

## The alert's page {#the-alerts-page}

The card's **Timeline** button opens it. From top to bottom: the alert itself (title, status, severity, since when, labels, runbook); what people were told and did (acknowledged, silenced, paged); **Probes** — for the alert's service, each location's latest result and reason, as `get_evidence` returns it; **Related alerts** in the same failure domain; **Agents** — whether they reach machines, which hooks were woken, one line "N sessions on M machines, K commands"; **Sessions** — every session in the alert's window on the machines its labels name, each with a **Replay** link; and **Notes**, **Changes** and the **Timeline**, everything in time order as `get_timeline` returns it. The window opens 30 minutes before the alert started, as for the [evidence](on-call.md#the-evidence).

## The session's page and its replay {#the-sessions-page-and-its-replay}

Who, on which machine, through what (ssh, MCP or the API), when it started and how long it took, the exit status — and the recording replayed in the page with [asciinema-player](https://github.com/asciinema/asciinema-player). The recording is asciicast v3, as the SSH channel records it: output, input, resizes, a marker at each command, the exit status. Below the player come an interactive shell's commands with their exit statuses, or a command's output; the recording can also be downloaded. A session still open shows its record; the recording plays once it ends.

## Signed links {#signed-links}

The links Beacon puts in the chat open one page without signing in. Such a link ends in `?s=<expiry>.<signature>`: the expiry as Unix seconds, and the base64url HMAC-SHA256 of the method, the path and the expiry, under a key Beacon keeps in its database. Links are valid for `pages.link_ttl`, 30 days by default, and grant exactly one page, view-only: the alert's page, or the session's page with its recording. A link for another path, a changed expiry or expired fails, and every failure is a plain 404, the same as for a page that does not exist, so a wrong link reveals nothing.

**Anyone holding a link can see that page, recording included, without signing in anywhere.** The chat the links go to must be private, and a link should not be forwarded further than the recording may travel; keep `link_ttl` as short as the chat's habits allow. Beacon hands them out wherever a person might pick one up: the card's **Timeline** button, the chat's echoes with a **Replay** link per session, and the API and MCP answers, so that an agent can hand a person a link.

Links made before 0.3.0 named `/alerts/{id}` and `/sessions/{id}` at the site's root; those paths still answer, sent on (`308`) to the page's path under `/console`. Every other old path of the console is sent on to its place under `/console` with its query, so bookmarks keep working ([The console says 404 on an old link](../self-hosting/troubleshooting.md#the-console-says-404-on-an-old-link)).

## Tokens instead {#tokens-instead}

Any credential with the scope opens the alert's and the session's pages as it opens the matching API routes, view-only: a token (`lbt_…`) or an OAuth access token (`lba_…`), with `status:read` for an alert's page, `sessions:read` or `machines:ssh` for a session's page and its recording. Send it as `Authorization: Bearer …`; a credential without the scope, an unknown one and none all get the same 404.

## How it is built, what it does not do {#how-it-is-built-what-it-does-not-do}

The console is on the OpenLinkOS design system ([`@openlinkos/ui`](https://github.com/openlinkos/design/tree/master/packages/ui)), with its workbench shell and Beacon's brand, so the family's tools look alike; its stylesheet, fonts and the player's code are embedded in Beacon's binary and nothing is fetched from anywhere else — the `Content-Security-Policy` allows scripts, styles, fonts and forms from Beacon only. One page for every screen: a full sidebar from 1200px, a rail of icons from 860px, and lists restacked as cards at phone widths, all by CSS. Text from outside Beacon (alert annotations, notes, commands) is cleaned by the bridge and escaped on the way out. The console is in the configured language, English or Chinese, follows the system's light or dark scheme, and its pages are marked `noindex`, since their links carry signatures.

Three things it does not do: no sign-in from anywhere else — Beacon's own accounts are the only way in; no approval workflow — the forms are what the person's role already allows through the API, and the pause switch is the brake; and it does not edit the configuration beyond reverting a change, which goes through the same checks as the files.

## Check it

Run the loop once as a person: from the card or `/console/alerts`, open an alert, read its probes and its timeline, and open one session's replay from it. Then check the record is complete: `get_timeline` (or the API's `GET /api/v1/alerts/{id}/timeline`) next to the alert's page should show the same events — the hook's call, the sessions, the `probe_now` result and the notes.

## Next

- [Put an agent on call](on-call.md): where the records the console shows come from.
- [Publish a status page](status-pages.md): the same story, public and one service at a time.
- [Stop an agent, pause everything](stop.md): the pause switch on the Machines page, and the rest of the brakes.
