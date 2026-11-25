# Review what agents did

The console shows in one place what Beacon and the agents are doing. It also holds the switch that stops agents' access to machines. By the end of this page you can find:

- whether anything needs attention;
- the alerts firing, and what has been done about each;
- what is silenced, and what maintenance is planned;
- every session an agent opened on a machine, with its replay;
- the people and their agents, with their credentials;
- the configuration and its history.

The console is for whoever is on call or answers for the machines. The agents themselves use the [REST API](../reference/api.md) and the [MCP tools](../reference/mcp.md). A link on an alert's card opens that alert's page without signing in. Signing in shows the rest.

You need an account ([Invite your team](people.md)). A viewer sees everything; acting on what you see takes the role for it.

Beacon serves the console itself, at `/console` on port 17001, beside the API and MCP, under `public_url`. The site's root `/` is public ([Publish a status page](status-pages.md)).

## Sign in {#signing-in}

Open `https://status.example.com/console`. You are sent to `/console/sign-in` and asked for your username and password. Then you are sent back to the page you asked for.

- **A sign-in lasts 12 hours.**
- **The browser holds only the session's id**, in a cookie that is `HttpOnly`, `Secure` and `SameSite=Strict`. Beacon keeps only its SHA-256.
- **The cookie is `Secure`**, so the console needs HTTPS (or `localhost`).
- **What the session may do is what the person's role allows**, read again on every request. A lower role takes effect at once.
- **Disabling the account, or a new password,** ends its sessions.

