# Try it on your laptop

In ten minutes, on one machine, you break a service and watch an agent repair it through Beacon. There is nothing to configure first.

You start Beacon in Docker beside a probe location and a demo machine. You make your account, connect Claude Code and break the demo service. Then an agent reads the alert's evidence, repairs the service through Beacon's SSH channel, verifies the repair and leaves its note. Every command it ran is recorded for you to replay.

The try environment lives at [`deploy/try`](https://github.com/openlinkos/beacon/tree/main/deploy/try) in the repository. Everything is served from `http://localhost:17001`, on plain http, without a domain or a certificate.

**You need:**

- Docker with Compose v2.
- `ssh-keygen` (the OpenSSH client).
- About 2 GB of memory to spare.

Nothing else: no domain, no certificate and no cloud account. Beacon calls no model.

> [!TIP]
> **Hand it to your agent.** Paste this into Claude Code and let it do the steps:
>
> ```text
> Read https://beacon.openlinkos.com/guide/quickstart.md and set up a Beacon trial on this machine. Start at "Start the environment" and run every command as written; stop after "Ask what is alerting" and report the summary and the console address. Leave "Make your account" to me.
> ```

## Start the environment {#start}

```bash
git clone https://github.com/openlinkos/beacon.git
cd beacon/deploy/try
sh up.sh
```

On Windows, run it from Git Bash. It is safe to run again. `up.sh`:

- makes the throwaway secrets: the rules token, the SSH channel's host and certificate-authority keys, and the demo machine's host key;
- starts five containers: Beacon, VictoriaMetrics, vmalert, the probe location `edge-1` and the demo machine;
- enrols the probe and pins the demo machine's host key;
- prints the link that makes the first account.

After a minute or two of pulling and starting, you will see:

```text
Beacon's try environment is up (…s):
  http://localhost:17001          the API (/api/v1), MCP (/mcp) and the console (/console)
  http://localhost:18080          the demo service this environment watches

Make the first account (an owner): open this one-time link within 30 minutes:
  http://localhost:17001/console/setup?code=…
```

If it goes wrong, `docker compose logs beacon` says why. [Troubleshooting](../self-hosting/troubleshooting.md) has the common cases.

## Make your account {#account}

Open the link, and choose a username, a display name and a password. That account is the owner. You are now signed in to the console at `http://localhost:17001/console`, with the overview, the alerts, the machines and the sessions.

The link works once. If it expires, `docker compose exec beacon beacon setup` prints a new one.

## Connect Claude Code {#connect}

1. **Add Beacon to Claude Code.**

   ```bash
   claude mcp add --transport http beacon http://localhost:17001/mcp
   ```

2. **Sign in.** In Claude Code, type `/mcp` and choose `beacon`. Your browser opens Beacon's consent page, **Claude Code wants to use Beacon as you**. You are signed in already.
3. **Allow it as one of your agents.** Take the name it suggests, the operator preset, all machines and 30 days, and press Allow.

Back in Claude Code, `beacon` shows connected.

Plain http on localhost is fine here. The console's cookie is `Secure`, which a browser allows on localhost. Claude Code, too, sends credentials to a token endpoint on localhost.

## Ask what is alerting {#ask}

In Claude Code:

```text
What is alerting on Beacon right now?
```

The agent calls `status_summary` and answers with what it returns. Nothing is firing: the demo service is up, the probe location reports, and alerts could reach people.

This is the agent's normal view. The same question works at 3 a.m., and then the answer is the alert you are about to make.

## Break the demo service {#break}

```bash
docker compose exec demo stop-web
```

You will see `the demo web server is stopped`. The probe finds out on its next round, every 30 seconds. Within about two minutes of stopping the service, the rule engine has it down. The alert group then exists, titled **demo-web is down**.

No chat is configured in the try environment, so nobody is notified. The alert is in Beacon, which is where the agent looks.

Ask the agent, in Claude Code:

```text
demo-web is down. Use Beacon: get the evidence, repair the service on the machine through the SSH channel, verify the fix with probe_now, and leave a note on the alert.
```

You will see the agent work the loop Beacon exists for:

1. `get_evidence` returns the alert with the probe's result: location `edge-1`, reason `connect`, connection refused. The agent leaves a first note.
2. `run_command` on the machine `demo` finds the web server down, and starts it again with `start-web`. The connection uses a five-minute certificate, and the agent never holds a key.
3. `probe_now` makes the location probe the target again at once. The result comes back a success.
4. `add_note` leaves what was wrong, what the agent did, and that nothing remains for a person.

## Watch it recover, and replay what the agent did {#replay}

Open the console at `http://localhost:17001/console`. You have been signed in since step 2.

- **Alerts** → the *demo-web is down* group: resolved a few moments after the repair. Its timeline shows the agent's notes.
- **Sessions** → the sessions on `demo`: the replay shows the work, command by command, as it happened.

![The alert group after the repair: the timeline with the agent's notes, the probe recovered, the sessions that touched the machine](/media/console-alert-light.png)

![The session's replay: what the agent ran, and what came back](/media/console-replay-light.png)

A recording of a repair like this one is at `/media/agent-repairs-demo-web.cast`. It is the agent's session as an asciicast, which is what the console's replay plays, command by command.

## Clean up {#clean}

```bash
docker compose down -v
```

This removes the containers, the volumes (Beacon's database, the metrics, the recordings) and the network. The throwaway keys stay in `deploy/try/secrets/`. Delete the directory if you like.

## Check yourself {#check}

- The setup link opened **Set Beacon up**. The console's overview shows one probe location reporting.
- `/mcp` in Claude Code lists `beacon` as connected. The agent answered the alerting question with Beacon's own summary.
- After `stop-web`, the alert appeared within about two minutes. After the agent's repair, the group was resolved with the probes' agreement.

## Next {#next}

- [On a server](../self-hosting/install.md): put Beacon on a server, with TLS and a real probe location.
- [Put an agent on call](on-call.md): have alerts wake the agent by themselves.
- [Give agents a machine](machines.md): see what an agent may and may not do on a machine.
- [Integrations](../integrations/index.md): more clients, such as Codex, Cursor and headless agents.
