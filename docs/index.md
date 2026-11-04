---
layout: home
title: Put your AI agent on call
---

<OlHero command="git clone https://github.com/openlinkos/beacon.git && cd beacon/deploy/try && sh up.sh">
  <template #title>Put your AI agent on call</template>
  <template #lead>Beacon checks your services from where their users are, wakes an agent with the evidence when one breaks, lets it reach the machine without ever holding a key, and records every command it runs.</template>
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

The recording above is the loop the try environment runs on your machine: you break the demo service, and the alert, the repair, the check and the replay follow. The five steps below are the same loop; each panel beside a step is what that step returns, and the last two are the console’s own pages. At 3 a.m. they run with nobody watching.

<OlSteps :steps="[
  { title: 'Several locations see it fail', text: 'A probe of under 20 MB runs on any small machine and checks each service the way its users reach it, recording why a check fails. A service is down only when several locations agree, so one flaky location wakes nobody.', media: '/landing/step-probes.svg', alt: 'The evidence a failing service produces: the service, the target, the location and the reason' },
  { title: 'The alert wakes an agent, with the evidence', text: 'A signed webhook or a GitHub workflow wakes the agent you choose. One call returns what every location sees, the other alerts in the same failure domain, the deployments just before and what has already been done.', media: '/landing/step-wake.svg', alt: 'The one call that hands the agent the evidence: the probes, the related alerts, the changes and what was done' },
  { title: 'The agent reaches the machine without a key', text: 'It uses its own ssh, scp and rsync through Beacon, or the MCP tools for running commands and editing files. Each connection gets a certificate valid for five minutes, and the machines trust only Beacon.', media: '/media/console-replay-light.png', alt: 'The session replay in the console: what the agent ran, and what came back' },
  { title: 'probe_now checks the fix', text: 'The agent asks every location to probe the target again at once, and sees each answer within the wait. The alert resolves on what the probes see, not on what the agent says.', media: '/landing/step-probe-now.svg', alt: 'Every location probes the target again at once, and each answer comes back' },
  { title: 'The alert resolves; you replay the session', text: 'The group resolves once the probes recover, the agent notes left on its timeline. In the console every session replays in the browser, command by command.', media: '/media/console-alert-light.png', alt: 'The alert group after the repair: the timeline with the agent notes, the probe recovered' },
]" />

## Three pillars {#three-pillars}

### Checked from where your users are {#checked-from-where-your-users-are}

Probes check HTTP, TLS, TCP, container registries, Git and domain expiry, and say why a check fails: DNS, connection, TLS, timeout, status code. Targets and modules live in Beacon, so changing what is checked needs no redeployment. [Monitor a service](/guide/monitoring).

### The agent never holds a key {#the-agent-never-holds-a-key}

Each connection gets a certificate valid for five minutes, and the machines trust only Beacon. Revoke one agent, or stop them all with one switch. [Give agents a machine](/guide/machines).

### Every command on record {#every-command-on-record}

Sessions replay in the browser, interactive shells are split into commands with their exit codes, and the agent's notes land on the alert. You review what was done, instead of approving each step in advance. [Review what agents did](/guide/console).

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
