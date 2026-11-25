# Give agents a machine

By the end of this page an agent reaches a server through Beacon's SSH channel. It never holds a key or a password for the machine. How a connection goes:

- the agent signs in to Beacon with its own key;
- Beacon checks that it may reach the machine;
- Beacon connects with a certificate for that one connection, valid for five minutes;
- every session leaves a record of who connected, to what and when.

Taking the agent's access away closes what it has open at once.

You need:

- Beacon running.
- An agent whose preset is On call or Operator (they give `machines:ssh`), and whose owner's role allows it ([Invite your team](people.md)).

The commands run on the centre. The reference deployment has them as `docker exec beacon beacon …`.

## Turn it on {#turn-it-on}

The reference deployment does this for you: `install.sh` generates both keys, and `config.yaml` names them. Elsewhere, make two keys and name them in the configuration:

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

3. **Publish the port and check it.** The channel is plain SSH on its own port, so publish it directly, not through the HTTP proxy. `beacon check-config` parses both keys, and refuses one key used for both. `beacon ssh status` then says:

   ```
   agents reach machines; the channel's host key is ssh-ed25519 AAAA…
   ```

The CA's private key lets whoever holds it into every machine that trusts it. Keep it in its own file, readable by the service only. Set `source_addresses` when Beacon's addresses are fixed. What the channel guarantees, and what it does not, is in the [security model](../self-hosting/security.md).

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

3. **Give the account only the rights its work needs.** What the account may do is the real boundary. For example, in `/etc/sudoers.d/beacon`:

   ```
   beacon ALL=(root) NOPASSWD: /usr/bin/systemctl restart relay, /usr/bin/journalctl
   ```

Check that `sshd_config` includes `/etc/ssh/sshd_config.d/*.conf`. Run `sshd -t` before reloading. The service is `ssh` on Debian and Ubuntu and `sshd` on distributions such as RHEL.

If the machine's OpenSSH supports `PerSourcePenaltyExemptList` (introduced in 9.8), add `PerSourcePenaltyExemptList 203.0.113.7` to `beacon.conf`, using the centre's source address. All connections from Beacon arrive from that address. Validate and reload sshd after the change.

Other options and commands:

- `-user` names another account than `beacon`.
- `-critical` marks the machine as critical for whoever works on it.
- `machine list` shows every machine.
- `machine add … -replace` pins a new host key after a reinstall.
- `machine remove NAME` forgets a machine, and keeps its sessions on record.

The console does the same for someone with `machines:write`, on **Machines**. **Add a machine** takes the name, the address (`host` or `host:port`, or behind NAT), the account (`beacon` when left out), whether it is critical, a note, and, for a machine behind NAT, the key it connects out with and an optional host key. The page then shows, once, what the machine needs: the same lines the command prints. A machine's page edits the note, the critical flag and the account, pins the host key again (and shows the old and new fingerprints), and, for one behind NAT, changes the key it connects out with. **Remove the machine** asks for the machine's name, typed in full. The machine is forgotten, an open connector closes, and its sessions stay on record.

