---
layout: home

hero:
  name: Beacon
  text: Infrastructure for AI agents that run operations
  tagline: It sees whether your services work for their users, reaches every machine without handing an agent a key, and puts the people on call and their agents on one platform, all in the ways agents already work.
  image:
    light: /logo.svg
    dark: /logo-dark.svg
    alt: Beacon
  actions:
    - theme: brand
      text: Up in 30 minutes
      link: /install
    - theme: alt
      text: Goals
      link: /goals
    - theme: alt
      text: GitHub
      link: https://github.com/openlinkos/beacon

features:
  - title: Sees
    details: Probes check your services from where your users are and say why each check fails. An outage is what several locations agree on, so one location's hiccup pages nobody, and one call hands an agent the evidence.
    link: /catalog
    linkText: What is monitored
  - title: Reaches
    details: Agents reach every enrolled machine, behind NAT too, with their own ssh, scp, rsync and port forwarding, or with MCP tools. Beacon signs a certificate valid for five minutes for each connection and records every session; no agent holds a key.
    link: /ssh
    linkText: The SSH channel
  - title: Connects
    details: One token works for SSH, REST and MCP. A new alert wakes an agent, one timeline joins alerts, sessions, commands and notes, and people see all of it in the chat, replay it, and can pause agents at once.
    link: /agents
    linkText: Agents and alerts
---

## How it works

One service, `linkos-beacon`, stands between the machines that report, the people on call and the agents that do the work. Metrics and rules stay in VictoriaMetrics and vmalert; Beacon calls no model.

<div class="bd-wrap">
<svg class="bd" viewBox="0 0 960 400" role="img" aria-labelledby="bd-title">
  <title id="bd-title">How Beacon fits: probes, CI and Flux report to Beacon, which keeps metrics and rules in VictoriaMetrics and vmalert, sends cards and phone calls to the people on call, wakes agents, and lets them reach machines through its SSH channel.</title>
  <defs>
    <marker id="bd-arrow" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="10" markerHeight="10" markerUnits="userSpaceOnUse" orient="auto">
      <path class="bd-line" d="M2 1.5 L8 5 L2 8.5" />
    </marker>
  </defs>
  <rect class="bd-box" x="1" y="28" width="228" height="84" rx="12" />
  <text class="bd-title" x="115" y="64" text-anchor="middle">Probes</text>
  <text class="bd-text" x="115" y="84" text-anchor="middle">linkos-probe at each location</text>
  <rect class="bd-box" x="1" y="158" width="228" height="84" rx="12" />
  <text class="bd-title" x="115" y="194" text-anchor="middle">CI and Flux</text>
  <text class="bd-text" x="115" y="214" text-anchor="middle">deployments, reconciliations</text>
  <rect class="bd-box" x="1" y="288" width="228" height="84" rx="12" />
  <text class="bd-title" x="115" y="324" text-anchor="middle">VictoriaMetrics, vmalert</text>
  <text class="bd-text" x="115" y="344" text-anchor="middle">metrics and rules</text>
  <rect class="bd-core" x="350" y="4" width="260" height="392" rx="16" />
  <text class="bd-name" x="480" y="40" text-anchor="middle">Beacon</text>
  <text class="bd-mono" x="480" y="60" text-anchor="middle">linkos-beacon serve</text>
  <rect class="bd-part" x="370" y="80" width="220" height="50" rx="10" />
  <text class="bd-title" x="480" y="101" text-anchor="middle">Gateway</text>
  <text class="bd-text" x="480" y="118" text-anchor="middle">for machines · port 17006</text>
  <rect class="bd-part" x="370" y="142" width="220" height="50" rx="10" />
  <text class="bd-title" x="480" y="163" text-anchor="middle">Router</text>
  <text class="bd-text" x="480" y="180" text-anchor="middle">groups, inhibits, silences</text>
  <rect class="bd-part" x="370" y="204" width="220" height="50" rx="10" />
  <text class="bd-title" x="480" y="225" text-anchor="middle">Bridge</text>
  <text class="bd-text" x="480" y="242" text-anchor="middle">cards, escalation, hooks</text>
  <rect class="bd-part" x="370" y="266" width="220" height="50" rx="10" />
  <text class="bd-title" x="480" y="287" text-anchor="middle">API and MCP</text>
  <text class="bd-text" x="480" y="304" text-anchor="middle">people and agents · port 17001</text>
  <rect class="bd-part" x="370" y="328" width="220" height="50" rx="10" />
  <text class="bd-title" x="480" y="349" text-anchor="middle">SSH channel</text>
  <text class="bd-text" x="480" y="366" text-anchor="middle">every session recorded · port 17002</text>
  <rect class="bd-box" x="731" y="28" width="228" height="84" rx="12" />
  <text class="bd-title" x="845" y="64" text-anchor="middle">People on call</text>
  <text class="bd-text" x="845" y="84" text-anchor="middle">cards in the chat, phones</text>
  <rect class="bd-box" x="731" y="158" width="228" height="84" rx="12" />
  <text class="bd-title" x="845" y="194" text-anchor="middle">Agents</text>
  <text class="bd-text" x="845" y="214" text-anchor="middle">Claude Code, Codex, …</text>
  <rect class="bd-box" x="731" y="288" width="228" height="84" rx="12" />
  <text class="bd-title" x="845" y="324" text-anchor="middle">Machines</text>
  <text class="bd-text" x="845" y="344" text-anchor="middle">5-minute certificates, NAT too</text>
  <path class="bd-line" d="M238 62 H342" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="290" y="54" text-anchor="middle">results</text>
  <path class="bd-line" d="M342 78 H238" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="290" y="94" text-anchor="middle">targets</text>
  <path class="bd-line" d="M238 200 H342" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="290" y="192" text-anchor="middle">change events</text>
  <path class="bd-line" d="M342 322 H238" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="290" y="314" text-anchor="middle">samples</text>
  <path class="bd-line" d="M238 338 H342" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="290" y="354" text-anchor="middle">alerts</text>
  <path class="bd-line" d="M618 62 H722" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="670" y="54" text-anchor="middle">cards, calls</text>
  <path class="bd-line" d="M722 78 H618" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="670" y="94" text-anchor="middle">ack, silence</text>
  <path class="bd-line" d="M618 192 H722" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="670" y="184" text-anchor="middle">a hook wakes</text>
  <path class="bd-line" d="M722 208 H618" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="670" y="224" text-anchor="middle">REST, MCP, ssh</text>
  <path class="bd-line" d="M618 330 H722" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="670" y="322" text-anchor="middle">ssh</text>
