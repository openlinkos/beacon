# The pages

From the card in the chat, one link opens the alert's page: what the probes see, the timeline, the notes, the agents woken, and every session on the alert's machines, each with its commands and a replay. Beacon serves these pages itself, beside the API on port 17001, at `public_url`. There is no login system: a page opens with a signed link, or with a bearer token.

## The alert's page: `/alerts/{id}`

The card's **Timeline** button opens it. It shows:

- the alert: title, status, severity, since when (or when it resolved and after how long), description, service, labels, the runbook and the dashboard;
- what people were told and did: acknowledged, silenced, buzzed, paged;
- **Probes**: for the alert's service, each location's latest result and reason (what `get_evidence` returns): OK, failing since when, stale, the failure reason, the duration, the HTTP status, the attempts, the certificate's expiry;
- **Related alerts**: others firing in the same failure domain or for the same service, each linking to its own page;
- **Agents**: whether agents reach machines or the channel is paused and since when; which hooks were woken and when; one line "N sessions on M machines, K commands";
- **Sessions**: every session in the alert's window on the machines its labels name, or by the agents that acknowledged it or left notes: who, how, where, what, the exit status, and for an interactive shell each command with its exit status and duration, each session with a **Replay** link;
- **Notes**, **Changes** (the change events around the alert), **Labels**;
- **Timeline**: everything above in time order, as `get_timeline` returns it.

The window opens 30 minutes before the alert started and closes now, or 30 minutes after it resolved, as for the evidence.

## The session's page: `/sessions/{id}`

Who, on which machine, through what (ssh, MCP or the API), what kind of session, when it started and how long it took, the exit status, and the recording replayed in the page with [asciinema-player](https://github.com/asciinema/asciinema-player) (asciicast v3, as the SSH channel records: output, input, resizes, a marker at each command, the exit status). Below the player, an interactive shell's commands with their exit statuses, or a command's output. The recording can also be downloaded. A session still open shows its record; the recording plays once it ends.

## Signed links

A link to a page ends in `?s=<expiry>.<signature>`: the expiry as Unix seconds, and the base64url HMAC-SHA256 of the method, the path and the expiry (each followed by a newline), under a 32-byte key Beacon makes at its first start and keeps in its database. Links are valid for `pages.link_ttl` (30 days by default):

```yaml
public_url: https://beacon.example.com   # where the pages are; without it there are no links
pages:
  link_ttl: 720h
```

A link grants exactly one page: the alert's page, or the session's page with its recording, until it expires. The links inside a page (to sessions, to the recording) last as long as the link that opened it. A link for another path, another method or a changed expiry fails, and so does an expired one; every failure is a plain 404, the same as for a page that does not exist, so a wrong link reveals nothing. **Anyone holding a link can see that page, recording included, without signing in anywhere**: the chat the links go to must be private, and a link should not be forwarded further than the recording may travel. There is no command yet to revoke links before they expire (that would mean replacing the key); keep `link_ttl` as short as the chat's habits allow.

Beacon hands out the links wherever a person might pick one up:

- the card: the **Timeline** button and the agents' block;
- the chat echoes of what agents did on machines: a **Replay** link per session;
- the API and MCP answers, so that an agent can hand a person a link: `page` on an alert (`GET /api/v1/alerts/{id}`, `get_alert`), `links.page` in the evidence, `url` on a timeline's session events, `page` on a session (`list_sessions`, `get_session`, `done.sessions[]` in the evidence).

## Tokens instead

A bearer token opens the pages as it opens the matching API routes: `status:read` for an alert's page, `sessions:read` or `machines:ssh` for a session's page and its recording. Send it as `Authorization: Bearer lbt_…`; a page opened this way links to its sessions and recording for an hour. A token without the scope, an unknown token and no token all get the same 404.

## What the pages do not do

- No login, no accounts, no sessions of their own: links and tokens are the whole of it.
- No actions: acknowledging, silencing and pausing are done on the card, through the API and MCP, or on the command line. People see; agents do.
- Nothing is fetched from anywhere but Beacon: the player's JavaScript and CSS are embedded in the binary (`internal/web/static`, with its licence and the hashes in `NOTICE`), the pages carry no inline script, and their `Content-Security-Policy` allows scripts and styles from Beacon only (`'wasm-unsafe-eval'` because the player's terminal is WebAssembly). Pages are marked `noindex` and send no referrer, since their links carry signatures.
- The pages are in the configured `language`, follow the system's light or dark scheme, and are laid out for a phone, where people open them from the chat.