When the sign-in fails, the form says why. [Signing in to the console fails](../self-hosting/troubleshooting.md#signing-in-to-the-console-fails) lists every message and what to do about it.

**Sign in with a token instead**, below the form, still takes a pasted Beacon token, for one more release. It is for scripts, and for a Beacon that has no account yet, where it is the only form.

- The token needs `status:read` at least.
- The session lasts 12 hours and is bound to the token, so revoking the token ends it.
- A token's sign-in cannot allow an MCP client: an OAuth client always acts for a person.

## The pages {#the-pages}

Every page has the same frame:

- the sections in a narrow sidebar, with counts;
- the filters (facets) on the left of lists;
- at the foot, Beacon's version, with links to the documentation, the code and where to report an issue.

Lists show the newest first, 50 to a page. Every filter is a link, so every view has a URL you can share.

The sidebar has the overview on its own, then three parts:

- **Monitoring**: what Beacon watches and what it does about it. Alerts, monitors, status pages, silences and maintenance, and the changes reported by CI and Flux.
- **Agents and machines**: the agents, the machines they reach through the SSH channel, their sessions, and the hooks that wake them.
- **Administration**: the configuration and its history, the people (for an owner), the tokens and the audit log.

| Page | What it shows |
| --- | --- |
| `/console` overview | Whether anything needs attention, five figures, and what Beacon sees going wrong ([below](#the-overview)) |
| `/console/alerts`, `/console/alerts/{id}` | Every alert group (resolved ones for 7 days), with facets for state, severity, service, acknowledged and silenced. The alert's page ([below](#the-alerts-page)) has the forms to acknowledge, silence, end its silence and add a note |
| `/console/monitors`, `/console/monitors/{service}` | What is monitored and how it is doing now ([below](#monitors-and-status-pages)) |
| `/console/status-pages`, `/console/status-pages/{name}` | The status pages as the public sees them now ([below](#monitors-and-status-pages)) |
| `/console/silences` | What keeps alerts from paging people: the maintenance windows and the silences ([below](#silences-and-maintenance)) |
| `/console/machines`, `/console/machines/{name}` | The machines the channel reaches, whether one behind NAT is connected, and sessions in the last 24 hours. With `machines:write`: add a machine, edit its note, whether it is critical and its account, pin the host key again, change a connector's key, and remove a machine after typing its name. One machine's address, host key, recent sessions and the commands run on it. Search by machine name, address or note. Open **Add a machine** when you need the registration form. Members with SSH access can select **SSH** to get the command and terminal settings for their own account. The separate **Pause all SSH access** control stops machine operations through Beacon while monitoring continues |
| `/console/sessions`, `/console/sessions/{id}` | Every session, with facets for agent, machine, kind, via, exit status and day, and search over commands. The session's page has its replay ([below](#the-sessions-page-and-its-replay)) |
| `/console/agents`, `/console/agents/{name}` | Your agents (an owner sees everyone's): what each may do now, credentials, keys. Open the connection instructions for an OAuth client or the form for a headless agent's token. The editor shows the chosen preset's permissions. Choose all current and future machines, or select the specific machines it may reach. Each agent's page changes its preset and machines, and revokes its credentials. An owner who signs in as a person also sets custom scopes there |
| `/console/tokens` | Every token: scopes, machines, keys, expiry, whose agent it is. **Legacy** marks those from before accounts, with the form to claim one. Never a secret |
| `/console/config`, `/console/config/history/{version}` | The configuration, read-only (locations, heartbeats, resources, target sets, modules), and its history. Who, when, through what and why; each version item by item as a diff, with the form to revert it |
| `/console/hooks` | The agent hooks configured, and every call made: hook, event, status, attempts, when, for which alert |
| `/console/people`, `/console/account` | The people and their roles: inviting, changing a role, disabling, password resets ([Invite your team](people.md)). Your account and your password |
| `/console/changes`, `/console/audit` | The change events from CI and Flux. The audit log, with facets for who, action and via |
| `/console/setup`, `/console/join`, `/console/reset`, `/oauth/authorize` | The pages a one-time link opens, and the consent page an MCP client sends a person to |

### The overview {#the-overview}

`/console` opens with a sentence that says whether anything needs attention. Five figures follow, each linking to its list:

- firing alerts, by severity;
- services answering at every location;
- probe locations reporting;
- machines, and those behind NAT connected;
- agent sessions today, with the status of today's alert deliveries to agent hooks.

**Needs attention** lists the alerts firing, the worst first. Those someone acknowledged or silenced come after the rest. Then it lists what Beacon sees going wrong that no firing alert names:

- a service failing;
- a probe location gone quiet;
- a machine behind NAT not connected;
- a failed delivery to an agent hook;
- the channel paused.

Beside it are:

- the status pages, as the public sees them;
- what is coming up: maintenance in progress or planned, and resources expiring within 30 days;
- today's agent hook deliveries;
- the latest sessions and changes;
- the channel's state and the configuration's version.

### Monitors and status pages {#monitors-and-status-pages}

`/console/monitors` shows what is monitored and how it is doing now:

- **every service** the target sets probe, failing first, with its state across the locations, its targets, the status pages that show it and its firing alert;
- **every heartbeat**, with its last success;
- **every resource**, with when it expires.

A service's page has what each location saw of its targets in the last 30 minutes, with the reasons. It also lists its targets with their target sets, and its alerts.

With `config:write`, use **Add HTTP monitor** to select an endpoint, its check profile and its probe locations. The new service opens immediately so you can see when its first results arrive. The change appears in **Configuration** history. See [Monitor a service](monitoring.md#add-an-http-monitor).

`/console/status-pages` shows the status pages as the public sees them now: each page's address, state, components, active incidents and maintenance windows. One page's view has:

- its components, with the services behind each, their state and 90-day availability;
- its incidents, each linking to its alert;
- its maintenance;
- links to the page, its JSON, feed and badge, as Beacon serves them under `/status/<name>`.

### Machine settings history {#machine-settings-history}

Open **Settings history** on a machine's page to see each revision of its account, critical flag and note, along with who changed them. With `machines:write`, you can copy an older snapshot into a new revision. Beacon refuses an edit or restore from a stale page. Reload to review newer changes.

Enrolling or explicitly replacing a machine starts a new history. Restoring settings preserves its current address and keys. For a machine that predates this feature, the first recorded edit keeps a snapshot of the preceding settings.

### Silences and maintenance {#silences-and-maintenance}

`/console/silences` shows what keeps alerts from paging people:

- **The maintenance windows.** Those in progress and planned come first, then the last 20 that ended. Each shows its services, its times and who planned it.
- **The silences** in force or to come, as Alertmanager holds them. Each shows what it matches, its comment, who made it, when it ends and the alert it silences.

What each role may do:

- With `maintenance:write`, schedule a window, end it early, or call it off before it starts.
- Whoever may silence (`alerts:write`) can end a silence.
- A window's silences end with the window.

**Schedule maintenance** lets you select services and preview which public pages and components will announce the window. A service's monitor page opens the same form with that service selected. Agents and scripts use `schedule_maintenance` or `POST /api/v1/maintenance`.

### Forms and roles {#forms-and-roles}

The console's forms are the API's and the command line's operations, on the same scopes. They are done as the signed-in person, with `via` `console`. Like every other write, each is recorded in the audit log and echoed to the alert chat.

A form the person's role does not allow is not offered, and is refused if sent anyway. A page your role does not allow says "Not with your role", with the scope it needs and your role.

### Team credentials {#team-credentials}

On installations with the credential store enabled, owners can store and replace a bearer token, send a test request as themselves, and delegate or revoke the same request for an agent. See [Use a team credential](team-credentials.md).

## The alert's page {#the-alerts-page}

The card's **Timeline** button opens it. The response panel shows how long the alert has been firing, who acknowledged it, and the result of sending the alert to hooks and notification providers. The current handover note stays next to the response actions, and a failed agent hook is shown separately from human ownership. On a phone or desktop, you can acknowledge or take over the alert, probe again or jump to the note form from the same panel.

A new notification opens the incident it describes, even when the same service has fired again. Once the complete evidence is replaced or expires, sign in to open that incident's retained history. Links issued before incident-specific links were added still open the alert group. If an incident changed while a response form was open, review the alert before taking action again.

The Feishu app card updates in place when sessions in the alert's evidence close. **Finished SSH sessions** shows session, machine and command counts and the most recent finish time. Open **Timeline** to find the replays. Beacon batches session updates and spaces them at least five minutes apart per card. It reserves the last 10% of the Feishu budget for urgent delivery.

After signing in, **Linked machines** takes you to an enrolled machine named by the rule's explicit `machine` label or by the catalogue entry for a heartbeat. If your account can use SSH, the link opens the instructions for connecting with your own key. Otherwise it opens the machine details. Open the recorded session from the alert's **Sessions**, then add the handover in **Notes**.

- **The alert itself**: title, status, severity, since when, labels, runbook.
- **Notifications and response**: provider acceptances, failed submissions, acknowledgement history and silences. A provider acceptance records receipt by its API. A Pushover acknowledgement records a response from the device. A button ends the silence.
- **Probes**: for the alert's service, each location's latest result and reason, as `get_evidence` returns it.
- **Related alerts** in the same failure domain.
- **Agents**: whether they reach machines, delivery to each hook, and one line "N sessions on M machines, K commands".
- **Sessions**: every session in the alert's window on the machines its labels name, each with a **Replay** link.
- **Notes**, **Changes** and the **Timeline**: everything in time order, as `get_timeline` returns it.

The window opens 30 minutes before the alert started, as for the [evidence](on-call.md#the-evidence). **Probe again now** waits up to 45 seconds and opens the result on the timeline, including failures and missing results by location and target.

## Incident history {#incident-history}

From **Alerts**, open **Incident history** to revisit each resolved episode, including when the same group fired again. The outcome records when Beacon marked the alert resolved, the latest alert action associated with each known kind of actor, and the latest on-demand check's counts by target and location. Standalone credentials appear as unattributed. A delivered hook alone does not make an agent a participant.

**Related audit entries** opens the existing audit log filtered by the alert group and the episode's time window. The audit log's signed checkpoints cover the audit log. The outcome is a compact summary of observed activity and does not prove who caused recovery.

The full alert evidence stays for seven days after recovery, or until the group fires again. Beacon then retains links to at most eight related sessions with the outcome. The outcome stays for `storage.incident_retention` after recovery, 180 days by default. Audit entries and session recordings follow their own retention settings. History is private to viewers with `status:read` and separate from the public status pages. Previously removed episodes cannot be reconstructed on upgrade.

## The session's page and its replay {#the-sessions-page-and-its-replay}

The session's page shows:

- who, on which machine, and through what (ssh, MCP or the API);
- when it started and how long it took;
- the exit status;
- the recording, replayed in the page with [asciinema-player](https://github.com/asciinema/asciinema-player).

The recording is asciicast v3, as the SSH channel records it: output, input, resizes, a marker at each command, the exit status. Below the player come an interactive shell's commands with their exit statuses, or a command's output. The recording can also be downloaded.

A session still open shows its record. The recording plays once the session ends.

## Signed links {#signed-links}

The links Beacon puts in the chat open one page without signing in. Such a link ends in `?s=<expiry>.<signature>`:

- the expiry is in Unix seconds;
- the signature is the base64url HMAC-SHA256 of the method, the path and the expiry, under a key Beacon keeps in its database.

Links are valid for `pages.link_ttl`, 30 days by default. Each grants exactly one page, view-only: the alert's page, or the session's page with its recording. A link for another path, with a changed expiry, or expired, fails. Every failure is a plain 404, the same as for a page that does not exist, so a wrong link reveals nothing.

**Sign in** on that page returns to the same alert or session. After sign-in, the page uses the account's own permissions.

**Anyone holding a link can see that page, recording included, without signing in anywhere.** So:

- the chat the links go to must be private;
- a link should not be forwarded further than the recording may travel;
- keep `link_ttl` as short as the chat's habits allow.

Beacon hands links out wherever a person might pick one up:

- the card's **Timeline** button;
- the chat's echoes, with a **Replay** link per session;
- the API and MCP answers, so that an agent can hand a person a link.

Links made before 0.3.0 named `/alerts/{id}` and `/sessions/{id}` at the site's root. Those paths still answer, sent on (`308`) to the page's path under `/console`. Every other old path of the console is sent on to its place under `/console` with its query, so bookmarks keep working ([The console says 404 on an old link](../self-hosting/troubleshooting.md#the-console-says-404-on-an-old-link)).

## Tokens instead {#tokens-instead}

Any credential with the scope opens the alert's and the session's pages as it opens the matching API routes, view-only. That is a token (`lbt_…`) or an OAuth access token (`lba_…`), with:

- `status:read` for an alert's page;
- `sessions:read` or `machines:ssh` for a session's page and its recording.

Send it as `Authorization: Bearer …`. A credential without the scope, an unknown one and none all get the same 404.

## How it is built, what it does not do {#how-it-is-built-what-it-does-not-do}

The console is on the OpenLinkOS design system ([`@openlinkos/ui`](https://github.com/openlinkos/design/tree/master/packages/ui)), with its workbench shell and Beacon's brand, so the family's tools look alike.

- **Nothing fetched from elsewhere.** Its stylesheet, fonts and the player's code are embedded in Beacon's binary. The `Content-Security-Policy` allows scripts, styles, fonts and forms from Beacon only.
- **One page for every screen**, all by CSS: a full sidebar from 1200px, a rail of icons from 860px, and lists restacked as cards at phone widths.
- **Outside text cleaned.** Text from outside Beacon (alert annotations, notes, commands) is cleaned by the bridge and escaped on the way out.
- **Language and scheme.** The console is in the configured language, English or Chinese, and follows the system's light or dark scheme.
- **Not indexed.** Its pages are marked `noindex`, since their links carry signatures.

Two things it does not do:

- **No sign-in from anywhere else.** Beacon's own accounts are the only way in.
- **No approval workflow.** The forms are what the person's role already allows through the API, and the pause switch is the brake.

Use the files, the API, MCP's `put_config` or the command line for advanced checks, catalogue and status page definitions. Configuration changes from every entry point are checked, versioned and attributed.

## Check it

Run the loop once as a person:

1. **Open an alert** from the card or `/console/alerts`.
2. **Read its probes and its timeline.** You will see each location's latest result with its reason, and the events in time order.
3. **Open one session's replay from it.** Every session on the alert's machines has a **Replay** link.
4. **Check the record is complete.** Put `get_timeline` (or the API's `GET /api/v1/alerts/{id}/timeline`) next to the alert's page. Both should show the same events: the hook's call, the sessions, the `probe_now` result and the notes.

## Next

- [Put an agent on call](on-call.md): where the records the console shows come from.
- [Publish a status page](status-pages.md): the same story, public and one service at a time.
- [Stop an agent, pause everything](stop.md): the pause switch on the Machines page, and the rest of the brakes.
