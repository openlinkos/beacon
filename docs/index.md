---
layout: home
title: Infrastructure for AI agents, on the record
---

<OlHero command="git clone https://github.com/openlinkos/beacon.git && cd beacon/deploy/try && sh up.sh">
  <template #title>Infrastructure for AI agents, on the record</template>
  <template #lead>Your agents reach your machines and watch your services through Beacon, and soon take the credentials they are granted there too. Your team uses the same place for the same jobs, and everything either does is on the record.</template>
  <template #actions>
    <a href="/guide/quickstart">Quickstart</a>
    <a class="ghost" href="/guide/concepts">How it works</a>
    <a class="ghost" href="https://github.com/openlinkos/beacon">GitHub</a>
  </template>
  <template #meta>Ten minutes, one command, Docker on this machine — no domain or certificate, and no cloud account.</template>
  <template #media>
    <OlCast src="/media/agent-repairs-demo-web.cast" player="/landing/asciinema-player.min.js" player-css="/landing/asciinema-player.css" poster="/landing/cast-poster.svg" alt="A terminal where an agent repairs the demo web server: the health check is refused, the agent runs start-web, and the health check answers ok" caption="Recorded in the try environment: an agent repairs the demo service through the SSH channel, with the tools it already knows." />
  </template>
</OlHero>

## One outage, end to end {#one-outage}

The recording above is one loop these parts make possible. In the try environment you break a demo service; the alert, the repair, the check and the replay follow. Here are its five steps. At 3 a.m. they run with nobody watching.

<OlSteps :steps="[
  { title: 'Several locations see it fail', text: 'A probe of under 20 MB runs on any small machine and checks each service the way its users reach it, recording why a check fails. A service is down only when several locations agree, so one flaky location wakes nobody.', media: '/landing/step-probes.svg', alt: 'The evidence a failing service produces: the service, the target, the location and the reason' },
  { title: 'The alert wakes an agent, with the evidence', text: 'A signed webhook or a GitHub workflow wakes the agent you choose. One call returns what every location sees, the other alerts in the same failure domain, the deployments just before and what has already been done.', media: '/landing/step-wake.svg', alt: 'The one call that hands the agent the evidence: the probes, the related alerts, the changes and what was done' },
  { title: 'The agent reaches the machine without a key', text: 'It uses its own ssh, scp and rsync through Beacon, or the MCP tools for running commands and editing files. Each connection gets a certificate valid for five minutes, and the machines trust only Beacon.', media: '/media/console-replay-light.png', alt: 'The session replay in the console: what the agent ran, and what came back' },
  { title: 'probe_now checks the fix', text: 'The agent asks every location to probe the target again at once, and sees each answer within the wait. The alert resolves on what the probes see, not on what the agent says.', media: '/landing/step-probe-now.svg', alt: 'Every location probes the target again at once, and each answer comes back' },
  { title: 'The alert resolves; you replay the session', text: 'The group resolves once the probes recover, the agent notes left on its timeline. In the console every session replays in the browser, command by command.', media: '/media/console-alert-light.png', alt: 'The alert group after the repair: the timeline with the agent notes, the probe recovered' },
]" />

## One place for agents and people {#one-place}

Each part does a job people already have a tool for. Agents use it in the ways they already work, and people at least as well as that tool.

### A team password manager {#a-team-password-manager}

People keep and share passwords, keys and tokens. An agent uses one by grant, can use it without seeing it, and each use is recorded with its reason. In development: [Goals](/about/goals) describes it.

### Trusted access for agents {#trusted-access-for-agents}

Each agent is an identity that acts for a person, within that person's role. Claude Code and Codex sign in through the browser; a headless agent takes a token that renews itself. [Invite your team](/guide/people).

### An SSH bastion: the agent never holds a key {#the-agent-never-holds-a-key}

Each connection gets a certificate valid for five minutes, and the machines trust only Beacon. Revoke one agent, or stop them all with one switch. [Give agents a machine](/guide/machines).

### Monitoring, checked from where your users are {#checked-from-where-your-users-are}

Probes check HTTP, TLS, TCP, container registries, Git and domain expiry, and say why a check fails: DNS, connection, TLS, timeout, status code. Targets live in Beacon, so an agent can put a service it built under the probes itself. [Monitor a service](/guide/monitoring).

### Every action on record {#every-command-on-record}

Sessions replay in the browser, interactive shells are split into commands with their exit codes, and the agent's notes land on the alert. Each change is traced to who made it, for whom, and why. You review what was done, instead of approving each step in advance. [Review what agents did](/guide/console).

## Works with the tools agents already know {#works-with}

Sign Claude Code or Codex in through the browser with MCP's OAuth, point any MCP client at `/mcp`, or wake a headless agent with a signed webhook from CI.

<OlLogos label="Agent clients, and the ways one is woken" :items="[
  { name: 'Claude Code', href: '/integrations/claude-code' },
  { name: 'Codex', href: '/integrations/codex' },
  { name: 'Cursor', href: '/integrations/cursor' },
  { name: 'any MCP client', href: '/integrations/mcp-clients' },
  { name: 'Webhooks', href: '/integrations/webhooks' },
  { name: 'GitHub Actions', href: '/integrations/github-actions' },
  { name: 'Feishu', href: '/integrations/feishu' },
]" />

## The documentation {#docs}

| Part | What is in it |
| --- | --- |
| [Guide](/guide/) | What Beacon is, a ten-minute trial on your laptop, how it works, then [a page for each task](/guide/using-beacon): monitoring and alerts, agents on call, machines and the SSH channel, the team and permissions |
| [Integrations](/integrations/) | Claude Code, Codex, Cursor and other agent clients; webhooks, GitHub Actions, change events and Feishu |
| [Self-hosting](/self-hosting/) | Installing on a server, probe locations, configuration, upgrades, backups and security |
| [Reference](/reference/) | Every route, tool, command, configuration key and scope |
| [About](/about/) | The goals, the architecture and the changelog |

## Self-hosted, and open source {#self-hosted}

<OlBand>
  <template #title>Run it on one machine</template>
  <template #text>A Linux machine with 2 CPUs and 4 GB is plenty. Apache-2.0; Beacon calls no model and needs no cloud account.</template>
  <template #actions>
    <a href="/self-hosting/install">On a server</a>
    <a class="ghost" href="https://github.com/openlinkos/beacon">GitHub</a>
  </template>
</OlBand>

On a server, with TLS and two host names:

```bash
git clone https://github.com/openlinkos/beacon.git
sudo cp -r beacon/deploy /opt/beacon && cd /opt/beacon
sudo ./install.sh
```

## Running now {#running-now}

Beacon runs the operations of its makers. The public status page of the LinkOS services, [status.linkos.io](https://status.linkos.io), is served by Beacon: the state of each service, 90 days of history and the incidents, the same page your users would read.
