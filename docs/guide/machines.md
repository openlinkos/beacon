# Give agents a machine

By the end of this page an agent reaches a server through Beacon's SSH channel: it signs in to Beacon with its own key, Beacon checks that it may reach the machine and connects with a certificate for that one connection, valid for five minutes, and every session is recorded. The agent never holds a key or a password for the machine, and taking its access away closes what it has open at once.

You need Beacon running and an agent whose preset is On call or Operator (they give `machines:ssh`) and whose owner's role allows it ([Invite your team](people.md)). The commands run on the centre; the reference deployment has them as `docker exec beacon beacon …`.

## Turn it on {#turn-it-on}

The reference deployment does this for you: `install.sh` generates both keys and `config.yaml` names them. Elsewhere, make two keys and name them in the configuration:

1. **Make the keys** (the channel's own host key, and the CA that signs the certificates):

   ```bash
   ssh-keygen -t ed25519 -N '' -C 'beacon host' -f secrets/ssh-host-key
   ssh-keygen -t ed25519 -N '' -C 'beacon ca' -f secrets/ssh-ca-key
   ```

2. **Name them in the configuration** and restart Beacon:

   ```yaml
   ssh:
     listen: ":17002"
     public_address: ssh.example.com:17002   # where agents connect
     host_key_file: /etc/beacon/secrets/ssh-host-key
     ca_key_file: /etc/beacon/secrets/ssh-ca-key
     # source_addresses: [203.0.113.7/32]    # certificates then work only from Beacon's addresses
     keep: 4320h                             # sessions are kept 180 days
   ```

3. **Check it.** The channel is plain SSH on its own port: publish it directly, not through the HTTP proxy. `beacon check-config` parses both keys and refuses one key used for both; `beacon ssh status` then says:

   ```
   agents reach machines; the channel's host key is ssh-ed25519 AAAA…
   ```

The CA's private key lets whoever holds it into every machine that trusts it. Keep it in its own file, readable by the service only, and set `source_addresses` when Beacon's addresses are fixed. What the channel guarantees, and what it does not, is in the [security model](../self-hosting/security.md).

## Add a machine {#add-a-machine}

1. **On the centre**, name the machine and its address. Beacon connects once to port 22 (or `HOST:PORT`), pins the machine's host key and prints what the machine needs:

   ```bash
   docker exec beacon beacon machine add edge-1 edge-1.example.net -note "Los Angeles edge node"
   ```

   You will see:

   ```
   added edge-1 at edge-1.example.net as beacon; its host key is ssh-ed25519 AAAA…
   On the machine, as root, if not done yet:
     echo 'ssh-ed25519 AAAA… beacon-ca' > /etc/ssh/beacon_ca.pub
     ...
   ```

2. **On the machine, as root**, run the lines it printed:

   ```bash
   echo 'ssh-ed25519 AAAA… beacon-ca' > /etc/ssh/beacon_ca.pub   # the line machine add printed; beacon machine ca prints it again
   echo 'TrustedUserCAKeys /etc/ssh/beacon_ca.pub' > /etc/ssh/sshd_config.d/beacon.conf
   id beacon >/dev/null 2>&1 || useradd -m -s /bin/bash -p '*' beacon   # the account Beacon's certificates name
   systemctl reload ssh
   ```

3. **Give the account only the rights its work needs.** What the account may do is the real boundary, for example in `/etc/sudoers.d/beacon`:

   ```
   beacon ALL=(root) NOPASSWD: /usr/bin/systemctl restart relay, /usr/bin/journalctl
   ```

`-user` names another account than `beacon`; `-critical` marks the machine as critical for whoever works on it. OpenSSH 9.8 and later penalise an address after failed connections, and every agent comes from Beacon's: add `PerSourcePenaltyExemptList 203.0.113.7` to the machine's `beacon.conf`. `machine list` shows every machine; `machine add … -replace` pins a new host key after a reinstall; `machine remove NAME` forgets a machine and keeps its sessions on record. A machine Beacon cannot reach is added the other way round: [Machines behind NAT](#machines-behind-nat).

## Give an agent access {#give-an-agent-access}

An interactive agent such as Claude Code, signed in with OAuth, registers its own key: ask it to *"register my SSH key ~/.ssh/id_ed25519.pub with Beacon"* and it calls `add_ssh_key`. A headless agent is made with its key:

```bash
docker exec beacon beacon agent add claude-oncall -owner alice -preset oncall \
  -machines edge-1,edge-2 -ssh-key "ssh-ed25519 AAAA… agent@workstation"
```

`-machines` limits the machines the agent reaches; without it, it reaches all. The same credentials work for the MCP tools and the REST API. A token of its own keeps its keys and machines on the token (`token ssh-key`, `token machines`).

## Let the agent connect

Either way, with the same credentials:

- **Its own ssh.** `list_machines` returns an `ssh_config` block and a `known_hosts` line for the agent's own `ssh`, `scp`, `sftp` and `rsync`, with `ssh -L` port forwarding: how to put them in place is in [Claude Code](../integrations/claude-code.md#connect-with-your-own-ssh-scp-and-rsync).
- **Beacon's tools.** `run_command`, `read_file`, `write_file` and `edit_file` take the names and behaviour of an agent's own tools; `list_machines` lists the machines and whether the agent reaches each. Every parameter is in [MCP tools](../reference/mcp.md); each tool call is a session of its own, recorded like any other.

Every session is recorded, a command, a shell, a transfer, a forwarded port and each tool call: the recording (asciicast v3, under `data_dir/recordings/`) replays on the session's page in the console, the transcript holds each shell command with its exit status, and the alert chat hears one line per agent and machine with a **Replay** link. Sessions are kept for `ssh.keep` (180 days), then deleted.

## Pause, or stop one agent

`beacon ssh pause` closes every open session at once and refuses new ones until `beacon ssh resume`; `ssh status` says which. To stop one agent rather than all, revoke its credentials or delete it on its page, or `beacon token revoke NAME`: what it has open on machines closes at once. [Stop an agent, pause everything](stop.md) lists every switch that does the same.

## Machines behind NAT {#machines-behind-nat}

A machine Beacon cannot reach (behind NAT, behind a firewall that lets connections out only, a box at home) connects out to the channel itself, with the OpenSSH client it already has, and Beacon reaches its sshd through that connection. Nothing is installed on the machine.

1. **On the machine, as root**, make the key it connects out with:

   ```bash
   ssh-keygen -t ed25519 -N '' -C 'beacon connector edge-9' -f /etc/ssh/beacon_connect_key
   ```

2. **On the centre**, register the key (paste the content of the machine's `/etc/ssh/beacon_connect_key.pub`):

   ```bash
   docker exec beacon beacon machine add edge-9 connector -key "ssh-ed25519 AAAA… beacon connector edge-9" -note "office NAS"
   ```

   You will see `added edge-9 as a connector machine (user beacon): it connects out to Beacon with ssh -R; …` and a script for the machine: the CA and the `beacon` account as for any machine, then a `beacon-connector` user and a systemd unit with `Restart=always`. The machine's host key is pinned like any other's: give it with `-host-key`, or let Beacon learn it at the first connection (the audit log and the chat say so).

From then on everything is as for any other machine: `ssh`, `scp`, `rsync`, the tools, the recording, the pause switch. `machine list` shows `connector: true` and `connected`; a machine whose connector is not connected fails at once (`machine.disconnected`), and after a Beacon restart the units reconnect within seconds. A connector key signs in only as its own machine and opens no session; what the channel guarantees here is in the [security model](../self-hosting/security.md).

## Check it

Ask the agent to run one command and read the session back:

```
ssh edge-1 'hostname'
```

Then open `/console/sessions` (or ask for `list_sessions`): the session is there with its recording, and the alert chat has the line about it. When the channel refuses, it says why: [The agent cannot reach a machine](../self-hosting/troubleshooting.md#the-agent-cannot-reach-a-machine) lists every message and what to do about it.

## Next

- [Put an agent on call](on-call.md): the evidence that sends the agent to the machine, and the loop around it.
- [Review what agents did](console.md): the sessions, the transcripts and the replay.
- [Stop an agent, pause everything](stop.md): the brakes, and what each closes.
