# Stop an agent, pause everything

By the end of this page you know every brake Beacon has and what each one closes: the switches that stop one agent — a revoked token, a revoked sign-in, a removed key, a narrowed preset, a disabled owner — and the one that stops them all, the channel's pause. None of them needs luck: what an agent may do is worked out afresh on every request, and what it has open on machines is closed at once when the answer changes.

You need an owner account, or a person whose role gives `machines:write` for the pause switch ([Invite your team](people.md)); the commands run on the centre, as `docker exec beacon beacon …` in the reference deployment.

## Stop one agent {#stop-one-agent}

Pick the switch by how much you want to stop:

| Switch | How | What closes |
| --- | --- | --- |
| Revoke a token | **Revoke** on the agent's page, or `beacon token revoke NAME` (the console's **Tokens** page, for any token that reaches machines) | The token is refused from its next request on; what was opened with it — connections, forwarded ports, tool calls — closes at once, and the keys the agent registered with it go |
| Revoke an OAuth sign-in | **Revoke** on the agent's page, or the client logging out (`claude mcp logout beacon`) | The same, for what that sign-in opened; the client has to sign in again |
| Remove an SSH key | `remove_ssh_key`, or the agent's page | The connections signed in with that key close |
| Delete the agent | `beacon agent remove NAME`, or its page | Every credential of its — tokens, sign-ins, keys — and what they had open; what it did stays on record |
| Narrow the agent | Its page or `beacon agent` on the centre: take a machine from its list, or lower its preset below `machines:ssh` | What it has open on the machines it may no longer reach closes; the rest keeps working |
| Stop the owner | `beacon user disable NAME`, or `beacon user role NAME viewer` ([Change a role, disable someone](people.md#change-a-role-disable-someone)) | Their sign-ins end, their agents stop, and what those agents have open on machines closes |

Revoking a token also ends the console sessions signed in with it — revoking the token you signed in with signs you out. What the agent did — sessions, recordings, notes — stays for `ssh.keep` (180 days); stopping an agent never deletes its record.

Beacon also checks every minute for credentials that expired meanwhile, so a token past its expiry closes what it has open without anyone asking.

## Pause the whole channel {#pause-the-whole-channel}

To stop every agent at once — a compromise, a change freeze, a night off — pause the SSH channel. It closes every open session at once and refuses new ones until it is resumed, from any of the three places:

```bash
docker exec beacon beacon ssh pause     # or: ssh resume
```

You will see `agents' access to machines is paused since …` (`ssh status` says which, and so does the switch above the machines on the console's **Machines** page). While paused, an agent's own ssh gets `beacon: agents' access to machines is paused since …; a person resumes it with beacon ssh resume`, and the MCP tools and the API are refused with the same words. Monitoring is not touched: the probes keep reporting, alerts keep arriving, and a pause is recorded in the audit log. The API route is `POST /api/v1/ssh/pause` and `/resume`, the MCP tool `pause_ssh` — both `machines:write`, and both echo to the alert chat, so a paused channel does not go unnoticed.

The pause is the brake for the whole fleet; the switches above stop one agent without stopping the others.

## What closes, and when {#what-closes-and-when}

Everything above works the same way underneath: Beacon re-reads who may do what — the token's or sign-in's scopes, the agent's preset, the owner's role, the machine list — and closes what the answer no longer allows (connections, forwarded ports, tool calls) at once, without waiting for the agent to ask anything. An agent in the middle of a command has its answer cut short; one that tries the next command is refused, with the reason in the refusal.

Nothing here stops the recording or the audit: a session closed by a switch keeps its record and its replay, and the switch used is in the audit log — `revoke_token`, `ssh_pause`, `user_disable` — with who pulled it.

## Check it

Pause the channel and ask an agent to run a command: it is refused with the words above, and the **Machines** page and `beacon ssh status` say paused since when. Resume, then revoke a token an agent holds and watch its open session close on the **Sessions** page, marked ended, with its replay intact. When an agent cannot reach a machine for another reason, [The agent cannot reach a machine](../self-hosting/troubleshooting.md#the-agent-cannot-reach-a-machine) lists every message and what to do about it.

## Next

- [Invite your team](people.md): the roles and presets the switches narrow.
- [Give agents a machine](machines.md): the channel a pause closes, and what it records meanwhile.
- [Review what agents did](console.md): the sessions a stopped agent leaves behind, replay included.
