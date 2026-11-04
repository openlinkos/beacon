# The SSH channel

An agent that fixes servers has to log in to them. The usual way is to give it a private key or a password, and from then on it can reach every machine that key opens, from anywhere, with nothing recording what it runs. Beacon's SSH channel does it the other way round. The agent signs in to Beacon with its own key, registered with Beacon; Beacon checks that the agent may reach the machine, connects to the machine with a certificate it signs for that one connection, valid for five minutes, and records the session. Machines trust only Beacon's certificate authority (CA), so the agent never holds a key or a password for them, and taking its access away is a matter of revoking its credentials or pausing the channel: what it has open closes at once.

This page is for whoever runs Beacon and adds machines to it. It covers turning the channel on, adding a machine, giving an agent access, how the agent connects with its own `ssh`, `scp` and `rsync` or with Beacon's MCP tools, what is recorded and how to replay it, pausing, machines behind NAT, and what the channel guarantees.

Beacon does not review commands: deciding what to run is the agent's job, done by its own harness. People see everything and can pause the channel.

## Turn it on

The reference deployment does this for you: `install.sh` generates both keys and `config.yaml` names them. Elsewhere, make two keys and name them in the configuration:

```bash
ssh-keygen -t ed25519 -N '' -C 'beacon host' -f secrets/ssh-host-key   # the channel's own host key
ssh-keygen -t ed25519 -N '' -C 'beacon ca' -f secrets/ssh-ca-key       # signs the certificates
```

```yaml
ssh:
  listen: ":17002"
  public_address: ssh.example.com:17002   # where agents connect
  host_key_file: /etc/beacon/secrets/ssh-host-key
  ca_key_file: /etc/beacon/secrets/ssh-ca-key
  # source_addresses: [203.0.113.7/32]    # certificates then work only from Beacon's addresses
  keep: 4320h                             # sessions are kept 180 days
```

The channel is plain SSH on its own port: publish it directly (a firewall rule), not through the HTTP proxy. `beacon check-config` checks both keys and refuses one key used for both. `beacon ssh status` then says `agents reach machines; the channel's host key is ssh-ed25519 AAAA…`.

The CA's private key lets whoever holds it into every machine that trusts it. Keep it in its own file, readable by the service only, and set `source_addresses` when Beacon's addresses are fixed.

## Add a machine

On the centre:

```bash
docker exec beacon beacon machine add edge-1 edge-1.example.net -note "Los Angeles edge node"
```

Beacon connects once to port 22 (or the port you give, `HOST:PORT`), pins the machine's host key, and prints what the machine needs: the line for `/etc/ssh/beacon_ca.pub`, and the commands below. The name is the machine's name everywhere in Beacon: lower-case letters, digits and dashes. On the machine, as root:

```bash
echo 'ssh-ed25519 AAAA… beacon-ca' > /etc/ssh/beacon_ca.pub   # the line machine add printed; beacon machine ca prints it again
echo 'TrustedUserCAKeys /etc/ssh/beacon_ca.pub' > /etc/ssh/sshd_config.d/beacon.conf
id beacon >/dev/null 2>&1 || useradd -m -s /bin/bash -p '*' beacon   # the account Beacon's certificates name
systemctl reload ssh
```

`-user` names another account than `beacon`, and `-critical` marks a machine as critical for whoever works on it (the console and `list_machines` show it). Then:

- **What the account may do is the real boundary.** Give `beacon` only the rights its work needs, for example in `/etc/sudoers.d/beacon`:

  ```
  beacon ALL=(root) NOPASSWD: /usr/bin/systemctl restart relay, /usr/bin/journalctl
  ```

  A broader rule is the team's choice to make; Beacon records what was done either way.
