# The secrets design

Why Beacon keeps the credentials agents work with, and the shape of that part: what it borrows from three mature problems, what is a setting, and the four things that never are. This page is the why; [Secrets for agents](../guide/secrets.md) is the how, [Settings](../reference/settings.md) lists every knob with its default, and the [security model](../self-hosting/security.md) carries the threat model.

An agent that runs operations holds, sooner or later, the credentials of the services it works on: API tokens, database passwords, DNS keys. Copying them into the agent's context is how they end up in a transcript, a prompt or a repository. Beacon keeps them instead: the agent asks once, a grant is made — by a rule, by a review agent or by a person — and from then on the agent takes the value as often as the grant allows, every use on record.

## Three problems, three mature answers

The design does not invent; it combines what three solved problems each know, and adds one thing of its own.

| The problem | The mature answer | What Beacon takes from it |
| --- | --- | --- |
| Keeping credentials where an unattended holder can be handed a value | Server-hosted secret stores: Vault, Infisical, 1Password Connect — the server holds keys that decrypt | The data model: a key hierarchy, per-collection keys, summaries in the clear for search and policy, values encrypted. The server must be able to decrypt, or nobody could hand an agent a value at three in the morning; "the server knows nothing" is a different product |
| Just-in-time privileged access for a requester who is software | GCP Privileged Access Manager, Entra PIM, Teleport Access Requests | Entitlements (who may ask for what), grants that name their item, action, uses, duration and reason, no self-approval, approval thresholds, reasons required |
| State and backup for a self-hosted control plane | Teleport, Gitea, Grafana: SQLite by default and a bigger database when needed; age-encrypted backups; signed audit digests | The storage layer, the backup design and the audit chain ([Architecture](architecture.md), [Backup and restore](../self-hosting/backup.md)) |

What Beacon adds is small and deliberate: the applicant and the reviewer can both be agents. A person still decides what agents may ask for; whether a particular request is sound at three in the morning can be a rule's, another agent's or a person's call, and which of them — that is a setting.

## One grant, many uses, every use on record

The core is [goal 2](goals.md#product-goals-three-pillars): one authorization, many uses, traceable all the way back.

- A **grant** names its item, fields, action, number of uses and duration — either may be unlimited — and the reason it was given. An agent may hold a grant for as long as the approver allows, and renew it.
- Each **use** within the grant disturbs nobody: no card, no waiting. But each one carries a reason, and lands in the record against the agent, the person it acts for, the grant and the context — the alert, the session — before the value is returned.
- What the agent does with a value it has read is its own doing; a grant limits how much it can take and says who took it and why. Where that is not enough, an item can be restricted to [use without seeing the value](../guide/secrets.md#using-a-secret-without-seeing-it).

## Two axes: the level and the entitlement

Two questions, asked separately, answered together:

- **The level of an item** — `auto`, `review`, `person` or `never` — says what a request for it takes: nothing more than entitlement, a review, a person, or no agent at all. Levels live on collections, tags and items, and the strictest wins.
- **The entitlement of an agent** — by collection, tag or item, with the actions `describe`, `read` and `use` — says what it may even ask for. An agent with no entitlement does not see the item at all; `deny` entitlements come first; an agent never gets past its owner's permissions.

The approval a request needs is the stricter of the two. Default deny, everywhere; a request may also be split by level, so the `auto` part is usable at once while the rest waits.

## Everything is a setting

Defaults that work out of the box, and every one of them a setting in the database with a version, an author, a reason and a revert: how long a request may wait, what happens when a reviewer does not answer, how many approvals a level needs, rate limits, what counts as an anomaly, retention, import rules. The system sets the floor, and a collection, a tag, an entitlement or a request may tighten it. What behaviour is right at three in the morning is not for the software to guess; it is written down, and it can be changed with a record of who changed it.

## Encryption and signatures

A root key wraps one key per collection; item details — every field value, username, note, one-time password seed, attachment — are encrypted under the collection's key, bound to the item, its version and its collection. Summaries — title, category, tags, URLs, field names — stay readable, because policy and search need them; someone holding only the database knows which secrets exist, not what they say.

The root key comes from a file by default; it can be split into a passphrase plus a file, or wrapped by a KMS, so that the whole disk is not enough to open the vault ([security model](../self-hosting/security.md#unsealing-the-root-key)).

Policy rows — entitlements, grants, agent tags and settings — are signed. A row whose signature does not verify is treated as though it did not exist, and alarms: someone editing the database directly cannot widen a permission. The secrets brake is sealing the vault (`beacon key seal`); uses and reveals then wait (`ErrSealed`), while monitoring and the SSH channel carry on. The SSH channel's pause switch stays on the channel.

## The audit chain

Every event — items created, changed, archived, imported; requests decided; grants made, used, renewed, revoked; keys rotated, the vault sealed and unsealed; backups taken and restored — is one record: who, for whom, what, why, from where. The records form a hash chain, and a signed checkpoint lands in the database and every backup on a schedule. Records are pruned by retention, checkpoints stay, and what remains still verifies. A use is written before the value is returned; if the record cannot be written, the value is not returned.

## The four invariants

Four things are not settings, because everything else stands on them:

1. The use record is committed before the value is returned. Without it there is no traceability.
2. An applicant never approves its own request. Without it there is no approval.
3. A policy row whose signature fails is treated as absent, and alarms. Without it one database edit bypasses everything.
4. A `never` item is given to no agent. That is what the level means; to let an agent use it, change the level.

## What it borrows

Public designs, named: from 1Password, the two-key derivation, per-vault keys and the summary/details split, and the `op://` reference syntax that `beacon://` mirrors; from GCP PAM and Teleport, entitlements, required reasons, thresholds and the refusal of self-approval; from Vault, default deny, `deny` first, and use-counted tokens; from CloudTrail, periodic signed digests over a log chain; from restic, grandfather-father-son retention; from age, encryption to public keys so the writer cannot read what it wrote.

## What it deliberately is not

The server can decrypt, so a compromised centre is a compromised vault — no better and no worse than the SSH authority it already is ([threat model](../self-hosting/security.md#the-threat-model)). Summaries are in the clear. An agent that reads a value can leak it; a grant bounds what it holds and puts its name on every taking, and the stricter path is to use a value without seeing it. Masking in transcripts stops accidents, not intent. And Beacon calls no model: the review agent is outside, woken like an on-call agent, and what it may approve is bounded by settings.
