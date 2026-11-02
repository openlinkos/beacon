# Goals

English | [简体中文](zh/goals.md)

What Beacon is for, what it will not do, and how to choose when goals pull apart. Every change should serve one of these goals; say which when you plan one.

## North star

Beacon is one piece of infrastructure for the AI era: the piece where agents run operations. Its makers build such pieces to take away what gets in the way of AI and agents while they work, so that using them is smooth. Other pieces, memory that lasts across sessions for one, are other projects; Beacon stays in its lane.

For Beacon that means: **operations are done by agents through Beacon; people watch, can stop them, and never have to do the work by hand.**

It is read two ways:

- **Coverage**: of everything agents do on machines and alerts, how much goes through Beacon. Target: all of it, with no key in any agent's hands.
- **Closure**: of the critical alerts, how many an agent takes from being woken to a verified recovery with nobody stepping in. Target: most of them.

Three guardrails are never traded for either: no alert lost, no credential leaked, nothing done without a record. A change that moves neither coverage nor closure, and holds no guardrail, waits.

## In one sentence

Beacon is the infrastructure an AI agent finds handiest when it runs operations: it sees whether services work, it reaches every machine, and it puts people and agents on one platform, all in the ways agents already work.

Two tests:

- An agent connecting for the first time, without reading anything written for Beacon and using only ssh, the shell, HTTP and MCP, can find out what is wrong in a real outage, fix it on the machine, and leave people able to see what it did.
- Wherever Beacon runs, everything agents do in operations goes through it, and nobody ever hands a key to an agent.

## The problem

An operations engineer needs eyes (monitoring and alerts), hands (logging in to machines, running commands, editing files), a record (what was done, to look up and to replay) and a line to people (being woken, reporting, being seen). An agent needs the same four, with three differences: it knows general tools such as ssh, the shell, HTTP and MCP rather than each product's own interface; it remembers nothing from one session to the next; and it does not watch a screen, so something has to wake it. Beacon offers the four in the ways agents already work, rather than a new system for them to learn.

## Who it is for

- **People who run their own machines and services with AI agents** (Claude Code, Codex and the like): from one person with a few agents and a dozen machines to a small team, self-hosted.
- **Beacon's makers first**: they run their own operations on it before anyone else does.

## Product goals: three pillars

| Goal | Done when (figures are targets) |
| --- | --- |
| **1. Sees** (monitoring): whether services work for their users, what broke and why | A critical outage reaches agents and people within 5 minutes; one probe location's hiccup disturbs nobody; every failure carries its location and its reason; one call returns the evidence |
| **2. Reaches** (the SSH channel): agents get to every enrolled machine and work on it | ssh, scp, rsync, `ssh -L` and the MCP tools reach any enrolled machine, also behind NAT; the channel adds under 50 ms (measured: about 23 ms per new connection); agents never touch a password or a key |
| **3. Connects** (the platform): people and agents share one identity and one timeline | One token works for SSH, REST and MCP; a machine is enrolled once, and monitoring and the SSH channel share that identity; one timeline joins alerts, sessions, commands and notes; a new alert can wake an agent; people see in Feishu everything agents do, can replay it, and can pause agents at once |

## Design goals

| Goal | Done when |
| --- | --- |
| **4. Close to how agents work** (first among the design goals) | Standard protocols: SSH, Prometheus queries, HTTP with JSON, MCP; no configuration language of our own. MCP tools match the agents' own tools in name and meaning: run a command, read a file, edit a file, search, background jobs. Results are clean and structured, without terminal control codes; long output is cut and says how to get the rest. Error codes are stable and say how to put the problem right |
| **5. Visibility instead of approval** | Every session is recorded and can be replayed, every command has a structured record, kept for at least six months. No approval workflow is built in: reviewing is the agent's job, done by its own harness. Beacon supplies information (which machines are critical, for one) and a pause switch for people |
| **6. Credentials stay in Beacon** | Passwords and keys exist only in Beacon; machines are reached with certificates valid for minutes; tokens have scopes and expire; secrets are read only from files the configuration names |
| **7. Configured through Beacon** | The catalogue, probe targets, modules and machines live in Beacon's database and are changed through the API, MCP and the command line, with a history of who changed what; files are for import and export |
| **8. Small** | One machine with 2 CPUs and 4 GB is ample for the centre; probes and the connectors on machines stay within 20 MB resident; installing takes 30 minutes |
| **9. Open** | Apache-2.0; everything these goals need is in this repository; no lock-in to a model, a chat service or a cloud |
| **10. Hostable without a fork** | Only the seams are kept (workspace, storage interface, protocol versions, metering), so a hosted deployment stays possible; none of it is built ahead of need |

## Not doing

- **Beacon is not an agent and calls no model.** It wakes agents, and `linkos-oncall` starts the agent program you name when one is woken; reasoning and reviewing are the agents' work. Beacon is where they work.
- **No approval workflow.** The pause switch and each machine's access are the only brakes.
- **No log platform, tracing, APM or custom dashboards.** An agent reads logs over the SSH channel (journalctl and the like), the way it knows best.
- **No time-series database of our own.** VictoriaMetrics stores and queries.
- **No remote desktop and no deployment system.** Flux and its like stay; Beacon takes their change events.
- **No code that only one deployment needs.** What is particular to a deployment is configuration, kept outside this repository.

## What landed means

The project has landed (1.0) when all three hold:

1. **Where it runs**, every critical alert wakes an agent; most are closed by it, people are paged for the rest, and every operation agents do goes through Beacon.
2. **A team elsewhere** installs Beacon in 30 minutes from the README with the reference deployment, enrols a probe, a machine and the agent program it already uses (Claude Code, Codex or another), and has the same loop running with its own chat.
3. **People have one place to look**: from the card in the chat, one link opens the alert's timeline, each session's replay and the pause switch; Beacon has no login system of its own.

## When goals pull apart

Not losing alerts and not leaking credentials, then closeness to how agents work, then visibility, then staying small, then more features.
