# Security model

This page states Beacon's security model in one place: what Beacon guarantees, what it refuses to do, and the few things the operator carries. Each part links to the page that tells it in full.

Beacon holds what agents never see:

- the keys that let them into machines;
- the tokens they sign in with;
- the record of every command.

## Who may do what

- **People sign in with Beacon's own accounts.** Usernames and passwords, argon2id hashes, roles (viewer, on call, operator, owner). There is no other way in, and no outside identity provider. [Roles](../guide/people.md#roles).
- **An agent acts for one person, and never beyond them.** It signs in by MCP's OAuth or a scoped token. What it may do, its preset within its owner's role, is decided afresh on every request. So a demoted owner narrows its agents at once. [Agents](../guide/people.md#agents).
- **A machine's token writes only as its own subject**, and an expired machine cannot write. The rule engine's alerts need the rules token.
- **Tokens are stored as SHA-256 hashes**, carry scopes and expire. Failed attempts are limited per address.
- **Every write is on record.** Each change through the API, MCP, a card or the command line lands in the audit log, echoed to the alert chat ([Who the audit log names](../guide/people.md#who-the-audit-log-names)).

## The SSH channel

The channel's promise is that an agent reaches a machine without ever holding a key. [Give agents a machine](../guide/machines.md) sets it up.

What the channel does:

- The agent signs in to Beacon with its own key.
- Machines trust only Beacon's certificate authority.
- Every connection gets a certificate valid for five minutes, which names the account on the machine.
- Only the machines an agent may reach are connected to.
- When access ends, what the agent has open closes. This is checked again every minute.
- Each machine's host key is pinned.
- Every session is recorded, with a replay.

Equally deliberate is what it does not do:

- It reviews no command and runs nothing on its own. Deciding what to run is the agent's job, done by its own harness.
- It passes on no agent forwarding, no X11 and no remote port forwarding for agents.

The brakes are the pause switch ([Pause the whole channel](../guide/stop.md#pause-the-whole-channel)) and each machine's access. Beacon calls no model, anywhere.

## Text from outside Beacon

Much of the evidence an alert carries is text set by probes, rule engines, the probed services and CI. Any of it could hold instructions meant for an agent. Beacon:

- **cleans it**: terminal escapes, control and invisible characters are removed, and long strings are cut;
- **checks values** that go into queries;
- **marks the fields** it carries in an `untrusted` object, with a note to read them as data.

The [on-call prompt](../integrations/on-call-prompt.md) tells the agent the same. Such fields appear in [the evidence](../guide/on-call.md#the-evidence) and in [a hook's payload](../reference/webhooks.md#the-payload).

## What the operator carries

1. **HTTPS under `public_url`.** The console's cookie is `Secure`, and the OAuth clients check the issuer. So serve Beacon over HTTPS (or on `localhost`); the reference deployment puts Caddy in front. [Configuration](configuration.md).
2. **Secrets as files, never values.** Every credential is a `*_file` path under `secrets/`, owner 65532, mode 0400. Secrets are never logged and never appear in errors. How those files are filled and protected is the deployment's own business: Beacon depends on no secret manager. [Secrets are files, never values](configuration.md#secrets-are-files-never-values).
3. **The ports.** In the reference deployment only 80 and 443 (Caddy) and 17002 (the SSH channel) face the Internet. 17001, 17006, 8428 and 8880 are bound to `127.0.0.1`. `/metrics` answers direct connections only, so it stays off the proxies. [Ports](index.md#ports).
4. **A private chat.** Cards and the signed links they open go to your chat. Anyone holding a link sees that page until it expires, so the chat the links go to must be private. [Signed links](../guide/console.md#signed-links).
5. **The machine account is the real boundary.** Certificates name the `beacon` account. Give it only what the work needs, such as a sudoers entry with the exact commands. Also exempt the centre's address from OpenSSH's penalties. [On a server](install.md#3-the-first-machine).

## Reporting a hole

A hole in Beacon is a hole in every deployment. Report one privately, with the version and how to reproduce it, to the address in the [security policy](https://github.com/openlinkos/beacon/blob/main/SECURITY.md). Do not open a public issue for it.

## What next

- [Backup and restore](backup.md): a backup holds the keys as well as the data; protect it accordingly.
- [Goals](../about/goals.md), including what Beacon will not do.
