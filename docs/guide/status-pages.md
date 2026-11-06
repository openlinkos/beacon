# Publish a status page

By the end of this page your users have a page that says if your services work, how they did over the last 90 days, what is going wrong and what is being done about it, and what work is planned. Beacon makes it from what it already has — the probes' results, the alert groups, the maintenance windows — so there is nothing to update by hand: when the probes agree a service is down, the page says so, and when the alert resolves, the incident does too. Beacon serves the pages itself, without signing in: the default page at `/` and `/status`, every page at `/status/<name>`, and a page at `/` on a host name of its own if you give it one.

You need Beacon running with at least two probe locations reporting ([Monitor a service](monitoring.md)), because a page shows what the probes agree on, and a credential with `config:write`.

## The page you already have

The reference deployment ships one page, `status`, in `deploy/catalog/status_pages.yaml`: titled "Example Status", with two components, **Website** (the service `example.com`) and **Status and alerts** (`beacon`, Beacon's own status host). Open `https://status.example.com/` after [Up in 30 minutes](../self-hosting/install.md): until the first probe has reported it says **No data yet**; a minute or two after the first results, each component shows its state, and **All systems operational** when both work.

From top to bottom, a page shows:

- **The overall state** in one line: all systems operational, a component down or partly down, maintenance in progress, or no data yet.
- **Active incidents**, with their updates; **maintenance**, ongoing and scheduled.
- **Components**, each with its state now and a bar of the last 90 days, one cell a day: operational, partial outage, outage, maintenance or no data; each cell's title gives the date and the share of the day it was available (on a phone, the last 30 days).
- **The last 30 days**: the incidents and maintenance that ended, each linking to its own page (`/status/<name>/incidents/<id>`); the **probe locations** by their public names; **Sign in** to the console.

The pages are on the family's site design (`@openlinkos/ui`), in English, Chinese or both, readable on a phone, light or dark as the reader's system is, and need no script. Replace the two example components with your own services, or remove the file for no public page.

## Define your own page

A page is part of the catalogue, under `status_pages`, beside the locations, heartbeats and resources: imported from the catalogue's files at the first start and with `beacon config import`, changed through the API, MCP and the command line, checked with the rest of the configuration, and a version in the history that can be reverted.

```yaml
status_pages:
  - name: public                     # lower-case letters, digits and dashes: /status/public
    title: Example Status            # a text is one string, or one per language: {en: …, zh: …}
    description: Whether Example's public services work, from where their users are.
    default: true                    # the page at / and /status (else the first by name)
    host: uptime.example.com         # optional: served at / on this host name (and no longer at Beacon's /)
    brand: beacon                    # the design system's brand it wears
    languages: [en, zh]              # the first is the default; en when left out
    components:
      - name: website
        title: Website
        services: [example.com, www.example.com]   # the service labels of its probe targets
      - name: mirrors
        title: {en: Container mirrors}
        page: mirrors                # every service with a target labelled page: mirrors
    groups:                          # optional headings over components
      - title: Registries
        components: [mirrors]
    alerts: [ServiceDown, ServiceDegraded, MainlandPathDegraded]   # the alerts that are incidents (these by default)
    notes: public                    # which notes are updates: public (the default) or all
    links:
      home: https://example.com/
      docs: https://docs.example.com/
      abuse: mailto:abuse@example.com
      privacy: https://example.com/privacy
```

Or through the API, with a `config:write` token (the MCP tools are `get_config`, `put_config` and `delete_config` with `kind: status_pages`):

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "https://status.example.com/api/v1/config/status_pages/public?note=the+public+page" \
  -d '{"title": "Example Status", "components": [{"name": "website", "title": "Website", "services": ["example.com"]}]}'
```

You will see the new version and the page's entry, and the change echoed to the alert chat. A page is refused unless it passes these checks: names in the right form and unique; a title (at most 120 characters, a description at most 1000); at least one component; every component backed by `services` or a `page` label; groups naming the page's own components, each in one group at most; links that are `https`, `http` or `mailto`; a host name no other page has; at most one default page.

### Components and labels

A component is backed by the probe targets of some services. `services` names them by the targets' `service` label, the one every target has ([Targets](monitoring.md#targets)). `page` names them by another label: a component with `page: mirrors` is backed by every service with a target labelled `page: mirrors`, so that adding a mirror to the targets adds it to the page. A target labelled `visibility: internal` never backs a component through `page`. A component whose services no target has is taken, with a warning (in the change's `warnings` and from `check-config`), such as `status page "public": component "website" names service "www.example.com", which no probe target has; it shows no data until one does`: targets are often added after the page that shows them.

Every text (`title`, `description`, a group's or a link's `title`) is one string for every language, or one per language: `{en: Status, zh: …}`. A page in two languages has a language menu in its header, and its pages and links take `?lang=zh`. Without `?lang=`, readers see the page in the first of its languages their browser asks for (`Accept-Language`), and in its first language when the browser asks for neither, as search engines usually do; the JSON, the feed and the badges, which programs read, are in the first language unless `?lang=` says otherwise. A `menu` gives the page the family menu in its header and the columns of its footer, as the family's other sites have it. Every field of a page is in the catalogue reference: [The catalogue](../reference/catalogue.md).

## What the page says, and why

A component's state now is the worst of:

- **maintenance**, while a maintenance window covers one of its services;
- the **incidents** firing on its services: `ServiceDown` is an outage, the other alerts a partial outage;
- the **probes' agreement** in the last minutes, by the reference rules' logic (`deploy/rules/probes.yml`): a location fails a service once it has failed it for 3 minutes; the service is down when at least two locations fail it and they are at least half of those reporting, and partly down when at least two fail but fewer than half. One location's hiccup is never shown.

So a page never says a service works while an alert about it fires, and it shows a failure the probes agree on a minute or two before the alert does, since the alert waits a minute to be sure. A component of several services takes the worst of them. Nothing reporting is **no data**: no probe has sent a result for the component's services in the last minutes — the causes and what to do are in [A status page shows no data](../self-hosting/troubleshooting.md#a-status-page-shows-no-data). Each day of the bar is the same agreement minute by minute, with the incidents and the maintenance windows laid over it; availability is the share of the minutes with results, outside maintenance, that were not down. Days begin at midnight in the configuration's `timezone`; Beacon computes the last 90 days at the start, today again every minute, and a past day every 10 minutes until six hours after it ended, for results that probes queued while their path was down.

## Incidents and updates

An incident is an alert group, critical or warning, on the page's services, whose alert is one the page counts (`alerts`; by default `ServiceDown`, `ServiceDegraded` and `MainlandPathDegraded`, the availability alerts of the reference rules, so that an expiring certificate is not a public incident). Its title is made by Beacon in the page's language from its impact and the components it touches, such as "Outage: Website"; the alert's own text, which may name internal things, is never shown. Beacon keeps a copy of each incident for 91 days, so an incident and its page outlive the alert group (resolved groups go after 7 days).

Its updates, the newest first: **Detected**, when it started; **Investigating**, when someone acknowledged it; the **notes** that people and agents leave on the alert group; **Resolved**, when the alert resolved. Notes are written for the people working the alert, so by default only those that begin with `public:` are published, without the prefix; a page with `notes: all` publishes every note. To tell your users what is happening, leave such a note from the console, with `add_note`, or with an `alerts:write` token:

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/alerts/0cdfbada62dd/notes \
  -d '{"text": "public: The site is back for most users; we are watching the last region."}'
```

## Maintenance

A maintenance window is planned work on some services: the pages announce it ahead of time, show its components in maintenance while it lasts, and mark those days. Plan one with `POST /api/v1/maintenance` or the MCP tool `schedule_maintenance` (scope `maintenance:write`); end it early, or call it off, with `DELETE /api/v1/maintenance/{id}` or `end_maintenance`:

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/maintenance \
  -d '{"services": ["example.com"], "starts_at": "2026-11-03T02:00:00Z", "ends_at": "2026-11-03T03:00:00Z",
       "title": "Database upgrade", "description": "The site is read-only for a few minutes."}'
