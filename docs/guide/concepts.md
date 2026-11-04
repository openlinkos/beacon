# How Beacon works

This page has the shape of the thing: one diagram, the words the rest of the guide uses, and the two paths that matter — an alert's, from a service breaking to people and agents hearing of it, and an agent's, from being woken to leaving a note on the alert. [Architecture](../about/architecture.md) tells the same story in full detail, for when something needs looking into.

## The whole picture

<div class="bd-wrap">
<svg class="bd" viewBox="0 0 960 400" role="img" aria-labelledby="bd-title">
  <title id="bd-title">How Beacon fits: probes, CI and Flux report to Beacon; Beacon keeps metrics and rules in VictoriaMetrics and vmalert, sends cards and phone calls to the people on call, wakes agents, and lets them reach machines through its SSH channel.</title>
  <defs>
    <marker id="bd-arrow" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="10" markerHeight="10" markerUnits="userSpaceOnUse" orient="auto">
      <path class="bd-line" d="M2 1.5 L8 5 L2 8.5" />
    </marker>
  </defs>
  <rect class="bd-box" x="1" y="28" width="228" height="84" rx="12" />
  <text class="bd-title" x="115" y="64" text-anchor="middle">Probes</text>
  <text class="bd-text" x="115" y="84" text-anchor="middle">beacon-probe at each location</text>
  <rect class="bd-box" x="1" y="158" width="228" height="84" rx="12" />
  <text class="bd-title" x="115" y="194" text-anchor="middle">CI and Flux</text>
  <text class="bd-text" x="115" y="214" text-anchor="middle">deployments, reconciliations</text>
  <rect class="bd-box" x="1" y="288" width="228" height="84" rx="12" />
  <text class="bd-title" x="115" y="324" text-anchor="middle">VictoriaMetrics, vmalert</text>
  <text class="bd-text" x="115" y="344" text-anchor="middle">metrics and rules</text>
  <rect class="bd-core" x="350" y="4" width="260" height="392" rx="16" />
  <text class="bd-name" x="480" y="40" text-anchor="middle">Beacon</text>
  <text class="bd-mono" x="480" y="60" text-anchor="middle">beacon serve</text>
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

Three flows cross it. Results go up: each probe checks the targets the centre hands it and pushes them, through the gateway, into VictoriaMetrics. Alerts come back: vmalert evaluates the rules and posts what fires to the gateway, the router groups it, and the bridge tells people and wakes agents. Agents come in: over REST and MCP for the evidence and the notes, and over the SSH channel to the machines.

## The words

| Word | Meaning |
| --- | --- |
| centre | The `beacon` service and the machine it runs on. Probes push to it, people and agents sign in to it, and VictoriaMetrics and vmalert sit beside it. |
| probe location | A small machine running `beacon-probe`, near your users, that checks the targets the centre hands it and pushes the results back. |
| machine | A server enrolled with Beacon, reached by agents through the SSH channel: directly, or through a connector when it sits behind NAT. |
| person | Someone with an account of their own on this Beacon, in a role. |
| owner | The person an agent acts for; the agent never does more than this person's role allows. |
| role | What a person may do. Each role adds to the ones below it: viewer sees everything, on call also works alerts and reaches machines, operator also manages machines and maintenance, owner also changes the configuration and the team. |
| agent | A program that acts for one person: Claude Code, Codex, a CI job. One identity over SSH, REST and MCP; it signs in with MCP's OAuth or a token. |
| preset | What a new agent may do, chosen when it is made — read only, on call or operator — always within its owner's role. |
| SSH channel | Beacon's SSH door, port 17002. The agent's own `ssh`, `scp` and `rsync` go through it; each connection gets a certificate valid for five minutes; every session is recorded. |
| evidence | What `get_evidence` returns in one call: each location's result and the reason it failed, the other alerts in the same failure domain, the changes just before, and what people and agents have already done. |
| session | One visit to a machine through the channel: a shell, a single command, a file transfer, a forwarded port. Recorded end to end. |
| replay | A session's recording played back in the browser, with each command's exit status. |
| console | The pages people sign in to: alerts with their evidence and timeline, machines, sessions with their replays, people and agents, the configuration and its history, the audit log. |
| status page | The public page Beacon serves itself: components, their state and history, incidents, maintenance windows. |
| catalogue | Where what Beacon watches is kept: probe locations, heartbeats, expiring resources, status pages. It lives in the database, and every change to it is a version. |
| heartbeat | A job that must report success often enough — a backup, a scheduled task. When it is late or missing, an alert fires. |
| silence | Matching alerts muted for a while, from a card or the command line, with a reason and an end. |
| maintenance | Planned work on some services, announced ahead on the status page; the components it covers show as in maintenance while it lasts. |
| alert | What the rules send when they see trouble, grouped and routed by Beacon: to the chat as a card, to phones when nobody answers, and to agents as a hook. |
| drill | A rehearsed outage: `beacon drill fail` makes Beacon's own drill service fail on purpose, and you watch the loop run, with you or an agent in it. |

## An alert's path

Every minute, each probe checks the targets the centre handed it and pushes the results, with the reason for every failure; VictoriaMetrics keeps them. vmalert evaluates the rules every 30 seconds — a service counts as down only when several locations agree, so one location's bad network wakes nobody — and posts what fires to Beacon, which does Alertmanager's job itself: alerts are grouped (a new group waits half a minute for its fellows), inhibition and silences mute what they match. A new group becomes a card in the chat; a critical one nobody acknowledges is buzzed after 10 minutes and phoned after 15. The same group fires the hooks that wake agents, beside the delivery, which never waits for them. When the probes succeed again, the rule engine sees it and the alert resolves.

## An agent's path

An agent acts for one person, its owner, and never does more than that person's role allows, read again on every request. Woken by a hook, or asked by its person, it signs in once — with MCP's OAuth through the browser, or with a token — and that one identity opens every door: the same calls over REST and MCP, the same machines over SSH. `get_evidence` gathers the whole story in one answer; `probe_now` asks every location to check again at once; `add_note`, `ack_alert` and `silence_alert` write, and every write is audited and echoed to the chat. Machine work goes through the SSH channel, with the agent's own `ssh` and `scp` or with Beacon's tools (`run_command`, `read_file`, `write_file`, `edit_file`); each connection gets a five-minute certificate, each session a recording. When an agent's access ends — a credential revoked, its owner demoted — what it has open closes at once, and `beacon ssh pause` closes everything, for everyone.

## Where to read more

- [Architecture](../about/architecture.md): every part in detail — what runs where, where the state lives, the rules each part keeps.
- [Put an agent on call](on-call.md) and [Give agents a machine](machines.md): the two paths above, as things you do.
