# What is Beacon

Beacon is self-hosted monitoring and SSH access for the AI agents that run your servers. When a service breaks, it wakes your agent with the evidence, lets it onto the machine, and records everything it does.

- **Probes from where your users are.** Small probes check each service from several places and say why a check failed: DNS, connection, TLS, timeout or status code.
- **Wakes your agent.** Claude Code, Codex or anything that speaks MCP or HTTP gets the alert, and the evidence in one call.
- **No keys for the agent.** It reaches machines with the `ssh`, `scp` and `rsync` it already knows, through Beacon, and holds no password and no key.
- **Every session on record.** Each command the agent runs is recorded and can be replayed in a browser.
- **People and users kept informed.** The people on call get a card in the chat, and a phone call when nobody answers. Your users get a public status page.

## Who it is for

People and small teams who run their own machines and services with AI agents: from one person with a few agents and a dozen machines to a team that shares the work.

Beacon runs on your hardware. It keeps its state in one SQLite file by default, calls no model and needs no cloud account. Everything it watches, and everyone who may act, is yours.

## The three problems it solves

### The agent does not know that something broke

Monitoring tools page people and draw dashboards. An agent hears of an outage only when someone pastes the alert into a chat, and then it has to guess what the probes saw.

Beacon sends the alert to the agent as well: a signed webhook or a GitHub workflow wakes it. One call, `get_evidence`, returns what every location sees and why, the deployments just before, and what has already been done. After a fix, `probe_now` has every location check the service again at once. The alert resolves on what the probes see, not on what the agent says.

### To touch a server, it needs your keys

The usual way is to hand the agent an SSH key or a root password. From then on it can reach every machine they open, from anywhere, and nothing records what it runs.

Through Beacon's SSH channel the agent still uses its own `ssh`, `scp` and `rsync`. Beacon signs a certificate for each connection, valid for five minutes, and your servers trust only Beacon. You can revoke one agent, or stop them all with one switch.

### Afterwards, nobody can tell what happened

The agent's commands scroll past in a terminal nobody was watching, and the agent remembers nothing between sessions.

Beacon records every session and replays it in the browser. It splits an interactive shell into commands with their exit codes, and keeps the agent's notes on the alert. You review the work afterwards, instead of approving each step in advance.

## What Beacon is not

- **Not an agent, and it calls no model.** It wakes your agent and hands it the evidence; deciding what to do is the agent's work. `beacon-oncall` is the small harness that starts the agent program you name when a hook fires.
- **No approval workflow.** The pause switch and each machine's access are the only brakes. People review afterwards, on the record.
- **No log platform, tracing, APM or dashboards.** An agent reads logs through the SSH channel, with `journalctl` and the tools it already knows.
- **No time-series database of its own.** VictoriaMetrics stores and queries the probes' results; vmalert evaluates the rules.
- **No remote desktop and no deployment system.** Flux and its like stay; Beacon takes their change events.
- **Nothing that only one deployment needs.** What is particular to where you run Beacon is configuration, kept outside the repository.

[Goals](../about/goals.md) has the full version, with the reasoning and the order the goals come in.

## Three ways to run it

| Where | What you get | Start with |
| --- | --- | --- |
| Your laptop | The whole loop in ten minutes, with no domain and no certificate | [Try it on your laptop](quickstart.md) |
| A server | Real use: two DNS names point at it; 2 CPUs and 4 GB is ample | [On a server](../self-hosting/install.md) |
| Probe locations | Checks from near your users, and the machines your agents work on | [Probe locations](../self-hosting/probes.md) |

[Deployment options](../self-hosting/index.md) compares the three.

## Where to go next

Read these two in order:

1. [Try it on your laptop](quickstart.md): the whole loop, from a broken service to a fixed one, in ten minutes.
2. [How Beacon works](concepts.md): the picture, the words, and the paths an alert and an agent take.

Then go by what you want to do:

- [Using Beacon](using-beacon.md): a page for each task.
- [Integrations](../integrations/index.md): the agent clients, and the hooks, change events and chat around an alert.
- [Self-hosting](../self-hosting/index.md): installing Beacon on a server and keeping it running.
- [Reference](../reference/index.md): every route, tool, command, key and scope.
- [About](../about/index.md): the goals, the architecture and the changelog.