A machine Beacon cannot reach is added the other way round: [Machines behind NAT](#machines-behind-nat).

## Check machine SSH {#check-machine-ssh}

After configuring and reloading sshd, open the enrolled machine under **Machines → Connect over SSH** and select **Check machine SSH**. You need the On call role or another role with SSH access to that machine. Beacon checks the registered route, verifies the pinned host key and tries its short-lived certificate without opening a shell or running a command. The signed-in result identifies where the check stopped, and the audit log records the attempt as `machine_ssh_check`.

**Machine SSH is ready** confirms the handshake from Beacon to the machine. Continue with the personal connection below to verify your workstation's key and reachability. If certificate login did not complete, check the machine account, `TrustedUserCAKeys`, and sshd's logs. For a host key mismatch, verify the machine's fingerprint before updating the pin. For a machine behind NAT, verify that its connector has registered the forward to the correct sshd port.

## Connect as yourself {#connect-as-yourself}

With the On call role or above, register a public key reserved for your account in **Your account → Personal SSH keys**. If you need a new key, create it on your workstation.

```bash
mkdir -p -m 700 ~/.ssh
ssh-keygen -t ed25519 -f ~/.ssh/beacon-personal
```

Register only the contents of `~/.ssh/beacon-personal.pub`. Keep the matching private key on your workstation. Beacon accepts each key fingerprint for one identity, so the agent needs a different key even on the same workstation.

Open **Machines → edge-1 → Connect over SSH**. Copy the displayed `Host` block to `~/.ssh/beacon-personal.conf` and append the host key line to `~/.ssh/known_hosts`. Set `IdentityFile` to your local private key if you use a different path. Run `ssh -F ~/.ssh/beacon-personal.conf beacon-edge-1 hostname`. The dedicated configuration selects your key and establishes a new connection under your personal account. The reference deployment sets `ssh.public_address` so the console can show these settings.

Open **Sessions**, find the command on `edge-1` and start its replay. The actor is your username. An agent's sessions show `person / agent`. Remove a personal key in **Your account** to close its active connections and stop it from signing in again.

## Give an agent access {#give-an-agent-access}

An interactive agent signed in with OAuth can run `run_command` without registering an SSH key. To use its own OpenSSH client, follow [Connect with your own ssh, scp and rsync](../integrations/claude-code.md#connect-with-your-own-ssh-scp-and-rsync) to register a dedicated agent key.

A headless agent is made with its key:

```bash
docker exec beacon beacon agent add claude-oncall -owner alice -preset oncall \
  -machines edge-1,edge-2 -ssh-key "ssh-ed25519 AAAA… agent@workstation"
```

`-machines` limits the machines the agent reaches; without it, it reaches all. The same credentials work for the MCP tools and the REST API. A token of its own keeps its keys and machines on the token (`token ssh-key`, `token machines`).

## Let the agent connect

Either way, with the same credentials:

- **Its own ssh.** `list_machines` returns an `ssh_config` block and a `known_hosts` line. They are for the agent's own `ssh`, `scp`, `sftp` and `rsync`, with `ssh -L` port forwarding. How to put them in place is in [Claude Code](../integrations/claude-code.md#connect-with-your-own-ssh-scp-and-rsync).
- **Beacon's tools.** `run_command`, `read_file`, `write_file` and `edit_file` take the names and behaviour of an agent's own tools. `list_machines` lists the machines and whether the agent reaches each. Every parameter is in [MCP tools](../reference/mcp.md). Each tool call is a session of its own, recorded like any other.

Beacon keeps a session record for commands, shells, transfers, forwarded ports and each tool call. Capture depends on the session type.

- **The recording** captures terminal activity when supported. An available asciicast v3 file under `data_dir/recordings/` replays on the session's page.
- **The transcript** keeps command and tool output, or the individual commands and exit statuses from a shell with usable command integration.
- **SFTP and port forwards** keep connection metadata. The console shows recording and transcript capture status independently, including incomplete captures and storage failures.
- **The alert chat** hears one line per agent and machine, with a **Replay** link that uses the reader’s Beacon account.

Sessions are kept for `ssh.keep` (180 days), then deleted.

A session without a terminal, such as a tool call or `ssh host command`, sends lines ending in a bare `\n`. The replay shows them as a terminal would, with `\r\n`. The file and the transcript keep what was sent.

## Pause, or stop one agent

`beacon ssh pause` closes every open session at once, and refuses new ones until `beacon ssh resume`. `ssh status` says which.

To stop one agent rather than all, revoke its credentials or delete it on its page, or run `beacon token revoke NAME`. What it has open on machines closes at once. [Stop an agent, pause everything](stop.md) lists every switch that does the same.

## Machines behind NAT {#machines-behind-nat}

Some machines Beacon cannot reach: behind NAT, behind a firewall that lets connections out only, or a box at home. Such a machine connects out to the channel itself, with the OpenSSH client it already has. Beacon reaches its sshd through that connection. Nothing is installed on the machine.

1. **On the machine, as root**, make the key it connects out with:

   ```bash
   ssh-keygen -t ed25519 -N '' -C 'beacon connector edge-9' -f /etc/ssh/beacon_connect_key
   ```

2. **On the centre**, register the key (paste the content of the machine's `/etc/ssh/beacon_connect_key.pub`):

   ```bash
   docker exec beacon beacon machine add edge-9 connector -key "ssh-ed25519 AAAA… beacon connector edge-9" -note "office NAS"
   ```

   You will see `added edge-9 as a connector machine (user beacon): it connects out to Beacon with ssh -R; …` and a script for the machine. The script sets up the CA and the `beacon` account as for any machine, then a `beacon-connector` user and a systemd unit with `Restart=always`.

   The machine's host key is pinned like any other's. Give it with `-host-key`, or let Beacon learn it at the first connection (the audit log and the chat say so).

From then on everything is as for any other machine: `ssh`, `scp`, `rsync`, the tools, the recording, the pause switch.

- `machine list` shows `connector: true` and `connected`.
- A machine whose connector is not connected fails at once (`machine.disconnected`).
- After a Beacon restart, the units reconnect within seconds.
- A connector key signs in only as its own machine, and opens no session.

What the channel guarantees here is in the [security model](../self-hosting/security.md).

## Check it

1. **Ask the agent to run one command:**

   ```
   ssh -F ~/.ssh/beacon-claude-code.conf edge-1 hostname
   ```

2. **Read the session back.** Open `/console/sessions`, or ask for `list_sessions`. The session has a recording and shows `person / agent` as the actor. If alert chat is configured, it also gets a replay link for a signed-in reader.

When the channel refuses, it says why. [The agent cannot reach a machine](../self-hosting/troubleshooting.md#the-agent-cannot-reach-a-machine) lists every message and what to do about it.

## Next

- [Put an agent on call](on-call.md): the evidence that sends the agent to the machine, and the loop around it.
- [Review what agents did](console.md): the sessions, the transcripts and the replay.
- [Stop an agent, pause everything](stop.md): the brakes, and what each closes.