</svg>
</div>

The gateway gives each machine its own token and labels what it writes, so no machine speaks for another, and hands every probe its targets. The router groups, inhibits and silences alerts as Alertmanager does. The bridge turns an alert group into a card in the chat, escalates to phones when nobody acknowledges it, and wakes agents with a signed hook. Agents gather the evidence and leave notes over REST or MCP, and reach machines through the SSH channel. [Architecture](/architecture) has the parts and how they fit.

## Install

On a Linux machine with Docker, three commands start the centre: Beacon, VictoriaMetrics, vmalert and Caddy.

```bash
git clone https://github.com/openlinkos/beacon.git
sudo cp -r beacon/deploy /opt/beacon && cd /opt/beacon
sudo ./install.sh
```

Then enrol a probe, a machine and your agent. [Up in 30 minutes](/install) walks through it, and ends with a rehearsed outage going from alert to recovery.

## Connect an agent

One token works for the API, MCP and the SSH channel, and the agent needs nothing written for Beacon: MCP over HTTP, its own ssh, the shell.

```bash
claude mcp add --transport http linkos-beacon https://status.example.com/mcp --header "Authorization: Bearer lbt_…"
```

`list_machines` hands it an `ssh_config` block, after which its own `ssh edge-1`, `scp` and `rsync` go through the channel. Woken by an alert, it starts with `get_evidence`, works on the machines, verifies with `probe_now` and leaves a note for the people on call. See [Agents and alerts](/agents) and the [MCP tools](/reference/mcp).

## What Beacon is not

- **Not an agent.** It calls no model. It wakes agents, answers them and records what they do; deciding is theirs.
- **No approval workflow.** People see every session and can replay it; the pause switch stops agents at once.
- **No log platform and no time-series database of its own.** Agents read logs over SSH, the way they know best; VictoriaMetrics stores and queries.

[Goals](/goals) says what Beacon is for, what it will not do, and how to choose when goals pull apart. Beacon is open source under the Apache License 2.0, on [GitHub](https://github.com/openlinkos/beacon).
