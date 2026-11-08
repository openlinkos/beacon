# What is Beacon

Beacon is self-hosted infrastructure for AI agents, shared with the people who manage them. Agents get credentials, machines and monitoring through it, and people use it for the same jobs. Everything either does is on the record.

- **A team password manager.** People keep and share passwords, keys and tokens. An agent uses one by grant, and can use it without seeing it.
- **Trusted access for agents.** Each agent is an identity that acts for a person, within that person's role. Claude Code and Codex sign in through the browser.
- **An SSH bastion.** Agents reach machines with the `ssh`, `scp` and `rsync` they already know, and hold no password and no key.
- **Monitoring from where your users are.** Probes check each service from several places and say why a check failed. An alert wakes your agent with the evidence in one call.
- **For what agents build, too.** An agent puts a service it built under the probes itself, through the same API.
- **Every action on record.** Each command an agent runs is recorded and replays in a browser. Each change is traced to who made it, for whom, and why.
- **People in the same place.** The people on call get a card in the chat, and a phone call when nobody answers. Your users get a public status page.

The password manager, and views of a service's own metrics, are in development. [Goals](../about/goals.md) says where Beacon is going.

## Who it is for

People and teams who run their own machines and services with AI agents: from one person with a few agents and a dozen machines to a team that shares its passwords and its work.

Beacon runs on your hardware. It keeps its state in one SQLite file by default, calls no model and needs no cloud account. Everything it holds and watches, and everyone who may act, is yours.

## Shared by agents and people

An agent cannot close the loop alone. A person decides what it may use, and does the same jobs. So each part of Beacon is built for both: for agents in the ways they already work, and for people at least as well as the tool they use for that job today.

## The problems it solves

### To call a service, it needs your passwords

To call an API or open a database, the agent needs a token or a password. Usually someone pastes one into the chat. From there it can end up in a transcript, a prompt or a repository, and nobody can say later which agent used it, or why.

Beacon keeps the team's credentials, as a password manager does. An agent asks once and is granted a secret. It then uses the secret as often as the grant allows, each use recorded with its reason. The people who grant it keep their own passwords in the same place.

### To touch a server, it needs your keys

The usual way is to hand the agent an SSH key or a root password. From then on it can reach every machine they open, from anywhere, and nothing records what it runs.

Through Beacon's SSH channel the agent still uses its own `ssh`, `scp` and `rsync`. Beacon signs a certificate for each connection, valid for five minutes, and your servers trust only Beacon. You can revoke one agent, or stop them all with one switch.

### The agent does not know that something broke

Monitoring tools page people and draw dashboards. An agent hears of an outage only when someone pastes the alert into a chat, and then it has to guess what the probes saw.

Beacon sends the alert to the agent as well: a signed webhook or a GitHub workflow wakes it. One call, `get_evidence`, returns what every location sees and why, the deployments just before, and what has already been done. After a fix, `probe_now` has every location check the service again at once. The alert resolves on what the probes see, not on what the agent says.

### Afterwards, nobody can tell what happened

The agent's commands scroll past in a terminal nobody was watching, and the agent remembers nothing between sessions.

Beacon records every session and replays it in the browser. It splits an interactive shell into commands with their exit codes, and keeps the agent's notes on the alert. You review the work afterwards, instead of approving each step in advance.

The same record covers each change to the configuration: who made it, for whom, and why.

## What Beacon is not

- **Not an agent, and it calls no model.** It wakes your agent and hands it the evidence; deciding what to do is the agent's work. `beacon-oncall` is the small harness that starts the agent program you name when a hook fires.
- **No approval of what agents run.** People review afterwards, on the record. The one thing approved is a credential, once per grant. On machines, the pause switch and each machine's access are the brakes.
- **No log platform, tracing or APM.** An agent reads logs through the SSH channel, with `journalctl` and the tools it already knows.
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
