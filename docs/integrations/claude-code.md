# Claude Code

Claude Code signs in to Beacon through the browser, with MCP's OAuth. You add Beacon's `/mcp` without a token, sign in from Claude Code, and allow it on Beacon's consent page.

1. **Add Beacon.** On the workstation where Claude Code runs:

   ```bash
   claude mcp add --scope user --transport http beacon https://status.example.com/mcp
   ```

2. **Sign in.** Run `/mcp` in Claude Code, choose `beacon` and authenticate. Or, from the shell, run `claude mcp login beacon`.
3. **Allow it** on Beacon's consent page, as the next section shows.

## Allow it on Beacon's consent page

The browser opens Beacon's consent page, **Claude Code wants to use Beacon as you**. If you were not signed in to the console, you sign in first. The page asks four things:

- **Agent name**: `claude-code`, or your existing agent for Claude Code. The same name later reuses that agent. Another person's agent's name is refused. So when someone else already has a `claude-code`, the form offers `claude-code-alice`.
- **What it may do**: the presets within your role. The strongest is chosen, or the strongest the client asked for. For an agent you reuse, none is stronger than the agent's own. You may choose less.
- **Machines**: all of them, or only the ones you tick. For an agent you reuse, only among its own.
- **For how long**: 1 day, 30 days or 90 days; 30 by default.

The foot of the page says where Beacon sends you back to: a `localhost` port on your machine, where Claude Code is listening.

- **Allow** makes the agent, or reuses yours as it is. It sends Claude Code a one-time code, which Claude Code exchanges for its tokens. `/mcp` then shows `beacon` connected.
- **Deny** tells Claude Code you said no.

Both are in the audit log (`oauth_allow`, `oauth_deny`), and allowing is echoed to the chat.

From then on Claude Code holds two tokens: an access token valid for an hour, and a refresh token valid for as long as you allowed. It refreshes by itself. When the time you allowed is up, it asks you to sign in again. No token is copied, stored by hand or pasted into a chat.

- **No browser at hand**, as over ssh: run `claude mcp login beacon --no-browser` in an interactive terminal. It prints the address to open elsewhere, then asks you to paste in the address you are sent back to.
- **Signing out**: `claude mcp logout beacon` ends the sign-in on Beacon's side too. `claude mcp remove beacon` alone forgets Claude Code's tokens without revoking them. So log out first, or revoke the sign-in on the agent's page.
- **A header wins**: a server added with `--header "Authorization: Bearer …"` uses that token and never signs in with OAuth. Such a token is one of your agents' credentials ([Headless agents and CI](headless.md#make-an-agent-and-its-token)).

Claude Code names itself with its client ID metadata document, `https://claude.ai/oauth/claude-code-client-metadata`. Beacon fetches it, so Claude Code registers nothing. It listens for the answer on `http://localhost:<port>/callback`.

What the agent may do follows from its preset and your role, read afresh on every request ([What a sign-in may do](../guide/people.md#what-a-sign-in-may-do)).

## Connect with your own ssh, scp and rsync

1. **Register the agent's public key.** Ask Claude Code to *"register my SSH key ~/.ssh/id_ed25519.pub with Beacon"*, and it calls `add_ssh_key` ([SSH keys](../guide/people.md#ssh-keys)).
2. **Get the machines.** `list_machines` (MCP) or `GET /api/v1/machines` returns the machines and whether the agent reaches each. It also returns an `ssh_config` block and a `known_hosts` line.
3. **Put the block in `~/.ssh/config`** and the line in `~/.ssh/known_hosts`:

   ```
   Host edge-1
     HostName ssh.example.com
     Port 17002
     User edge-1
     HostKeyAlias beacon
   ```

4. **Connect.** `ssh edge-1 'systemctl status relay'`, `scp`, `sftp`, `rsync -e ssh` and `ssh -L` work as usual.

The user name tells Beacon which machine. On the machine, the session runs as `beacon`. `HostKeyAlias beacon` makes ssh check Beacon's own host key, the same for every machine. Beacon checks each machine's pinned key itself.

When the channel refuses, the agent's ssh prints why and exits with 1. For example: `beacon: this agent may not reach the machine edge-2; machines this agent reaches: edge-1`.

[Troubleshooting](../self-hosting/troubleshooting.md#the-agent-cannot-reach-a-machine) lists the messages and what to do about each. More about the channel:

- What it records: [Give agents a machine](../guide/machines.md).
- How to pause it: [Stop an agent, pause everything](../guide/stop.md).
- What it guarantees: the [security model](../self-hosting/security.md#the-ssh-channel).
