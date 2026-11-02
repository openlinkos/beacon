# Status pages

Beacon serves public status pages itself, from what it already has: the probes' results, the alert groups and the maintenance windows. A page says whether each of its components works now, how it did on each of the last 90 days, what is going wrong and what is being done about it, and what work is planned. There is nothing to feed by hand and no third-party status page to keep in step: when the probes agree a service is down, the page says so, and when the alert resolves, the incident does too.

The pages are on the family's site design (`@openlinkos/ui`), in English, Chinese or both, readable on a phone, light or dark as the reader's system is, and need no script. They are served on port 17001 beside the console, without signing in: the default page at the site's root `/` and at `/status`, every page at `/status/<name>`, and a page at `/` on a host name of its own.

## What a page shows

- **The overall state** in one line: all systems operational, a component down or partly down, maintenance in progress, or no data yet.
- **Active incidents**, with their updates.
- **Maintenance**, ongoing and scheduled, announced ahead of time.
- **Components**, each with its state now and a bar of the last 90 days, one cell a day: operational, partial outage, outage, maintenance or no data; each cell's title gives the date and the share of the day it was available, and the bar its availability over the 90 days. On a phone the bar shows the last 30 days.
- **The last 30 days**: the incidents and maintenance that ended, each linking to its own page (`/status/<name>/incidents/<id>`).
- **The probe locations**, by their public names (the catalogue's `name` and `network`), and the links the page names: home, documentation, where to report abuse, the privacy notice; the family menu in the header and the footer.
- **Sign in** at the right of the header, to the console at `/console` (**Console** for someone signed in), and in the footer the line **Powered by Beacon, open source** ([Branding](#branding)).

## Defining a page

A page is part of the catalogue, under `status_pages`, beside the locations, heartbeats and resources ([Catalogue, targets and modules](catalog.md)). Like them it is imported from the catalogue's files at the first start and with `beacon config import`, changed through the API, MCP and the command line, checked with the rest of the configuration, and a version in the history that can be reverted:

```yaml
status_pages:
  - name: public                     # lower-case letters, digits and dashes: /status/public
    title: Example Status            # a text is one string, or one per language: {en: …, zh: …}
    description: Whether Example's public services work, from where their users are.
    default: true                    # the page at / and /status (else the first by name)
    host: uptime.example.com         # optional: served at / on this host name too
    brand: beacon                    # the design system's brand it wears: beacon, linkos, linkos-status, linkos-mirrors, openlinkos
    languages: [en, zh]              # the first is the default; en when left out
    components:
      - name: website
        title: Website
        description: example.com and www.example.com
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
    menu:                            # the family menu in the header, and columns in the footer
      title: Example
      groups:
        - links: [{title: Example home, url: "https://example.com/"}]
        - title: Free public services
          links: [{title: Example Mirrors, url: "https://mirrors.example.com/"}]
```

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "https://status.example.com/api/v1/config/status_pages/public?note=the+public+page" \
  -d '{"title": "Example Status", "components": [{"name": "website", "title": "Website", "services": ["example.com"]}]}'
```

The MCP tools are `get_config`, `put_config` and `delete_config` with `kind: status_pages`. A page's export is `catalog/status_pages.yaml`. Its checks: names in the right form and unique, a title, at least one component, every component backed by `services` or a `page` label, groups naming its own components, links that are `https`, `http` or `mailto`, a host name no other page has, at most one default page.

### Components and labels

A component is backed by the probe targets of some services. `services` names them by the targets' `service` label, the one every target has ([Targets](catalog.md#targets)). `page` names them by another label on the targets: a component with `page: mirrors` is backed by every service with a target labelled `page: mirrors`, so that adding a mirror to the targets adds it to the page. A target labelled `visibility: internal` never backs a component through `page`.

A component whose services no target has is taken, with a warning (in the change's `warnings` and from `check-config`): targets are often added after the page that shows them. Until one is, it shows no data.

### Groups, languages and links

Every text (`title`, `description`, a group's or a link's `title`) is one string for every language, or one per language: `{en: Status, zh: …}`. A page in two languages has a language menu in its header, and its pages and links take `?lang=zh`; the default language needs none. The family menu (`menu`) is the site header's menu of the family, as the family's other sites have it: a button with groups of links, such as the organisation's home, its open-source projects and its free public services. Its titled groups are also the footer's columns. `links` are the footer's links.

## The state of a component

A component's state now is the worst of:

- **maintenance**, while a maintenance window covers one of its services;
- the **incidents** firing on its services: `ServiceDown` is an outage, the other alerts a partial outage;
- the **probes' agreement** in the last minutes, by the reference rules' logic (`deploy/rules/probes.yml`): a location fails a service once it has failed it for 3 minutes; the service is down when at least two locations fail it and they are at least half of those reporting, and partly down when at least two fail but fewer than half. One location's hiccup is never shown.

So a page never says a service works while an alert about it fires, and it shows a failure the probes agree on a minute or two before the alert does, since the alert waits a minute to be sure. Nothing reporting is no data. A component of several services takes the worst of them.

## History

Each day of the bar is the probes' agreement minute by minute, evaluated by VictoriaMetrics on the probes' results with the rules' logic, so it needs no recording rules; then the incidents are laid over it (a minute an incident covered is at least what the incident says), and the maintenance windows (a minute in maintenance counts neither for nor against).

| A day is | When |
| --- | --- |
| an outage | the probes agreed it was down for at least two minutes, or an outage incident touched it |
| a partial outage | it was down or partly down for at least two minutes, or a partial outage incident touched it |
| maintenance | a maintenance window covered part of it, and it was not down outside the window |
| operational | there were results, and none of the above |
| no data | nothing reported |

Availability is the share of the minutes with results, outside maintenance, that were not down; a partial outage counts as available. Days begin at midnight in the configuration's `timezone`. Beacon keeps the days it has computed in memory and computes the last 90 at the start, one query a day for every page's services; today is computed again every minute, a past day again until six hours after it ended, for results that probes queued while their path was down.

## Incidents

An incident is an alert group, critical or warning, on the page's services, whose alert is one the page counts (`alerts`; by default `ServiceDown`, `ServiceDegraded` and `MainlandPathDegraded`, the availability alerts of the reference rules, so that an expiring certificate is not a public incident). Its title is made by Beacon in the page's language from its impact and the components it touches, such as "Outage: Website"; the alert's own text, which may name internal things, is never shown.

Its updates, the newest first:

- **Detected**, when it started: the probes see it failing from at least half of their locations, or from some;
- **Investigating**, when someone acknowledged it;
- **notes**, which people and agents leave on the alert group (`add_note`, `POST /api/v1/alerts/{id}/notes`, the console). Notes are written for the people working the alert, so by default only those that begin with `public:` are published, without the prefix; a page with `notes: all` publishes every note. A note left through the API or MCP is marked as an agent's, one left in the console or on a card as a person's; no token's or person's name is shown;
- **Resolved**, when the alert resolved.

Beacon keeps a copy of each incident for 91 days, so an incident and its page outlive the alert group (resolved groups go after 7 days).

## Maintenance

A maintenance window is planned work on some services: the pages announce it ahead of time, show its components in maintenance while it lasts, and mark those days. Plan one with `POST /api/v1/maintenance` or the MCP tool `schedule_maintenance` (scope `maintenance:write`); end it early, or call it off, with `DELETE /api/v1/maintenance/{id}` or `end_maintenance`:

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/maintenance \
  -d '{"services": ["example.com"], "starts_at": "2026-11-03T02:00:00Z", "ends_at": "2026-11-03T03:00:00Z",
       "title": "Database upgrade", "description": "The site is read-only for a few minutes."}'
```

A window lasts at most 7 days and starts within 90; a start in the past is now, so that a window never covers what already happened. For each service it makes a silence for the window with the comment `maintenance: <title>`, so that what the work breaks pages nobody; ending the window ends them. Planning and ending are recorded in the audit log and echoed to the alert chat.

A silence made another way (`POST /api/v1/silences`, `beacon silence create`) whose comment begins with `maintenance:` is shown as maintenance too, on the components whose services its `service` matchers match, while it lasts and for as long as Beacon keeps it.

## A host of its own

A page with `host` is also served at `/` on that host name, with its incidents at `/incidents/<id>`, its JSON at `/status.json`, its feed at `/feed.atom` and its badges at `/badge.svg` and `/components/<name>/badge.svg`. Nothing else of Beacon answers on that host: not the console, the API or MCP, so the page has no **Sign in** there. Point the name at the reverse proxy and pass the host through; in the reference deployment, a block in `deploy/Caddyfile`:

```
uptime.example.com {
	import common
	reverse_proxy beacon:17001 {
		header_up Host {host}
		header_up X-Forwarded-For {client_ip}
	}
}
```

Caddy gets the name's certificate as for the other hosts. Beacon serves the page by the request's `Host` header, so the proxy must not rewrite it.

## JSON, feed and badges

| Path | What |
| --- | --- |
| `/status/<name>.json` (`/status.json` for the default page) | The page's state: components with their state and 90 days of history, the incidents (active, then the last 30 days) with their updates, the maintenance windows, the links. Its shape is stable: fields are added, never changed or removed |
| `/status/<name>/feed.atom` | The incidents and maintenance windows as an Atom feed, each incident with its updates |
| `/status/<name>/badge.svg` | A small badge with the page's state: operational, partial outage, outage, maintenance or no data, in the design system's colours |
| `/status/<name>/components/<component>/badge.svg` | The same for one component |

They take `?lang=` as the page does, and may be embedded and fetched from other sites (`Access-Control-Allow-Origin: *`). An agent gets the same JSON with `GET /api/v1/status/<name>` (`status:read`) or the MCP tool `get_status_page`. It names no service label and no token: what is public is the page's own text.

## Branding

Every public page says what it runs on: the footer has the line **Powered by Beacon, open source**, with Beacon's mark, linking to [beacon.openlinkos.com](https://beacon.openlinkos.com/) (its Chinese pages for a page in Chinese) and to the code on [GitHub](https://github.com/openlinkos/beacon). A team that wants its status pages to carry only its own name may leave the line out:

```yaml
branding:
  title: Example Operations   # the page at / when there is no status page (Beacon when left out)
  powered_by: false           # no "Powered by Beacon" on the public pages (true by default)
```

`powered_by` is for the public pages only; the console always shows Beacon's version and the project's links ([The console](pages.md)).

Without any status page, `/` is a small page on the same design: the deployment's title (`branding.title`), one line on what Beacon is, **Sign in**, and the footer's line.

## Caching and search engines

A page is computed at most every 30 seconds, and every answer is public and cacheable: `Cache-Control: public, max-age=30` and an `ETag`, so a browser or an edge cache asks again cheaply and gets `304 Not Modified`. A page asked for by someone signed in to the console, whose header says **Console** rather than **Sign in**, is `private, max-age=30`, so that no shared cache keeps it; both lead to `/console`, so a cached page that says **Sign in** does no harm. Unlike the console's pages, status pages are not marked `noindex`; `/robots.txt` lets search engines read `/`, `/status` and the files they load, and keeps them out of `/console`, `/api/`, `/mcp` and the old paths of the console's links (`/alerts/`, `/sessions/`); on a page's own host it allows everything. They have the console's `Content-Security-Policy` without any script, send no referrer, and cannot be framed.
