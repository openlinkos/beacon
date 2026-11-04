# Secrets for agents

By the end of this page an agent of yours can find a credential, be granted it once, and take it whenever the work needs it — each taking on record — and you will have set what it may ask for at all. The page is for the people who run Beacon and for the agents they let in; the design behind it is [The secrets design](../about/secrets.md), every knob is in [Settings](../reference/settings.md), and the levels, entitlements and scopes are also in [the permissions reference](../reference/permissions.md#the-secrets-levels-entitlements-and-actions).

You need Beacon running and an account with `secrets:admin` (an owner by default) to set levels and entitlements; the agent needs one of the secrets scopes and an entitlement.

## What a secret is

Secrets live in **collections** (like 1Password's vaults), which hold **items**; an item has **fields** with types — `concealed`, `string`, `email`, `url`, `otp`, `date`, `file` — and purposes such as `username`, `password` and `notes`. Each change to the values is a new **version**, kept by retention, so a value can be looked at as it was. Items carry **tags**, hierarchical and lower-case (`env/prod`, `service/dns`, `source/1password`), and tags change what permissions mean, so tagging needs `manage`.

An agent names a secret by reference, in the way `op://` does:

```text
beacon://Infra/dns-example-com/password
beacon://Shared/api tokens/example-token/credential
```

The parts are collection, item, optional section, field; names may hold letters, digits, `-_.` and spaces, and anything else is named by id. Field names with spaces are written as they are.

Summaries — title, category, tags, URLs, field names — are readable in the database; the details are encrypted under the collection's key ([the design](../about/secrets.md#encryption-and-signatures)).

## Who may ask for what

Two things decide, and the stricter wins:

| Level | A request for the item takes |
| --- | --- |
| `auto` | Nothing beyond an entitlement: granted on asking |
| `review` | The review chain — rules, a review agent, people, as configured |
| `person` | A person, always |
| `never` | No agent at all; people can still read it in the console |

A level is set on a collection, a tag or the item itself; when it is not, the strictest of the collection's default and the item's tags applies, and an explicit level on the item is a manager's decision that the console marks as an override.

An **entitlement** is what an agent may even ask for. None is granted when an agent arrives: default deny. One names its principals, its resources and its actions:

```yaml
name: oncall-dns
description: The on-call agents may ask for DNS credentials
principals:
  agents: [dns-bot]
  agent_tags: [role/on-call]
  agents_of: [alice]
resources:
  collections: [Infra]
  tags: ["service/dns/*", "provider/example"]
actions: [describe, read]
approval: review
settings:
  request.default_duration: 30d
  request.max_uses: unlimited
reviewers: [rule:dns-on-alert, agent:review-bot, people:operator]
```

`deny` entitlements come before every allow; an entitlement's `approval` is a floor, not a ceiling; and an agent never gets more than its owner may read in that collection. When an agent is made — in the console or at the OAuth consent page — its owner picks from entitlement templates (none, on-call, script, review, custom) and sees what it would reach: *N items: auto X, review Y, person Z*.

## Ask, and be granted

The agent calls `request_secret` with what it wants and why — a reason is required, ten characters at least by default:

```
request_secret(items: ["beacon://Infra/dns-example-com"], actions: [read],
               reason: "renew the DNS record for status.example.com", uses: 3, duration: 30d)
```

A request that spans several levels is split: the `auto` part is granted at once, the rest goes to the review chain, and one answer says which is which. The agent may `wait` inside the call, hear the result on its hook (`secret.granted`, `secret.denied`), or ask `get_request` later. Reviews come from three kinds of reviewer, in the order the settings say — rules built into Beacon (an alerting service matching the item's `service/` tag, a group, a time window, a reason pattern), a review agent outside Beacon woken like an on-call agent, and people answering a card or the console — with how many approvals a level needs, and what happens when a layer times out, all settings. One refusal ends a request, and an applicant never reviews its own.

A **grant** follows: the item, fields, action, a number of uses and a duration — either may be unlimited — the approver, and the reason. Grants live until they are used up, expire, are revoked, or (when bound to an alert) the alert recovers and its grace passes.

## Take a value

`get_secret` returns a field's value against a grant, with a reason each time — two characters at least by default:

```
get_secret(ref: "beacon://Infra/dns-example-com/password", reason: "renewing the record")
```

An `otp` field returns the current code and the seconds left; the seed goes to agents only if the setting says so. The use is written — against the agent, its owner's grant and the context — before the value is returned; if the record cannot be written, no value comes back. Each agent is rate-limited (600 a minute by default), and unusual takings — a first use of a collection, a fivefold jump over the week's average, a run of refusals — raise an alert by default.

`list_grants` lists what the agent holds, `renew_grant` sends it back through the review chain before it expires, and `release_grant` gives it up early. When an item's value changes, holders hear `secret.changed` and take the new version under the same grant; when policy tightens, the settings say what happens to existing grants.

## Using a secret without seeing it

For values that should not pass through an agent's context at all, the `use` action works without revealing the value:

- **`beacon run --reason "…" -- <command>`** reads a `.env` of references and injects them into the child process, like `op run`; the values reach the process, not the conversation.
- **`run_command` with `secrets`** passes values as environment variables into a command on a machine, and masks them — the value and its base64, URL-encoded and hexadecimal forms — in the output, the recording and the transcript. Only on the machines the item is bound to.
- **`http_request`** substitutes a secret reference into a request Beacon itself sends, only to the `https://` hosts the item is bound to, and returns the response masked:

  ```text
  Authorization: Bearer {{secret.beacon://Engineering/api-token/credential}}
  ```
- **File delivery** places an item's value at a path on a machine it names, with owner and mode, and re-delivers it when the value changes; `read_file` sees it masked.

Masking stops accidents, not intent: the command is the agent's own, so these paths are for machines that hold the credential anyway.

## Keeping secrets healthy

Items may carry a rotation period, and Beacon can generate a new value server-side to a specification — as a new version, returning the version number, never the value — so an agent can rotate a credential end to end without seeing it. The console reports weak, reused and ageing passwords, computed locally; the HIBP breach check is off by default because it sends requests outward. Grants nobody has used for a long time are marked, and what follows (mark, notify, release) is a setting.

## The commands

| Command | What it does |
| --- | --- |
| `beacon secret import` | Import from 1Password, Chrome, Bitwarden or CSV ([1Password](../integrations/1password.md), [Chrome](../integrations/chrome.md)) |
| `beacon secret ls` \| `show` \| `set` | List collections and items; show an item's summary; write an item |
| `beacon secret reveal` | Read a value, with the re-authentication window people get |
| `beacon secret tag` \| `level` | Change tags or the level of an item (needs `manage`) |
| `beacon secret archive` \| `rm` | Archive an item (the trash keeps 30 days) or remove it |
| `beacon secret generate` | Make a new value to a specification, as a new version |
| `beacon entitlement add` \| `ls` \| `rm` | Manage entitlements |
| `beacon grant ls` \| `revoke` | List grants, revoke one |
| `beacon run` | Run a command with references from a `.env` injected |
| `beacon settings get` \| `set` \| `history` \| `revert` | Read and change the settings of [Settings](../reference/settings.md) |
| `beacon key rotate` \| `seal` \| `unseal` | Rotate the root key; seal and unseal the vault |
| `beacon backup now` \| `ls` \| `restore` \| `drill` | [Backup and restore](../self-hosting/backup.md) |

`beacon audit verify` checks the audit chain, and `beacon storage check` the database's health ([Command line](../reference/cli.md)).

## What next

- [The secrets design](../about/secrets.md), for what stands behind the levels and the grants.
- [Import from 1Password](../integrations/1password.md) and [Import from Chrome](../integrations/chrome.md), to fill the vault.
- [Settings](../reference/settings.md), for every default this page named.
- [The security model](../self-hosting/security.md), for the threat model and the unsealing of the root key.