- **Exempt Beacon from OpenSSH's penalties.** OpenSSH 9.8 and later penalise an address after failed connections (`PerSourcePenalties`). Every agent comes from Beacon's address, so add it: `PerSourcePenaltyExemptList 203.0.113.7` in `beacon.conf`.
- **A record of what ran on the machine itself**, optional: the certificate's key ID, which sshd logs ("ID beacon 20261101-091724-aa8bba for alice / claude-code via ssh"), names the session, and an auditd rule such as `-a always,exit -F arch=b64 -S execve -F auid>=1000 -k beacon` records every program it started, scripts included.

`machine list` shows every machine; `machine add … -replace` pins a new host key after a reinstall; `machine remove NAME` forgets a machine, and its sessions stay on record. A machine Beacon cannot connect to is added the other way round: see [Machines behind NAT](#machines-behind-nat).

## Give an agent access

An agent reaches machines when its preset is On call or Operator, which give `machines:ssh`, and its owner's role allows it ([People and their agents](people.md)). For its own ssh, Beacon also needs the agent's public key, from the machine where the agent runs.

An interactive agent such as Claude Code, signed in with OAuth, registers its own key: ask it to *"register my SSH key ~/.ssh/id_ed25519.pub with Beacon"*, and it calls `add_ssh_key`. It reaches the machines you allowed when it signed in. A headless agent is made with its key, on the console's **Agents** page or on the centre:

```bash
docker exec beacon beacon agent add claude-oncall -owner alice -preset oncall \
  -machines edge-1,edge-2 -ssh-key "ssh-ed25519 AAAA… agent@workstation"
```

- `-machines edge-1,edge-2` limits the machines the agent reaches; without it, it reaches all. The agent's page changes its machines and keys later, as the agent's own `add_ssh_key` and `remove_ssh_key` do.
- The agent's credentials work for the MCP tools and the REST API as well as the SSH channel.
- A token of its own rather than an agent's (an expert's, from `beacon token create NAME -scopes … -ssh-key KEY -machines a,b`) keeps its keys and machines on the token: `token ssh-key NAME add KEY`, `token ssh-key NAME remove FINGERPRINT`, `token machines NAME all`.

## Connect with your own ssh, scp and rsync

