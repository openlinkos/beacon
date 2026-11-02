<h1>
  <img alt="Beacon · 烽火" src="https://openlinkos.com/brand/beacon/github/readme.svg" height="56">
</h1>

**Self-hosted monitoring and SSH access for the AI agents that run your servers.**

[![CI](https://github.com/openlinkos/beacon/actions/workflows/ci.yml/badge.svg)](https://github.com/openlinkos/beacon/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/openlinkos/beacon?include_prereleases&sort=semver)](https://github.com/openlinkos/beacon/releases)
[![Licence: Apache-2.0](https://img.shields.io/badge/licence-Apache--2.0-blue.svg)](LICENSE)

English | [简体中文](README.zh-CN.md) · Documentation: [beacon.openlinkos.com](https://beacon.openlinkos.com)

Beacon checks your websites and services from several places at once, the way your users reach them, and says why a check fails: DNS, connection, TLS, timeout, status code. When a service goes down, it wakes your AI agent (Claude Code, Codex, or anything that speaks MCP or HTTP) and hands it the evidence in one call: what every location sees, what changed just before, what has already been done. The agent logs in to your servers through Beacon with its own `ssh`, or with Beacon's tools for running commands and editing files, fixes what it can, has every location check again, and leaves a note on the alert. It never holds a password or a private key, and every command it runs is recorded and can be replayed in a browser. Your users see a status page; the people on call get a card in Feishu and, when nobody answers, a call on their phone.

> **Status: early (0.3, release candidates).** Probing, alert routing, Feishu cards and phone escalation, the SSH channel with its recordings, waking agents, evidence, probing on demand, the console and public status pages work end to end, and run the makers' own servers. Until 1.0 the configuration may change between minor versions; the [changelog](CHANGELOG.md) says what to do when it does.

## Why

AI coding agents are already good at operations work: reading logs, finding the wrong line in a configuration, restarting the right service. What keeps people from letting them do it is everything around the work.

- **The agent does not know that something broke.** Monitoring tools page people and draw dashboards. An agent hears of an outage only when someone pastes the alert into a chat, and then has to guess what the probes saw.
- **To touch a server, it needs your keys.** The usual way is to give the agent your SSH key or a root password. From then on it can reach every machine they open, from anywhere, and nothing records what it ran.
- **Afterwards, nobody can tell what happened.** The agent's commands scrolled past in a terminal nobody was watching. Reviewing them means asking the agent, which remembers nothing between sessions.

Beacon is built for the agent to do the work and for people to see all of it:

- **Checked from where your users are.** A probe of under 20 MB, on any small machine, checks HTTP, TLS, TCP, container registries, Git and domain expiry, and records why each check failed. A service is down only when several locations agree, so one location's bad network wakes nobody.
- **The agent is woken with the evidence.** A signed webhook or a GitHub workflow wakes the agent you choose. One call, `get_evidence`, returns each location's result, the other alerts in the same failure domain, the deployments just before, and what people and agents have done so far.
- **Server access without handing over keys.** Agents use their own `ssh`, `scp` and `rsync` through Beacon, or its MCP tools. Beacon signs a certificate valid for five minutes for each connection, and your servers trust only Beacon. Revoke one agent, or stop them all with one switch.
- **Every command on record.** Sessions are recorded and replay in the browser, interactive shells are split into commands with their exit codes, and the agent's notes land on the alert. You review what was done afterwards instead of approving each step in advance.
- **Fixed means checked again.** After a fix the agent asks every location to probe the service at once (`probe_now`) and sees their answers within a minute.
- **People stay in the loop.** A public status page for your users, a console for your team, alert cards in Feishu that can be acknowledged and silenced, and escalation to phones through Pushover when nobody answers.
- **Small, and yours.** One Go service and one probe binary, on VictoriaMetrics; a machine with 2 CPUs and 4 GB is plenty. Apache-2.0. Beacon calls no model and needs no cloud account.

## Features

- **MCP server** at `/mcp` with 43 tools: alerts (`list_alerts`, `get_alert`, `ack_alert`, `silence_alert`, …), working an alert (`get_evidence`, `get_timeline`, `add_note`, `list_changes`, `probe_now`), machines (`list_machines`, `run_command`, `read_file`, `write_file`, `edit_file`, `list_sessions`, `get_session`, …), maintenance and status pages, the configuration, people and the agent's own SSH keys (`list_users`, `invite_user`, `add_ssh_key`, …), and the agent's own token (`whoami`, `renew_token`). Claude Code and Codex sign in to it through the browser, with MCP's OAuth. Every tool has the same REST route under `/api/v1`.
- **The SSH channel** on port 17002: `ssh`, `scp`, `sftp`, `rsync` and `ssh -L` to any enrolled machine, the user name naming the machine, also machines behind NAT that connect out with plain `ssh -R`.
- **Probes** (`beacon-probe`): Blackbox-compatible checks with failure reasons, host metrics and heartbeats, pushed through a queue on disk when the network is down; targets and modules come from the centre, so changing what is checked needs no redeployment.
- **Alerting** without Alertmanager: grouping, inhibition, silences and a dead man's switch inside the service; vmalert posts its alerts to Beacon as it would to Alertmanager.
- **Feishu cards** that change in place: what is wrong, since when, which locations fail and why, the agent's notes; buttons to acknowledge and silence; escalation to the Feishu app's urgent notice and to Pushover's emergency priority.
- **Waking agents**: signed webhooks and GitHub workflow dispatches for new alert groups, and `beacon-oncall`, a small harness that verifies the webhook and starts the agent program you name (wrappers for Claude Code and Codex included).
- **Public status pages** served by Beacon itself: components from the probe targets, 90 days of history, incidents from the alerts, maintenance windows, JSON, an Atom feed and badges.
- **The console** at `/console` (the site's root is the public status page): alerts with their evidence and timeline, machines, every session with its replay, people and their agents with their tokens, OAuth sign-ins and keys, the configuration and its history, the audit log.
- **Configuration through Beacon**: probe locations, targets, modules and status pages live in its database, changed through the API, MCP or the command line, every change a version that can be reverted; files are for import and export, so they can stay in git.

## Works with

| Agent | Connects over | Can be woken by Beacon |
|---|---|---|
| Claude Code | MCP over HTTP, its own `ssh` | ✅ with `beacon-oncall` (headless) or a GitHub workflow |
| Codex CLI | MCP over HTTP, its own `ssh` | ✅ with `beacon-oncall` |
| Any MCP client | MCP over HTTP | ✅ with a webhook |
| Bots and scripts | REST and `ssh` | ✅ with a webhook |

Beacon calls no model: what to do about an alert is the agent's decision, and every agent is welcome. See [Put an agent on call](https://beacon.openlinkos.com/agents).

## Getting started

**Requirements:** a Linux machine with 2 CPUs and 4 GB of memory, Docker with Compose v2, and two DNS names pointing at it.

```bash
git clone https://github.com/openlinkos/beacon.git
sudo cp -r beacon/deploy /opt/beacon && cd /opt/beacon
sudo ./install.sh        # asks for the two host names and an e-mail address for the certificates
```

`install.sh` generates the secrets, validates every file, starts Beacon, VictoriaMetrics, vmalert and Caddy, and prints what to do next. [Up in 30 minutes](https://beacon.openlinkos.com/install) walks through the rest: the first probe, the first machine, the first agent, and a rehearsed outage from alert to recovery.

### Connect Claude Code

```bash
docker exec beacon beacon setup    # a one-time link: open it and choose your username and password
claude mcp add --scope user --transport http beacon https://status.example.com/mcp
```

Then run `/mcp` in Claude Code and sign in through the browser: Beacon asks you to allow Claude Code as one of your agents, with what it may do and for how long, and no token is copied anywhere. Then ask, in any session: *"Is anything failing right now? Look at the evidence, check the machine, and tell me what you would do."* The agent answers from `get_evidence` and the machine itself, and everything it ran is in the console under Sessions. An agent without a browser takes a token instead: `docker exec beacon beacon agent add oncall-bot -owner alice -preset oncall`.

## Documentation

The documentation lives in [`docs/`](docs/) and is published at [beacon.openlinkos.com](https://beacon.openlinkos.com):

- [Up in 30 minutes](docs/install.md): from a bare machine to the first probe, machine, agent and drill
- [People and their agents](docs/accounts.md): accounts and roles, connecting Claude Code and Codex through the browser, headless agents and their tokens
- [Put an agent on call](docs/agents.md): waking an agent, the evidence, notes, the timeline, probing on demand, `beacon-oncall`
- [The SSH channel](docs/ssh.md): how agents reach machines without keys, recordings, machines behind NAT
- [Catalogue, targets and modules](docs/catalog.md): what is probed, from where, and how it is configured
- [Status pages](docs/status.md): the public page, incidents, maintenance, badges
- [The console](docs/pages.md): what people see, and the links cards open
- [Feishu](docs/feishu.md): the app, the cards, the group bot
- [API](docs/reference/api.md) and [MCP tools](docs/reference/mcp.md): every route and tool, with scopes and error codes
- [Architecture](docs/architecture.md) and [goals](docs/goals.md): how it fits together, what it is for and what it leaves out
- [Changelog](CHANGELOG.md)

## How it works

```
beacon-probe (each location) ──results──▶ gateway ──▶ VictoriaMetrics ◀── vmalert (rules)
CI, Flux ──change events──▶ gateway                                          │ alerts
                                                                             ▼
people ◀── Feishu cards, phones, status page ◀── beacon (router, bridge) ──▶ agents' webhooks
agents ──▶ MCP and REST (evidence, notes, probe_now) ──▶ beacon
agents ──▶ ssh, scp, rsync (port 17002) ──▶ SSH channel ──▶ your servers (5-minute certificates, every session recorded)
```

One service, `beacon`, holds the gateway for machines, the alert router, the bridge to people and agents, the API and MCP, the console and status pages, and the SSH channel; its state is one bbolt file, with the session recordings beside it. Metrics and rules stay in VictoriaMetrics and vmalert. [Architecture](docs/architecture.md) has the parts.

## Security

Agents never see a password or a private key: the SSH channel signs a short-lived certificate for every connection and the machines trust only its certificate authority. People sign in with Beacon's own accounts, their passwords kept as argon2id hashes; there is no external sign-in. An agent acts for a person and never does more than that person's role allows, checked on every request. Tokens are stored as SHA-256 hashes, carry scopes and expire; each probe and host can write only as itself. Everything an agent does is audited and echoed to the alert chat. Secrets are read from files and never logged. Report vulnerabilities as [SECURITY.md](SECURITY.md) describes.

## Contributing

Issues and pull requests are welcome; [CONTRIBUTING.md](CONTRIBUTING.md) explains how to build, test and propose a change, and [AGENTS.md](AGENTS.md) is the guide for AI agents working on the code.

## Licence

[Apache License 2.0](LICENSE). The LinkOS and OpenLinkOS names and marks are not covered by it.

Beacon is named after the beacon towers of the Great Wall: when a watch post saw trouble it lit a fire, the signal ran from tower to tower to the capital, and the number of fires told how serious it was.
