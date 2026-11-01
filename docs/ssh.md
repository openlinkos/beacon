# The SSH channel

English | [简体中文](zh/ssh.md)

Agents reach machines through Beacon: with their own `ssh`, `scp`, `sftp`, `rsync` and `ssh -L`, or with the MCP tools (`run_command`, `read_file`, `write_file`, `edit_file`). An agent never holds a password or a key for a machine. Beacon signs a certificate valid for five minutes for each connection, and the machine trusts only Beacon's certificate authority. Every session is recorded for replay, with an interactive shell broken down into its commands and their exit statuses, and echoed to the alert chat.

Beacon does not review commands: deciding what to run is the agent's job, done by its own harness. People see everything and can pause the channel.

## Turn it on

Make two keys and name them in the configuration:

```bash
ssh-keygen -t ed25519 -N '' -C 'beacon host' -f secrets/ssh-host-key   # the channel's own host key
ssh-keygen -t ed25519 -N '' -C 'beacon ca' -f secrets/ssh-ca-key       # signs the certificates
```

```yaml
ssh:
  listen: ":17002"
  public_address: ssh.example.com:17002   # where agents connect
  host_key_file: /etc/linkos-beacon/secrets/ssh-host-key
  ca_key_file: /etc/linkos-beacon/secrets/ssh-ca-key
  # source_addresses: [203.0.113.7/32]    # certificates then work only from Beacon's addresses
  keep: 4320h                             # sessions are kept 180 days
```

The channel is plain SSH on its own port: publish it directly (a firewall rule), not through the HTTP proxy. `linkos-beacon check-config` checks both keys.

The certificate authority's private key lets whoever holds it into every machine that trusts it. Keep it in its own file, readable by the service only, and keep `source_addresses` set when Beacon's addresses are fixed.

## Add a machine

```bash
docker exec linkos-beacon linkos-beacon machine add edge-1 edge-1.example.net -note "Los Angeles edge node"
```

Beacon connects once, pins the machine's host key and prints what the machine needs. On the machine, as root:

```bash
docker exec linkos-beacon linkos-beacon machine ca > /etc/ssh/beacon_ca.pub   # or paste the line it printed
echo 'TrustedUserCAKeys /etc/ssh/beacon_ca.pub' > /etc/ssh/sshd_config.d/beacon.conf
useradd -m -s /bin/bash -p '*' beacon   # the account Beacon's certificates name
systemctl reload ssh
```

- **What the account may do is the real boundary.** Give `beacon` only the rights its work needs, for example in `/etc/sudoers.d/beacon`:

  ```
  beacon ALL=(root) NOPASSWD: /usr/bin/systemctl restart relay, /usr/bin/journalctl
  ```

  A broader rule is the team's choice to make; Beacon records what was done either way.
- **Exempt Beacon from OpenSSH's penalties.** OpenSSH 9.8 and later penalise an address after failed connections (`PerSourcePenalties`). Every agent comes from Beacon's address, so add it: `PerSourcePenaltyExemptList 203.0.113.7` in `beacon.conf`.
- **A record of what ran on the machine itself**, optional: the certificate's key ID, which sshd logs ("ID beacon 20261101-091724-aa8bba for claude-code via ssh"), names the session, and an auditd rule such as `-a always,exit -F arch=b64 -S execve -F auid>=1000 -k beacon` records every program it started, scripts included.
- `machine add … -replace` pins a new host key after a reinstall; `machine remove NAME` forgets a machine (its sessions stay on record); `machine list` shows them all.

Machines behind NAT, which Beacon cannot connect to, are not supported yet; a small outbound connector is planned.

## Give an agent access

A token with the `machines:ssh` scope and the agent's public key:

```bash
docker exec linkos-beacon linkos-beacon token create claude-code \
  -scopes status:read,machines:ssh,sessions:read -expires 90d -ssh-key "$(cat ~/.ssh/id_ed25519.pub)"
```

`-machines edge-1,edge-2` limits the machines it reaches; `token machines NAME all` lifts the limit. `token ssh-key NAME add KEY` and `remove FINGERPRINT` change the keys later.

The same token works for MCP, where the tools are:

| Tool | What it does |
| --- | --- |
| `list_machines` | The machines, whether the token reaches each, and an `ssh_config` block with a `known_hosts` line for the agent's own ssh |
| `run_command` | Runs a command (in `sh`, as the machine's user, no terminal): exit code, stdout and stderr as plain text |
| `read_file` | Lines of a text file, numbered like `cat -n`, with `offset` and `limit` |
| `write_file` | A whole file, at most 1 MiB, making its directory if needed |
| `edit_file` | Replaces an exact string, once or everywhere; refuses if the file changed meanwhile |
| `list_sessions`, `get_session` | Who did what where; a session's transcript |
| `add_machine`, `remove_machine`, `pause_ssh`, `resume_ssh` | With `machines:write` |

The REST API has the same operations under `/api/v1/machines`, `/api/v1/sessions` and `/api/v1/ssh`.

For the agent's own ssh, put `list_machines`' `ssh_config` in `~/.ssh/config` and its `known_hosts` line in `~/.ssh/known_hosts`:

```
Host edge-1
  HostName ssh.example.com
  Port 17002
  User edge-1
  HostKeyAlias beacon
```

Then `ssh edge-1 'systemctl status relay'`, `scp`, `rsync -e ssh` and `ssh -L` work as usual. The user name tells Beacon which machine; on the machine, the session runs as `beacon`.

## What is recorded

Every session: a command, an interactive shell, a file transfer, a forwarded port, and each tool call.

- The recording is asciicast v3, under `data_dir/recordings/YYYY/MM/DD/`, and plays with `asciinema play` or in a page with asciinema-player. `GET /api/v1/sessions/{id}/recording` downloads it.
- The transcript (`get_session`) holds a command's output as plain text, or each command of an interactive shell with its output and exit status. Bash reports each command line through the shell-integration marks Beacon starts it with, so commands typed ahead are named too (that shell clears `HISTCONTROL`, so every command is also in the account's history); other shells are recorded without the breakdown.
- What is typed is recorded as well. Agents never type passwords; people should not type any into a session either.
- The alert chat gets one message per agent and machine, at most every minute: what ran, with exit codes.
- Sessions are kept for `ssh.keep`, then deleted with their files.

## Pausing

`linkos-beacon ssh pause` (or `pause_ssh`, or `POST /api/v1/ssh/pause`) closes every open session and refuses new ones until `ssh resume`. `ssh status` says which.

## What it does not do

- It does not review commands, and it does not run on its own: an agent asks for each session.
- It does not pass on the client's environment (the machine's locale and settings apply), remote port forwarding (`ssh -R`), agent forwarding or X11.
