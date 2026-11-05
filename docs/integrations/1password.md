# Import from 1Password

By the end of this page your 1Password vaults are Beacon collections, with their items, fields, attachments and one-time passwords, each at a level that says which agent may ask for it. The commands are in [Secrets for agents](../guide/secrets.md); this page is the import itself.

An export from 1Password is plaintext: everything in it, in the clear. `beacon secret import` talks to the running service over the admin socket in the data directory, so the file is parsed on the Beacon host — copy the export there, run the command inside the container, then delete it. The console's import wizard takes the file in the browser, parses it in memory and never writes it to disk. There is no REST or MCP import: an agent never sees an export. Delete the export when you are done — both paths remind you.

## Before you export

1Password offers three ways out, and all three land in Beacon:

| Source | What it holds | Use it when |
| --- | --- | --- |
| **1PUX** (recommended) | Everything: accounts, vaults, items, sections and fields of every type, notes, attachments, password history, archived state | Always, and certainly past about 300 items |
| **CSV** | Login and Password items only | A quick try on a slice |
| **The `op` command line | What the account can read, live | The vaults are large but you want only some items; mind the rate limits below |

Export 1PUX from the desktop app (Account settings → Export, one account at a time) or CSV from an item list; `op` needs an authenticated session. 1Password's own documentation describes each export in detail.

## What maps to what

| 1Password | Beacon |
| --- | --- |
| Vault (`P`, `E`, `U` alike) | A collection of the same name — `Shared`, `Engineering`, `Personal` stay `Shared`, `Engineering`, `Personal` |
| Item | An item with the same title and URL, its source kept as `1password:<uuid>` |
| Category (`categoryUuid`) | The same category: Login, Password, API Credential, Database, Server, SSH Key, Secure Note, Email Account, Wireless Router, Software License |
| Login fields | Split by purpose into `username` and `password` |
| Sections and fields | Sections kept; field types mapped — `concealed`, `string`, `email`, `url`, `totp`, `date`, `monthYear`, `phone`, `sshKey` |
| Notes, `passwordHistory`, `files/` | The note; the history as earlier versions; the files as attachments |
| Tags | Kept as they are (already `/`-separated), lower-cased, plus `source/1password` |
| `state: archived` | Archived |

Nothing is printed during the import — no value, no note — only counts.

## The levels after import

Each item arrives at a level, and the defaults are deliberate ([Settings](../reference/settings.md)):

- Vaults named **Personal** or **Private** import at `person`: these are yours, and an agent asks you for them.
- Every other vault imports at `review`.
- Identity and money categories — Credit Card, Bank Account, Identity, Passport, Driver License, Social Security Number, Medical Record, Membership, Reward Program — import at `never`: they are not for agents at all, whatever the vault.

The dry run lists what each vault and category would get, and you may change the levels per collection before committing; the conflict rule (`import.on_conflict`, `skip` by default; `new_version` or `keep_both`) says what happens to an item that looks like one already there — same URL after normalisation, same username. There is no `ask`: a headless import never waits for a decision.

## Run the import

```bash
beacon secret import 1pux --file export.1pux --dry-run
# per-collection counts, category spread, the levels they would get, the conflicts
beacon secret import 1pux --file export.1pux
```

From the `op` command line, the same import reads `op vault list`, `op item list --vault` and `op item get --reveal --format json` itself; it makes one to three requests per item, and an individual account is limited to about 1,000 reads a day, so past roughly 300 items take the 1PUX route instead. `beacon secret import csv --file export.csv` reads the CSV export.

When it finishes, the console lists what is left to do: confirm each collection's level, review the `never` suggestions, and give agents their entitlements. Then delete the export.

## References

A workflow or a `.env` written against 1Password's reference syntax moves by substitution:

```text
op://Engineering/api-token/credential        →   beacon://Engineering/api-token/credential
op://Shared/dns example.com/password         →   beacon://Shared/dns example.com/password
```

`beacon://` keeps the same shape — collection, item, optional section, field — so scripts that read `op://` references need only the prefix changed ([Secrets for agents](../guide/secrets.md#what-a-secret-is)).