`list_machines` returns an `ssh_config` block and a `known_hosts` line for the agent's own ssh, scp, sftp and rsync, with `ssh -L` port forwarding: how to put them in place, and what the agent's ssh says when the channel refuses, is in [Claude Code](../integrations/claude-code.md#connect-with-your-own-ssh-scp-and-rsync).

## The MCP tools

An agent without a shell, or one that prefers structured results, uses Beacon's tools with the same credentials. They take the names and the behaviour of an agent's own tools:

| Tool | What it does |
| --- | --- |
| `list_machines` | The machines, whether the agent reaches each, and an `ssh_config` block with a `known_hosts` line for the agent's own ssh |
| `run_command` | Runs a command (in `sh`, as the machine's user, no terminal): exit code, stdout and stderr as plain text |
| `read_file` | Lines of a text file, numbered like `cat -n`, with `offset` and `limit` |
| `write_file` | A whole file, at most 1 MiB, making its directory if needed |
| `edit_file` | Replaces an exact string, once or everywhere; refuses if the file changed meanwhile |
| `list_sessions`, `get_session` | Who did what where; a session's transcript |
| `add_machine`, `set_machine_connector`, `remove_machine`, `pause_ssh`, `resume_ssh` | With `machines:write` |

Each tool call is a session of its own, recorded like any other. The REST API has the same operations under `/api/v1/machines`, `/api/v1/sessions` and `/api/v1/ssh`; [MCP tools](../reference/mcp.md) has every parameter.

## What is recorded

Every session: a command, an interactive shell, a file transfer, a forwarded port, and each tool call.

- **The recording** is asciicast v3, under `data_dir/recordings/YYYY/MM/DD/`. It replays in the console, on the session's page (`/console/sessions/{id}`), plays with `asciinema play`, and downloads from `GET /api/v1/sessions/{id}/recording`.
- **The transcript** (`get_session`) holds a command's output as plain text, or each command of an interactive shell with its output and exit status. Bash reports each command line through the shell-integration marks Beacon starts it with, so commands typed ahead are named too (that shell clears `HISTCONTROL`, so every command is also in the account's history); other shells are recorded without the breakdown.
- **What is typed** is recorded as well. Agents never type passwords; people should not type any into a session either.
- **The alert chat** gets one message per agent and machine, at most every minute: what ran, with exit codes, and a **Replay** link per session.
- **Alerts** list the sessions on their machines: the evidence and the console's alert page show who ran what just before and during the alert.
- Sessions are kept for `ssh.keep` (180 days in the examples), then deleted with their files.

## Pausing

`beacon ssh pause` (or `pause_ssh`, `POST /api/v1/ssh/pause`, or the switch on the console's Machines page) closes every open session at once and refuses new ones until `ssh resume`. `ssh status` says which: `agents' access to machines is paused since …`. While paused, an agent's ssh gets `beacon: agents' access to machines is paused since …; a person resumes it with beacon ssh resume`.

To stop one agent rather than all, revoke its credentials or delete it on its page in the console, or revoke a token with `beacon token revoke NAME` (on the console's **Tokens** page, `machines:write` revokes any token that reaches machines). What the agent has open on machines closes at once, and its new connections and tool calls are refused. [Stop an agent](people.md#stop-an-agent) lists what else does the same: disabling or demoting its owner, narrowing its machines or its preset, removing a key.

## Machines behind NAT

A machine Beacon cannot reach (behind NAT, behind a firewall that lets connections out only, a box at home) connects out to the channel itself, with the OpenSSH client it already has, and Beacon reaches its sshd through that connection. Nothing is installed on the machine.

**How it works.** The machine runs `ssh -N -R 0:localhost:22 edge-9@ssh.example.com -p 17002` with a dedicated key, signing in as itself: the user name is the machine's name, as agents name their targets. Beacon tells a machine's connection from an agent's by the key that matched: a machine's connector key is registered on its record, an agent's on the agent (or on a token of its own). A connector connection may do nothing but ask for that one remote forward and keep alive: no sessions, no other channels or requests; one connection per machine, and the newest wins. Beacon binds no port for the forward (the port it reports back is a label). When an agent targets the machine, Beacon opens a channel back through the connection to the address the machine itself forwarded and runs its usual handshake over it: the pinned host key, a certificate valid for five minutes. Everything else is as for any other machine: `ssh`, `scp`, `sftp`, `rsync`, `ssh -L`, the MCP tools, the recording, the pause switch.

**Add it.** On the machine, as root, make the key it connects out with; then register it on Beacon:

```bash
ssh-keygen -t ed25519 -N '' -C 'beacon connector edge-9' -f /etc/ssh/beacon_connect_key   # on the machine
docker exec beacon beacon machine add edge-9 connector -key "ssh-ed25519 AAAA… beacon connector edge-9" -note "office NAS"   # on the centre
```

Paste the content of the machine's `/etc/ssh/beacon_connect_key.pub` as `-key`. `machine add` prints `added edge-9 as a connector machine (user beacon): it connects out to Beacon with ssh -R; …` and a script for the machine: the CA and the `beacon` account as for any machine, then the `known_hosts` line for Beacon's channel (its host key at `ssh.public_address`), a `beacon-connector` system user that owns the key, and a systemd unit `beacon-connector.service` with `Restart=always`, `ServerAliveInterval=30` and `ExitOnForwardFailure=yes`. The same comes back from `add_machine` (`address: connector`, `connector_key`) and `POST /api/v1/machines`, as `connector.script`, `connector.command` and `connector.unit`. The printed command forwards to port 22; edit it if the machine's sshd listens elsewhere.

- `machine connector NAME -key …` (`set_machine_connector`, `PUT /api/v1/machines/{name}/connector`) sets or changes the key; a connection made with the old key is closed, and the machine reconnects with the new one. A machine may be added without `-key` and given one later. `machine add … -replace` keeps the key unless `-key` is given.
- **The machine's host key** is pinned like any other machine's. Give it with `-host-key "ssh-ed25519 AAAA…"`, the content of the machine's `/etc/ssh/ssh_host_ed25519_key.pub`, or let Beacon learn it the first time the machine connects: Beacon asks the sshd behind the forward for its key, pins it, writes `machine_host_key` with the fingerprint to the audit log and tells the chat. Learning trusts that first connection, which signed in with the key you installed on the machine; to trust nothing, pass `-host-key`. After a reinstall, `machine add NAME connector -replace` learns the key again (or takes `-host-key`).
- The machine's sshd sees every session come from itself (`127.0.0.1` or `::1`): the printed setup adds `PerSourcePenaltyExemptList 127.0.0.1,::1`, and Beacon's certificates for such a machine allow those two sources besides `ssh.source_addresses`. sshd's log still names each session by the certificate's key ID.

**What Beacon guarantees.**

- Beacon connects only to the address the machine itself forwarded, never to one of its own choosing.
- A connector key signs in only as its own machine, opens no session, forwards no port of its own and holds no token; an agent's key registers no forward. A key an agent or a token holds cannot become a connector key.
- The pause switch closes sessions relayed through a connector like any other; the machine's own connection stays, so the machine is reachable again the moment the channel resumes.
- `machine list`, `list_machines` and `GET /api/v1/machines` show `connector: true`, `connected` and `connected_since`. A machine whose connector is not connected fails fast: `machine.disconnected` on the API and MCP, `beacon: the machine's connector is not connected` on an agent's ssh. Connects, disconnects and replacements are logged, not echoed to the chat; the gauge `beacon_ssh_connectors` counts the connected connectors and `beacon_ssh_connector_machines` the machines added as connectors, so a rule can fire when they differ.

**Limits.**

- One connection per machine. The newest wins; a Beacon restart drops them all, and `Restart=always` brings them back within seconds, during which the machine is `disconnected`.
- Two hops: the agent to Beacon, and Beacon to the machine over the connection the machine made, which carries every session on that machine. Latency adds up over both; throughput is that of the single connection.
- Beacon's own keepalives close a connection that stops answering for 90 seconds; a machine that fell off the network shows as disconnected after that, and reconnects when it is back.

## What the channel guarantees, and what it does not

It guarantees:

- **No key in an agent's hands.** Agents sign in to Beacon with their own key, registered with Beacon; machines trust only Beacon's CA, and every certificate is for one connection, names the account on the machine, and is valid for five minutes. With `source_addresses`, a certificate works only from Beacon's addresses.
- **Only the machines an agent may reach**, and only while the channel is not paused. An agent without `machines:ssh`, or limited to other machines, is refused before Beacon connects anywhere.
- **Access that ends closes what is open.** When an agent may no longer reach a machine (a credential revoked or expired, the agent deleted or narrowed, its owner disabled or demoted, a key removed), what it has open there closes at once; Beacon also checks every minute.
- **The machine is the machine you added.** Beacon checks each machine's pinned host key on every connection and refuses another.
- **Everything on record**: every session and tool call is recorded, listed under the alerts on its machine, and echoed to the chat.

It does not:

- **Review commands, or run on its own.** An agent asks for each session; what it runs is its own decision, bounded by what the account on the machine may do.
- **End an open session when its certificate expires.** The certificate is checked when Beacon opens the connection to the machine; a session open then lasts until it ends, the agent's access ends, the channel is paused, or Beacon restarts.
- **Pass on the client's environment** (the machine's locale and settings apply), remote port forwarding for agents (`ssh -R` is how a machine behind NAT connects out, never an agent's), agent forwarding or X11.
