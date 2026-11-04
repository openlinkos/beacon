# Security model

Beacon holds what agents never see: the keys that let them into machines, the tokens they sign in with, the credentials of the services they work on, and the record of every command. This page states the model in one place — what Beacon guarantees, what it refuses to do, and the few things the operator carries — and links to the pages that tell each part in full.

## Who may do what

- **People sign in with Beacon's own accounts.** Usernames and passwords, argon2id hashes, roles (viewer, on call, operator, owner); there is no other way in and no outside identity provider. [People and their agents](../guide/people.md#roles).
- **An agent acts for one person, and never beyond them.** It signs in by MCP's OAuth or a scoped token, and what it may do — its preset within its owner's role — is decided afresh on every request, so a demoted owner narrows its agents at once. [Security](../guide/people.md#security).
- **A machine's token writes only as its own subject**, and an expired machine cannot write; the rule engine's alerts need the rules token. Tokens are stored as SHA-256 hashes, carry scopes and expire, and failed attempts are limited per address.
- **Every write is on record.** Each change through the API, MCP, a card or the command line lands in the audit log, echoed to the alert chat ([Who the audit log names](../guide/people.md#who-the-audit-log-names)).

## The SSH channel

The channel's promise is that an agent reaches a machine without ever holding a key: it signs in to Beacon with its own key, machines trust only Beacon's certificate authority, and every connection gets a certificate valid for five minutes that names the account on the machine. Only the machines an agent may reach are connected to; when access ends, what it has open closes, checked again every minute; each machine's host key is pinned; every session is recorded, with a replay. [What the channel guarantees, and what it does not](../guide/machines.md#what-the-channel-guarantees-and-what-it-does-not).

Equally deliberate is what it does not do: it reviews no command and runs nothing on its own — deciding what to run is the agent's job, done by its own harness — and it passes on no agent forwarding, no X11, no remote port forwarding for agents. The brakes are the pause switch ([Pausing](../guide/machines.md#pausing)) and each machine's access. Beacon calls no model, anywhere.

## Unsealing the root key

The credentials agents work with live in Beacon's vault ([Secrets for agents](../guide/secrets.md)): item details encrypted under per-collection keys, those keys wrapped by one root key. Where the root key comes from decides what an attacker needs before the vault opens, and what you must do after a restart:

| Mode | How it works | After a restart | Whoever holds what can open it |
| --- | --- | --- | --- |
| `file` (default) | A 32-byte file in the secrets directory, made on first start | Available at once | The disk |
| `passphrase` | The root key derived from a passphrase (argon2id) and a key file together | The vault stays sealed until an owner types the passphrase in the console or at the command line | The disk *and* the passphrase |
| `aws-kms`, `gcp-kms`, `transit` | The root key wrapped by a KMS; Beacon asks it to unwrap at start | Available at once | KMS access |

Sealed, the vault refuses to serve values — monitoring, alerting and the SSH channel work as ever — and requests are refused with `secrets.sealed` (or queued, as the [settings](../reference/settings.md) say). Values are decrypted only when used, never cached in the clear; Go cannot guarantee scrubbing memory, so Beacon does its best and says so. [The secrets design](../about/secrets.md#encryption-and-signatures) has the whole hierarchy, and what is signed.

## Text from outside Beacon

Much of the evidence an alert carries is text set by probes, rule engines, the probed services and CI — any of it could hold instructions meant for an agent. Beacon cleans it (terminal escapes, control and invisible characters removed, long strings cut), checks values that go into queries, and marks the fields it carries in an `untrusted` object, with a note to read them as data. The [on-call prompt](../integrations/on-call-prompt.md) tells the agent the same. [Untrusted text](../guide/on-call.md#untrusted-text) has the whole of it.

## The threat model

Stated as attack and answer, with the mechanism that enforces each — nothing claimed beyond what a mechanism does:

| The attack | What happens | Enforced by |
| --- | --- | --- |
| A prompt-injected agent asks for an item outside its entitlements | It cannot see or request the item; the answer is as though the item did not exist | Server-side policy |
| An injected agent leaks a value it was granted | It can — that is the price of granting a read. The grant bounds the items, fields, uses and duration; every use carries the agent's name and a reason; anomalies alert; high-value items can be restricted to `use` without seeing the value | Policy and audit |
| An injected agent talks a review agent into approving too much | A review agent's levels and ceilings are settings it cannot raise; it cannot approve `person` items at all; every decision is pushed to people | Server-side policy |
| Someone takes a backup, without the private key | Nothing in it can be read | Encryption (age) |
| Someone takes the database file | Summaries are readable — which items exist, their tags — but values, usernames, notes and seeds are ciphertext | Encryption |
| Someone edits the database to widen a permission or insert a grant | The row's signature fails; it is treated as absent, and an alert is raised | Encryption (HMAC) |
| Someone edits or deletes audit records | The hash chain breaks; the checkpoint no longer matches | Hash chain and signatures |
| Someone takes the whole disk | `file` mode: the vault opens. `passphrase`: the passphrase is still needed. KMS: KMS access is still needed | The unsealing mode |
| Beacon's centre is compromised | That is the SSH certificate authority compromised as well — already the highest stake on the machine. Host hardening and Beacon's own alerts are the defence | Operations |
| An agent's transcript leaves the machine | Values taken with `read` appear in it; values used with `use` do not | The choice of action |

## What the operator carries

1. **HTTPS under `public_url`.** The console's cookie is `Secure`, and the OAuth clients check the issuer, so serve Beacon over HTTPS (or on `localhost`); the reference deployment puts Caddy in front. [Configuration](configuration.md).
2. **Secrets as files, never values.** Every credential is a `*_file` path under `secrets/`, owner 65532, mode 0400; secrets are never logged and never appear in errors. How those files are filled and protected is the deployment's own business: Beacon depends on no secret manager. [Secrets are files, never values](configuration.md#secrets-are-files-never-values).
3. **The ports.** In the reference deployment only 80 and 443 (Caddy) and 17002 (the SSH channel) face the Internet; 17001, 17006, 8428 and 8880 are bound to `127.0.0.1`. `/metrics` answers direct connections only, so it stays off the proxies. [Ports](index.md#ports).
4. **A private chat.** Cards and the signed links they open go to your chat; anyone holding a link sees that page until it expires, so the chat the links go to must be private. [Signed links](../guide/console.md#signed-links).
5. **The machine account is the real boundary.** Certificates name the `beacon` account; give it only what the work needs (a sudoers entry with the exact commands, for instance), and exempt the centre's address from OpenSSH's penalties. [On a server](install.md#3-the-first-machine).
6. **The recovery material, shown once.** At the first start Beacon shows — once, to download then — the private key its backups are encrypted to, with the root key beside it. Lose it and the backups are unreadable; it is the one thing with no copy on the server. [Backup and restore](backup.md).

## Reporting a hole

A hole in Beacon is a hole in every deployment. Report one privately, with the version and how to reproduce it, to the address in the [security policy](https://github.com/openlinkos/beacon/blob/main/SECURITY.md); do not open a public issue for it.

## What next

- [Backup and restore](backup.md): a backup holds the keys as well as the data; protect it accordingly.
- [Goals](../about/goals.md), including what Beacon will not do.