```

A window lasts at most 7 days and starts within 90; a start in the past is now, so that a window never covers what already happened. For each service it makes a silence for the window with the comment `maintenance: <title>`, so that what the work breaks pages nobody; ending the window ends them. A silence made another way (`POST /api/v1/silences`, `beacon silence create`) whose comment begins with `maintenance:` is shown as maintenance too. Planning and ending are recorded in the audit log and echoed to the alert chat.

## A host of its own

A page with `host` is also served at `/` on that host name, with its JSON at `/status.json`, its feed at `/feed.atom` and its badges at `/badge.svg`. Nothing else of Beacon answers on that host: not the console, the API or MCP, so the page has no **Sign in** there. The page then lives on its host: when it is the default page, Beacon's own root is no longer the page but the landing page, which leads to each status page Beacon serves and into the console, so Beacon's host name is the console's and the page's host name is the page's. The page stays under `/status/<name>` on Beacon's host as well, naming its own host as its canonical address, and its JSON, feed and badges keep answering there for links already given out. In the reference deployment, uncomment the block at the end of `deploy/Caddyfile`, with your name in place of `uptime.example.com`, and restart Caddy so that it reads the file again (`docker compose restart caddy`, in `/opt/beacon`); Caddy gets the name's certificate as for the other hosts, and Beacon serves the page by the request's `Host` header, so the proxy must not rewrite it:

```
uptime.example.com {
	import common
	reverse_proxy beacon:17001 {
		header_up Host {host}
		header_up X-Forwarded-For {client_ip}
	}
}
```

## JSON, feed and badges

| Path | What |
| --- | --- |
| `/status/<name>.json` (`/status.json` for the default page) | The page's state: components with their state and 90 days of history, the incidents with their updates, the maintenance windows, the links. Its shape is stable: fields are added, never changed or removed |
| `/status/<name>/feed.atom` | The incidents and maintenance windows as an Atom feed |
| `/status/<name>/badge.svg` and `/status/<name>/components/<component>/badge.svg` | A badge with the page's, or one component's, state, in the design system's colours |

They take `?lang=` as the page does, and may be embedded and fetched from other sites (`Access-Control-Allow-Origin: *`). An agent gets the same JSON with `GET /api/v1/status/<name>` (`status:read`) or the MCP tool `get_status_page`; it names no service label and no token: what is public is the page's own text.

## Branding

Every public page says what it runs on: the footer has the line **Powered by Beacon, open source**, linking to [beacon.openlinkos.com](https://beacon.openlinkos.com/) and to the code on [GitHub](https://github.com/openlinkos/beacon). A team that wants its status pages to carry only its own name may leave the line out:

```yaml
branding:
  title: Example Operations   # the page at / when there is no status page (Beacon when left out)
  powered_by: false           # no "Powered by Beacon" on the public pages (true by default)
```

`powered_by` is for the public pages only; the console always shows Beacon's version and the project's links ([Review what agents did](console.md)). A page is computed at most every 30 seconds, and every answer is public and cacheable (`Cache-Control: public, max-age=30` and an `ETag`), so a browser or an edge cache asks again cheaply and gets `304 Not Modified`; one asked for by someone signed in is `private, max-age=30`, so no shared cache keeps it. Unlike the console's pages, status pages are not marked `noindex`.

## Check it

After the first probes report, open the page and read one component's bar: each cell of the last days should be operational, with the share of the day it was available in its title. Then add the drill's service to your page — a component with `services: [drill]`, which the shipped page does not carry — and [rehearse an outage](drills.md): while the drill's alert fires, the component says **Outage** with the incident under it; leave a note beginning `public:` on the alert group and watch it appear as the incident's update; end the drill and watch both go, then remove the component. If a component says **No data yet**, that is [A status page shows no data](../self-hosting/troubleshooting.md#a-status-page-shows-no-data).

## Next

- [Rehearse an outage](drills.md): see the page change before your users do, on Beacon's own drill service.
- [Invite your team](people.md): the people and the roles behind the page's updates.
- [Review what agents did](console.md): the same incidents, with the sessions and notes behind them, for the team.
