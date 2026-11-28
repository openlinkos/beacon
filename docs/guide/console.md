# The operations console

Use the console to handle alerts, connect to machines by SSH and hand incidents over to the next shift. The overview shows what needs your attention. From the console, you can also:

- review alerts, ownership and verification checks;
- schedule maintenance and see what is silenced;
- find recordings of sessions opened by people and agents;
- manage people, agents and their access, or pause agents' access to machines;
- configure routine HTTP monitors and review configuration history.

Agents use the same incident records through the [REST API](../reference/api.md) and the [MCP tools](../reference/mcp.md). A valid signed card link opens the incident for reading. Sign in to see the actions and pages your role permits.

To give colleagues their own accounts, [invite your team](people.md).

Beacon serves the console itself, at `/console` on port 17001, beside the API and MCP, under `public_url`. The site's root `/` is public ([Publish a status page](status-pages.md)).

In a release build, `/console` opens the React console at `/workspace/`. The overview, alerts, people, agents, **MCP & API** capability hub (`/workspace/capabilities`), machines, monitoring, configuration and replay use one navigation layout, light/dark/system theme switching, `⌘K` quick jump, and the same account permissions. Beacon serves the built frontend and its session API in one container. When no default public status page sits at `/`, the root renders a public product showcase introducing Beacon's probing, SSH channel, MCP server and published status pages.

## Sign in {#signing-in}

Open `https://status.example.com/console`. When asked, enter your username and password. Beacon then opens the page you requested.

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

- a task-based sidebar, opened with the menu button on a phone;
- breadcrumbs above the page title and its main actions, with an on-demand filter sheet on **Alerts**;
- at the foot, Beacon's version, with links to the documentation, the code and where to report an issue.

Lists show the newest first, 50 to a page. Every filter is a link, so every view has a URL you can share.

Use the **Overview** to check what needs attention. The sidebar groups the rest by task:

- **Incident response** opens live alerts, the history of individual incidents, Agent deliveries, and the changes reported by CI and Flux.
- **Monitoring** opens service checks, maintenance and silences, and public status pages. The **Monitors** page links directly to services, heartbeats and resources.
- **SSH access** opens machine registration and connection instructions, and the sessions and replays available to your identity.
- **People and agents** opens members and roles, the agents and delegated access you manage, and the **MCP & API** hub showing live operational domains, tools, resources, prompts and copyable Claude Code / Codex connection commands.
- **Administration** opens configuration and its history, access tokens, the audit log, and team credentials when enabled.

