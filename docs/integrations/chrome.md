# Import from Chrome

By the end of this page the passwords Chrome has saved for you are items in one Beacon collection, at a level that keeps them yours. The commands are in [Secrets for agents](../guide/secrets.md); this page is the import itself.

A Chrome export is plaintext, and it is all of your saved passwords in one file. Import it on the machine the file is on; the console's import wizard also takes the file, parses it in memory and never writes it to disk. Delete the export when you are done.

## Export from Chrome

Chrome saves passwords per profile, and exports them as CSV from `chrome://settings/passwords` (Offer to save passwords → Saved passwords → ⋮ → Export passwords; Edge and Brave have the same export under their own settings pages). The file's first line is its header, and Beacon reads by header, not by position:

```csv
name,url,username,password,note
```

One row per saved site; the "affiliated" domains of one site are a row each. Chrome does not export passkeys — those stay where they are.

## What maps to what

| Chrome | Beacon |
| --- | --- |
| The whole file | One collection you name at import — `Browser`, say — separate from collections imported from elsewhere |
| A row | An item: `name` the title, `url` one of its URLs, `username` the `username` field, `password` the `password` field, `note` the note |
| Rows with the same username *and* password on different domains | Merged into one item with several URLs, as Chrome itself groups them |
| (no tags) | `source/chrome` on every item |

The collection imports at the level you give it — `person` by default, like Chrome itself: saved in the browser, read by you ([Settings](../reference/settings.md)).

## Run the import

```bash
beacon secret import chrome --file "Chrome Passwords.csv" --collection Browser --dry-run
beacon secret import chrome --file "Chrome Passwords.csv" --collection Browser
```

The dry run reports what conflicts with items already there — same URL after normalisation, same username — and `import.on_conflict` (ask, by default) says what happens to those. Nothing is printed during the import but counts.

## After the import

The console lists what is left: confirm the collection's level, and give agents their entitlements — the usual route is to leave a browser collection at `person` and let agents in only where you choose. Then delete the CSV, and empty the trash or downloads folder it sat in.

Passkeys, and anything Chrome keeps in its own sync rather than as a saved password, never left Chrome; a password manager that shares to agents is a different job — [1Password](1password.md) imports with attachments and one-time passwords, and its service credentials suit automation better.
