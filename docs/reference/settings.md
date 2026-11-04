# Settings

Every value Beacon works by that can be tuned is a setting: held in the database, shipped with a default that works out of the box, versioned with who changed it, when and why, and revertible. This page lists them all with their defaults. What a `*_file` secret or a port is — the things needed before the database opens — stays in [config.yaml](config.md); the backup and storage values are on their own pages, listed last.

A build check keeps this page honest: every setting named here must appear, in both languages, or the site does not build.

## Where settings live

In the database, not in a file: they change at runtime, take effect at once, and every change is one version with an author, a time, a reason and the values before and after — the same treatment the [catalogue](catalogue.md) gets. Changing a setting needs `secrets:admin`; an owner holds it, and it can be given to an agent, whose changes are recorded like anyone's. In the console every setting shows its current value, its default, which level overrides it, and who changed it and when.

`beacon settings get KEY`, `beacon settings set KEY VALUE -reason "…"`, `beacon settings history KEY` and `beacon settings revert KEY -to N` do the same from the command line; `get_config` and `put_config` (with the `settings` kind) do it over REST and MCP. Each has an audit entry and an echo to the alert chat.

## How they override

`system → collection → tag → entitlement → request`: the more specific level wins, and a request may only choose within the limits the level above set. Item levels do not ride this chain — they take the strictest, separately ([permissions](permissions.md#the-secrets-levels-entitlements-and-actions)). When one item matches several tags that each override the same setting, the stricter value wins: the smaller of counts and durations, the more conservative of consequences — in order, deny, expire, next, wait, approve.

## Requests and grants

| Key | Default | Choices and notes |
| --- | --- | --- |
| `request.default_uses` | `unlimited` | A number, or `unlimited`, when a request names none |
| `request.max_uses` | `unlimited` | The ceiling a request may ask within |
| `request.default_duration` | `auto`: `never`; `review`: `30d`; `person`: `7d` | A duration, or `never` |
| `request.max_duration` | `unlimited` | The ceiling |
| `request.reason_min_length` | `10` | `0` asks for no reason |
| `use.reason` | `required`, minimum `2` | `required` / `optional`; when optional, the context is recorded anyway |
| `request.pending_ttl` | `24h` | How long a request may wait unhandled |
| `request.on_pending_expiry` | `expire` | `expire` / `deny` / `approve` |
| `grant.renewal` | `auto` at level `auto`; `request` otherwise | `auto` / `request` / `none` |
| `grant.expiry_notice` | `3d` before expiry | Grants shorter than a day get no reminder |
| `grant.on_policy_tightened` | `revoke` | `revoke` / `keep_until_expiry` / `notify`; loosening affects no grant |
| `grant.on_owner_access_lost` | `revoke` | The same choices |
| `grant.on_item_changed` | `notify_holders` | `notify_holders` / `none`; a new version needs no new grant |
| `grant.alert_grace` | `15m` | How long a grant bound to an alert still works after it recovers |
| `agents.default_entitlements` | *(empty)* | Entitlement templates an agent gets when nothing is chosen |

## Review

| Key | Default | Choices and notes |
| --- | --- | --- |
| `review.chain` | `[rules, agents, people]` | Any composition and order of the three |
| `review.agent.may_approve` | `[review]` | The levels a review agent may approve; `person` is people's by definition |
| `review.agent.max_uses` | `unlimited` | What a review agent may give |
| `review.agent.max_duration` | `unlimited` | The same |
| `review.agent.timeout` | `10m` | |
| `review.agent.on_timeout` | `next` | `next` / `deny` / `approve` / `wait`; the console warns about `approve` |
| `review.people.timeout` | `24h` | |
| `review.people.on_timeout` | `expire` | `expire` / `deny` / `approve` |
| `review.approvals_required` | `1` | Per level; `2` needs two, a review agent and a person, say |
| `review.separation` | `requester` | `requester` / `owner`; `owner` also bars a reviewer owned by the applicant's owner |
| `review.notify_decisions` | the alert chat | Where a review agent's decisions are pushed |

## Uses, rate limits and anomalies

| Key | Default | Choices and notes |
| --- | --- | --- |
| `rate.per_agent_per_minute` | `600` | Uses, per agent, per minute |
| `rate.on_exceed` | `throttle` | `throttle` / `alert` |
| `anomaly.rules` | the five below, all on, notifying | A first use of a collection; a fivefold jump over the 7-day average; a run of refusals; a failed signature; a sealed vault. Each can be off or tuned |
| `anomaly.on_detect` | `notify` | `notify` / `suspend_agent` |
| `otp.seed_to_agents` | `false` | Whether `get_secret` on an `otp` field hands over the seed, not just the code |
| `reveal.reauth_window` | `5m` | How long after a password check a person may keep reading values |
| `scrub.encodings` | the value, base64, URL-encoded, hexadecimal | Which encodings of a value are masked in outputs |
| `http.allow_private` | `false` | Whether `http_request` may reach private addresses |
| `http.follow_redirects` | `same_host` | `none` / `same_host` / `any_bound` |
| `file.on_change` | `deliver` | `deliver` / `next_window` / `notify` — when a delivered file follows a changed value |
| `sealed.on_request` | `refuse` | `refuse` (answering `secrets.sealed`) / `queue` |

## Retention

| Key | Default | Choices and notes |
| --- | --- | --- |
| `retention.audit` | `365d` | Per event type, if wanted |
| `retention.uses` | `365d` | |
| `retention.requests` | `365d` | |
| `retention.item_versions` | `365d`, and always the newest `10` | |
| `retention.trash` | `30d` | |
| `audit.checkpoint_interval` | `24h` | How often the chain's signed checkpoint is written |
| `audit.checkpoint_publish` | into every backup; the chat as well when configured | |
| `audit.export` | *(none)* | `syslog` / `s3`, as JSONL |

## Import

| Key | Default | Choices and notes |
| --- | --- | --- |
| `import.levels.by_source` | 1Password `Personal` and `Private` → `person`; Chrome → `person`; every other vault → `review` | Changeable per collection in the dry run |
| `import.levels.by_category` | identity and money categories → `never` | [Imports](../integrations/1password.md) list them |
| `import.on_conflict` | `ask` | `ask` / `new_version` / `skip` / `keep_both` |
| `attachments.max_size` | `10MB` | |

## Backup and storage

The backup's schedule, retention, verification and destinations are settings of the same kind, listed with their defaults on [Backup and restore](../self-hosting/backup.md); the database driver and its file, the unsealing of the root key and the backup recipients' public keys are read before the database opens, so they live in [config.yaml](config.md) and in the secrets directory.

Metrics: `beacon_secret_uses_total{collection,agent,action}`, `beacon_secret_requests_total{decision,decider_kind}`, `beacon_secret_grants_active`, `beacon_secret_grant_expiring` ([metrics](metrics.md)).
