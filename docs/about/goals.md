# Goals

What Beacon is for, what it will not do, and how to choose when goals pull apart. Every change should serve one of these goals. Say which when you plan one.

## North star

Beacon is one piece of infrastructure for the AI era: the piece where agents run operations. Its makers build such pieces to remove what gets in the way of AI and agents at work, so that using them goes smoothly. Other pieces, such as memory that lasts across sessions, are other projects. Beacon stays in its lane.

People and agents operate through Beacon. A person can do the work directly, delegate it within their authority, and see who acted and whether the service recovered.

Measure it in three ways:

- **Coverage**: of everything agents do on machines and alerts, how much goes through Beacon. Target: all of it, with no key in any agent's hands.
- **Closure**: of the critical alerts, how many an agent takes from being woken to a verified recovery with nobody stepping in. Target: most of them.
- **Use by people**: how often a team does its regular work in Beacon, from registering services and connecting to machines through to incident handover. Track which jobs still send people to separate tools.

Three guardrails apply throughout: no alert lost, no credential leaked, nothing done without a record. Prioritise changes that improve coverage, closure, people's work or these guardrails.

## In one sentence

Beacon is self-hosted operations infrastructure for people and the agents they delegate to, with shared monitoring, machine access, authorisation and audit.

Three tests:

- An agent connecting for the first time, without reading anything written for Beacon and using only ssh, the shell, HTTP and MCP, can find out what is wrong in a real outage, fix it on the machine, and leave people able to see what it did.
- Wherever Beacon runs, everything agents do in operations goes through it, and nobody ever hands a key to an agent.
- A person can receive an alert on a phone, sign in to the same incident, connect to the affected machine under their own name, verify recovery and leave a handover. An administrator can enrol a service, see its first result, schedule maintenance and check what the public can see.

## The problem

An operations engineer needs four things:

- **eyes**: monitoring and alerts;
- **hands**: logging in to machines, running commands, editing files;
- **a record**: what was done, to look up and to replay;
- **a line to people**: being woken, reporting, being seen.

An agent needs the same four, with three differences:

- it knows general tools such as ssh, the shell, HTTP and MCP, not each product's own interface;
- it remembers nothing from one session to the next;
- it does not watch a screen, so something has to wake it.

Beacon offers the four in the ways agents already work, instead of a new system for them to learn.

## Who it is for

- **People who run their own machines and services with AI agents** (Claude Code, Codex and the like): from one person with a few agents and a dozen machines to a small team, self-hosted.
- **Beacon's makers first**: they run their own operations on it before anyone else.

## Product goals: three pillars

The deployed capabilities cover identity and delegation, SSH access, and service monitoring and alerts. Team credential custody is a fourth capability in development. Its first test is whether a person can keep a shared credential, authorise its use, trace each use and revoke the authorisation for future requests. Traceability applies to all four.

| Goal | Done when (figures are targets) |
| --- | --- |
| **1. Sees** (monitoring): whether services work for their users, what broke and why | A critical outage reaches agents and people within 5 minutes; one probe location's hiccup disturbs nobody; every failure carries its location and its reason; one call returns the evidence |
| **2. Reaches** (the SSH channel): people and agents get to enrolled machines and work on them | ssh, scp, rsync, `ssh -L` and the MCP tools reach any enrolled machine, also behind NAT; the channel adds under 50 ms (measured: about 23 ms per new connection); agents never touch a machine password or key. A session distinguishes a person acting directly from their agent |
| **3. Connects** (the platform): people and agents share one identity and one timeline | An agent is one identity on SSH, REST and MCP, acting for a person and never beyond that person's role; a machine is enrolled once, and monitoring and the SSH channel share that identity; one timeline joins alerts, sessions, commands and notes; a new alert can wake an agent; people see in Feishu everything agents do, can replay it, and can pause agents at once |

## Design goals

| Goal | Done when |
| --- | --- |
| **4. Close to how agents work** (first among the design goals) | Standard protocols: SSH, Prometheus queries, HTTP with JSON, MCP; no configuration language of our own. MCP tools match the agents' own tools in name and meaning: run a command, read a file, edit a file, search, background jobs. Results are clean and structured, without terminal control codes; long output is cut and says how to get the rest. Error codes are stable and say how to put the problem right |
| **5. Visibility instead of approval** | Every session is recorded and can be replayed, every command has a structured record, kept for at least six months. No approval workflow is built in: reviewing is the agent's job, done by its own harness. Beacon supplies information (which machines are critical, for one) and a pause switch for people |
| **6. Credentials stay in Beacon** | Passwords and keys exist only in Beacon; machines are reached with certificates valid for minutes; tokens have scopes and expire; secrets are read only from files the configuration names |
| **7. Configured through Beacon** | The catalogue, probe targets, modules and machines live in Beacon's database, with a history of who changed what. People can add routine HTTP monitors and schedule maintenance from the console. Machines and an agent's custom scopes can be changed in the console, through the API and MCP, and on the command line. The catalogue, targets and modules are available through the API and MCP, with files for import and export |
| **8. Small** | One machine with 2 CPUs and 4 GB is ample for the centre; probes and the connectors on machines stay within 20 MB resident; installing takes 30 minutes |
| **9. Open** | Apache-2.0; everything these goals need is in this repository; no lock-in to a model, a chat service or a cloud |
| **10. Hostable without a fork** | Only the seams are kept (workspace, storage interface, protocol versions, metering), so a hosted deployment stays possible; none of it is built ahead of need |

## Not doing

- **Beacon is not an agent and calls no model.** It wakes agents, and `beacon-oncall` starts the agent program you name when one is woken; reasoning and reviewing are the agents' work. Beacon is where they work.
- **No approval workflow.** The pause switch and each machine's access are the only brakes.
- **No log platform, tracing, APM or custom dashboards.** An agent reads logs through the SSH channel (journalctl and the like), the way it knows best.
- **No time-series database of our own.** VictoriaMetrics stores and queries.
- **No remote desktop and no deployment system.** Flux and its like stay; Beacon takes their change events.
- **No code that only one deployment needs.** What is particular to a deployment is configuration, kept outside this repository.

## What landed means

The project has landed (1.0) when all three hold:

1. **Where it runs**, every critical alert wakes an agent; most are closed by it, people are paged for the rest, and every operation agents do goes through Beacon.
2. **A team elsewhere** installs Beacon in 30 minutes from the README with the reference deployment, enrols a probe, a machine and the agent program it already uses (Claude Code, Codex or another), and has the same loop running with its own chat.
3. **People have one place to work**. The chat card opens the same incident in the console with evidence, recovery checks and session replays. People can act with Beacon's own accounts and grant only the access they intend. The console supports the common service setup, maintenance and SSH tasks on desktop, and the on-call actions are usable on a phone.

## When goals pull apart

First not losing alerts and not leaking credentials; then closeness to how agents work; then visibility; then staying small; and only then more features.