| Page | What it shows |
| --- | --- |
| `/console` overview | Whether anything needs attention, five figures, and what Beacon sees going wrong ([below](#the-overview)) |
| `/console/alerts`, `/console/alerts/{id}` | Every alert group (resolved ones for 7 days), with facets for state, severity, service, acknowledged and silenced. The alert's page ([below](#the-alerts-page)) has the forms to acknowledge, silence, end its silence and add a note |
| `/console/monitors`, `/console/monitors/{service}` | What is monitored and how it is doing now ([below](#monitors-and-status-pages)) |
| `/console/status-pages`, `/console/status-pages/{name}` | The status pages as the public sees them now ([below](#monitors-and-status-pages)) |
| `/console/silences` | What keeps alerts from paging people: the maintenance windows and the silences ([below](#silences-and-maintenance)) |
| `/console/machines`, `/console/machines/{name}` | The machines the channel reaches, whether one behind NAT is connected, and sessions in the last 24 hours. With `machines:write`: add a machine, edit its note, whether it is critical and its account, pin the host key again, change a connector's key, and remove a machine after typing its name. One machine's address, host key, recent sessions and the commands run on it. Search by machine name, address or note. Open **Add a machine** when you need the registration form. Members with SSH access can select **SSH** to get the command and terminal settings for their own account. The separate **Pause all SSH access** control stops machine operations through Beacon while monitoring continues |
| `/console/sessions`, `/console/sessions/{id}` | Sessions available to the signed-in identity, with facets for agent, machine, kind, via, exit status and day. With session detail permission the list searches command text and the session page plays the recording ([below](#the-sessions-page-and-its-replay)) |
| `/console/agents`, `/console/agents/{name}` | Your agents (an owner sees everyone's): what each may do now, credentials, keys. Open the connection instructions for an OAuth client or the form for a headless agent's token. The editor shows the chosen preset's permissions. Choose all current and future machines, or select the specific machines it may reach. Each agent's page changes its preset and machines, and revokes its credentials. An owner who signs in as a person also sets custom scopes there |
| `/console/tokens` | Every token: scopes, machines, keys, expiry, whose agent it is. **Legacy** marks those from before accounts, with the form to claim one. Never a secret |
| `/console/config`, `/console/config/history/{version}` | The configuration, read-only (locations, heartbeats, resources, target sets, modules), and its history. Who, when, through what and why; each version item by item as a diff, with the form to revert it |
| `/console/hooks` | The agent hooks configured, and every call made: hook, event, status, attempts, when, for which alert |
| `/console/people`, `/console/account` | The people and their roles: inviting, changing a role, disabling, password resets ([Invite your team](people.md)). Your account and your password |
| `/console/changes`, `/console/audit` | The change events from CI and Flux. The audit log, with facets for who, action and via |
| `/console/setup`, `/console/join`, `/console/reset`, `/oauth/authorize` | The pages a one-time link opens, and the consent page an MCP client sends a person to |

### The overview {#the-overview}

`/console` opens with the current health summary and flags when critical alerts have no route to people. The summary links to firing alerts, monitored services and reporting probe locations. The coverage bar uses the count of locations reporting in the last five minutes.

The response queue follows the summary on every screen. Expand **System details** for machine connectivity, today's SSH sessions, the channel state and the configuration version.

**Needs attention** lists the alerts firing, the worst first. Those someone acknowledged or silenced come after the rest. A Pushover device acknowledgement can pause new escalations. The active, unsilenced alert stays in **Alerts → Needs a responder** until someone takes over. Next, **Needs attention** lists what Beacon sees going wrong that no firing alert names:

- a service failing;
- a probe location gone quiet;
- a machine behind NAT not connected;
- a failed delivery to an agent hook;
- the channel paused.

The overview also shows:

- the status pages, as the public sees them;
- what is coming up: maintenance in progress or planned, and resources expiring within 30 days;
- today's agent hook deliveries;
- the latest sessions and changes.

**Critical notifications** shows the scheduled routes and each recent provider response. For Pushover, it shows a reported device acknowledgement when Beacon can link it to the accepted submission for the current destination. Use **Verify chat delivery** or **Verify phone delivery** for the drill that checks your intended recipient. These links open the guide in a new tab.

### Monitors and status pages {#monitors-and-status-pages}

`/console/monitors` shows what is monitored and how it is doing now:

- **every service** the target sets probe, failing first, with its state across the locations, its targets, the status pages that show it and its firing alert;
- **every heartbeat**, with its last success;
- **every resource**, with when it expires.

A service's page has what each location saw of its targets in the last 30 minutes, with the reasons. It also lists its targets with their target sets, and its alerts.

With `config:write`, use **Add HTTP monitor** to select an endpoint, its check profile and its probe locations. The new service opens immediately so you can see when its first results arrive. The change appears in **Configuration** history. See [Monitor a service](monitoring.md#add-an-http-monitor).

On an imported HTTP monitor, a signed-in person with `config:write` and `machines:write` can choose **Change designated machine** beside an eligible group under **Probe targets**. Check the target set, probe locations, failure domain, page label, visibility and endpoints before saving. Choose an enrolled machine or **No designated machine** to clear the selection. This only changes the response machine. The target set remains managed by its existing configuration source, which should retain the designation if it will be imported again. If an alert covers several groups, it links to a machine only if those groups agree on its enrolment. Reassigning a group applies to new incidents. An open incident can lose its recorded link if a later alert member conflicts with it.

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
- **The silences** in force or to come, as Alertmanager holds them. Each shows what it matches, its comment, who made it, when it ends and the alert it silences. An agent's silence is marked as such: it is recorded but still lets people be notified.

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

The card's **Timeline** button opens it. The response panel sits beside the evidence on desktop and above it on a phone. It shows how long the alert has been firing, who acknowledged it, and the result of sending the alert to hooks and notification providers. **Latest note** shows the latest update with its author and time. **Acknowledgement note** shows what was recorded at the current acknowledgement. Open **All notes** for the full text and earlier updates. A failed agent hook has its own status. You can acknowledge or take over the alert, probe again or jump to the note form from the same panel.

A new notification opens the incident it describes, even when the same service has fired again. Once the complete evidence is replaced or expires, sign in to open that incident's retained history. Links issued before incident-specific links were added still open the alert group. If an incident changed while a response form was open, review the alert before taking action again.

The Feishu app card updates in place when sessions in the alert's evidence close. **Finished SSH sessions** shows session, machine and command counts and the most recent finish time. Open **Timeline** to find the replays. Beacon batches session updates and spaces them at least five minutes apart per card. It reserves the last 10% of the Feishu budget for urgent delivery.

After signing in, **Linked machines** takes you to an enrolled machine named by the rule's explicit `machine` label or by the catalogue entry for a heartbeat. If your account can use SSH, the link opens the instructions for connecting with your own key. Otherwise it opens the machine details. Open the recorded session from the alert's **Sessions**, then add the handover in **Notes**.

For your first connection from an incident, choose **Add an SSH key**, save your public key, then use **Back to SSH setup** for that machine. **Back to incident** returns to the same episode for rechecking and handover.

**Designated service machines** shows the machine chosen for a routine [HTTP monitor](monitoring.md#add-an-http-monitor) when a new service outage began. Open the machine to connect and review sessions available to your account.

- **The alert itself**: title, status, severity, since when, labels, runbook.
- **Notifications and response** shows provider acceptances, failed submissions, acknowledgement history and silences. Provider acceptance records receipt by the API for the configured destination. Pushover reports whether a device acknowledged a page. The response panel shows when the device acknowledgement pauses new escalations and keeps the device response visible after a person takes over. When Beacon pauses new escalations after a console acknowledgement, an earlier Pushover page may still repeat while cancellation is pending. The response panel shows pending cancellation, confirmation from Pushover, or the end of the retry window. Choose **Refresh status** to check whether Pushover has confirmed cancellation or the retry window has ended. The response panel also identifies cancellations that cannot be verified and older pages with no recorded cancellation result. The timeline records cancellation attempts separately from submissions. A button ends the silence.
- **Probes**: for the alert's service, each location's latest result and reason, as `get_evidence` returns it.
- **Related alerts** in the same failure domain.
- **Agents**: whether they reach machines, delivery to each hook, and one line "N sessions on M machines, K commands".
- **Sessions**: records on related machines in the alert's window, filtered by your current machine access. With session detail permission, **Replay** opens the session.
- **Notes**, **Changes** and the **Timeline**: everything in time order, as `get_timeline` returns it.

The window opens 30 minutes before the alert started, as for the [evidence](on-call.md#the-evidence). **Probe again now** waits up to 45 seconds and opens the result on the timeline, including failures and missing results by location and target.

When you reopen the incident or take over from another person, **Latest on-demand check** brings the most recent recorded result into the response panel with its completion time. **View check results** opens the observations from that check. Earlier retained checks remain available in the timeline. A run in progress leaves the most recent recorded result visible. The alert's status follows the monitoring rule.

## Incident history {#incident-history}

Open **Started episodes** to review everything that began in a chosen UTC period, including incidents still open. Filter by severity and origin, then open each episode to see its evidence. The rows show hook acceptance, programme reports, passing checks and recorded participation together. Phone device responses remain in the incident’s notification history. Older rows with no recorded opening severity show the latest available severity. The view shows the earliest retained recovery time for resolved records.

From **Alerts**, open **Incident history** to revisit each resolved episode, including when the same group fired again. The outcome records when Beacon marked the alert resolved, the latest alert action associated with each known kind of actor, and the latest on-demand check's counts by target and location. Standalone credentials appear as unattributed. A delivered hook alone does not make an agent a participant.

Use **Severity**, **Resolved within** and **Evidence** to narrow the retained history. For a shift review, select **Critical** and **No passing check recorded** to find incidents with no complete pass retained near rule recovery. Each row shows which evidence is recorded and keeps the latest on-demand result separate. A passing check can be retained even when a later check failed or missed observations.

An Agent trigger report comes from the programme path for the firing alert, including a report that the programme could not start. A human acknowledgement comes from an identified person. Older records can lack either kind of evidence. Open an episode to see the recorded acknowledgement and the underlying Agent and check results.

Choose **Passing check requested by an Agent** to find retained complete passes requested by an identified Agent. Open an episode to review the check time, requester and related activity together.

**Agent deliveries** keeps the hook's delivery status and any programme report for the same episode. You can see when a hook accepted the alert and whether the programme reported a start, a failure or completion. A programme report records what the runner observed. Older archived episodes may lack this evidence.

Beacon also keeps one complete passing check near rule recovery. It keeps the last complete pass finished at or before rule recovery, or the first one finished afterwards if no earlier check passed. A complete pass requires at least one requested target-location observation and successful results for all of them. The check time records when Beacon finished collecting the results. You can compare this result with a later check that failed or missed observations, and see who requested each check where the record identifies them.

**Related audit entries** opens the existing audit log filtered by the alert group and the episode's time window. The audit log's signed checkpoints cover the audit log. The outcome is a compact summary of observed activity and does not prove who caused recovery.

The full alert evidence stays for seven days after recovery, or until the group fires again. Beacon then retains links to at most eight related sessions with the outcome. The outcome stays for `storage.incident_retention` after recovery, 180 days by default. Audit entries and session recordings follow their own retention settings. History is private to viewers with `status:read` and separate from the public status pages. Previously removed episodes cannot be reconstructed on upgrade.

## The session's page and its replay {#the-sessions-page-and-its-replay}

The session's page shows:

- who, on which machine, and through what (ssh, MCP or the API);
- when it started and how long it took;
- the exit status;
- recording and command transcript status, shown independently.

An available asciicast v3 recording can be downloaded or replayed with [asciinema-player](https://github.com/asciinema/asciinema-player). It can contain input, output, window sizes, shell command markers and the exit status. The **Command transcript** panel shows captured structured shell commands with their exits, or a command's output.

**Capture pending** means Beacon has no recorded end for the session. **Truncated** means only part was captured, and you can open the saved part when available. The page shows storage read failures separately from unknown capture status on older sessions. SFTP and port forwards keep connection metadata. Shells without usable command integration may have a replay and only metadata in the **Command transcript** panel.

The console applies your current permissions to session lists, alert timelines, command text and recordings. A credential limited to selected machines sees only sessions that belong to those machines' current enrolments. Enrolling a machine again under the same name creates a new enrolment. Credentials with `status:read` can see permitted session summaries. Command text and recordings require `sessions:read` or `machines:ssh` as well as access to the recorded machine. Older SSH audit entries that identify machines by name alone show command text only to credentials with unrestricted session detail access.

## Signed links {#signed-links}

Beacon's signed links let anyone who holds the link open the shared page without signing in. The signature is part of the `s` query parameter, and Beacon keeps the signing key in its database. Links last for `pages.link_ttl`, 30 days by default. A signed-out request with an expired, revoked or invalid link gets a plain 404.

An alert link opens one alert episode for viewing. Sign in with your own account to respond or browse related alerts and SSH replays, subject to that account's permissions. Command text from related SSH sessions follows the same access rules as replays. A session link lets a signed-out recipient open that session and play or download its recording when available. **Sign in** returns you to the same page. Opening a shared address while signed in uses your role and machine access.

To share from the console, sign in as an owner and open the alert or session. Choose **Manage share links**, then **Create share link**. Copy the displayed link and check its expiry. An alert share opens the current episode while its detail is retained. Send links only to people who should see the alert or recording, and set `link_ttl` to fit how long recipients need access.

Use **Revoke all share links** on that management page to end access through links already issued for the resource:

- For an alert, revocation covers every episode in its alert group, including links in chat.
- For a session, revocation covers both the session page and the signed recording link.

You can create a fresh link afterwards. A link previously issued for a related alert or session is a separate grant. Revoke those links from that resource's own management page. Signed-in users continue to access what their accounts allow.

Beacon stores revocations in its database. Restoring an older backup also restores its revocation state, so a link revoked after that backup can work again until it expires.

The card's **Timeline** button and the alert links in REST and MCP answers use signed links to the incident. The chat's **Replay** links and session links in REST and MCP answers take the reader to sign-in for the session. Previously issued signed links retain their expiry and revocation rules.

Older links may point to `/alerts/{id}` or `/sessions/{id}` at the site's root. A valid link redirects (`308`) to the same page under `/console`. Expiry and revocation apply across both paths. Other old console paths redirect with their query parameters, so bookmarks keep working ([The console says 404 on an old link](../self-hosting/troubleshooting.md#the-console-says-404-on-an-old-link)).

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
3. **Open a permitted session's replay from it.** Use **Replay** under **Sessions**.
4. **Check the record is complete.** Put `get_timeline` (or the API's `GET /api/v1/alerts/{id}/timeline`) next to the alert's page. Both should show the same events: the hook's call, the sessions, the `probe_now` result and the notes.

## Next

- [Put an agent on call](on-call.md): where the records the console shows come from.
- [Publish a status page](status-pages.md): the same story, public and one service at a time.
- [Stop an agent, pause everything](stop.md): the pause switch on the Machines page, and the rest of the brakes.
