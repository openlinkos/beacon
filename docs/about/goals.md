# Goals

What Beacon is for, what it will not do, and how to choose when goals pull apart. Every change should serve one of these goals. Say which when you plan one.

## North star

Beacon is one piece of infrastructure for the AI era. Its makers build such pieces to remove what gets in the way of AI and agents at work. Other pieces, such as memory that lasts across sessions, are other projects. Beacon stays in its lane.

Its lane is the infrastructure agents work through, shared with the people who manage them:

- **Agents** take the credentials they are granted, reach machines, and see whether what they run and what they build works.
- **People** do the same jobs in the same place, and decide what each agent may use.
- **Everything** either of them does is on the record, and can be traced.

It is measured three ways:

- **Coverage**: of the credentials, machines and services agents work with, how much they reach through Beacon. Target: all of it, with no credential handed to an agent any other way.
- **Replacement**: where Beacon runs, people use it instead of a separate tool for each job: a password manager, a bastion host, an uptime monitor. Target: each of those retired.
- **Closure**: of the critical alerts, how many an agent takes from being woken to a verified recovery, with nobody stepping in. Target: most of them. This is the loop the capabilities together make possible.

Three guardrails are never traded for any of these: nothing done without a record, no credential leaked, no alert lost. A change that moves no measure, and upholds no guardrail, waits.

## In one sentence

Beacon is self-hosted infrastructure that AI agents and their people share: a team password manager, trusted access for agents, an SSH bastion, and monitoring with alerts and views, for your machines and for the services your agents build, with everything traceable.

Two tests:

- **For an agent**: connecting for the first time, with only ssh, the shell, HTTP and MCP, it can take a credential it was granted, find out what is wrong in a real outage, fix it on the machine, and leave people able to see what it did.
- **For a person**: each job goes at least as well in Beacon as in the tool they used before, and any part of it can be granted to an agent.

## The problem

An agent at work needs what an engineer needs:

- **credentials**: the passwords, tokens and keys of the systems it works on;
- **access**: to machines and services;
- **eyes**: monitoring and alerts, for what it runs and for what it builds;
- **a record**: what was done, by whom, and why;
- **a line to people**: being woken, reporting, being seen.

It differs from an engineer in three ways:

- it knows general tools such as ssh, the shell, HTTP and MCP, not each product's own interface;
- it remembers nothing from one session to the next;
- it does not watch a screen, so something has to wake it.

An agent cannot close the loop alone. People hold the credentials, decide what an agent may use, and do the same jobs themselves. If only agents could use the infrastructure, people would keep a second set of tools, and could grant nothing from them.

So Beacon serves both, in one place. It offers agents each capability in the ways they already work. It offers people the same capability at least as well as the tool they use for the job today.

## Who it is for

- **People and teams who run their own machines and services with AI agents** (Claude Code, Codex and the like): from one person with a few agents and a dozen machines to a team that shares its passwords and its work, self-hosted.
- **Beacon's makers first**: they run their own work on it before anyone else.

## The core: everything traceable

Traceability is what lets people let agents work. Instead of approving each step in advance, they can see every step afterwards, and trace it to who did it, for whom and why.

| Goal | Done when |
| --- | --- |
| **1. Traceable** | Every action of a person or an agent is recorded: who did it, for whom, under which grant, and why. Sessions are recorded and replay; commands, uses of a secret and changes to the configuration each have a structured record, kept for at least six months. The records form a hash chain with signed checkpoints, so a gap or an edit shows. One timeline joins alerts, sessions, commands, notes and changes, and people see it in the console and in their chat. No command waits for a permit: what Beacon approves is credentials, once per grant and never at each use |

## Product goals: four capabilities

Each capability is a job people already have a tool for. Beacon does it for agents in the ways agents work, and for people at least as well as that tool.

| Goal | Done when (figures are targets) |
| --- | --- |
| **2. Secrets** (a team password manager): people and agents keep and share passwords, keys and tokens | A team moves off its password manager without losing anything: collections shared by role, the common item types, import from the common managers, filling in the browser, and the history of every item. An agent uses a secret by grant: one authorisation, many uses, a grant allowed to last and to be renewed. It can use a value without seeing it. Every read and every use is traced to the person or agent and the reason |
| **3. Trusted access** (identity): every agent is someone's, and does only what it was given | An agent is one identity on SSH, REST and MCP, acting for a person and never beyond that person's role. Claude Code and Codex sign in through the browser; a headless agent takes a scoped token that expires and renews itself. An agent holds no credential except through Beacon. A person sees each of their agents and what it may do, and can revoke one or pause them all at once |
| **4. Machines** (an SSH bastion): agents and people get to every enrolled machine and work on it | ssh, scp, rsync, `ssh -L` and the MCP tools reach any enrolled machine, also behind NAT. The channel adds under 50 ms (measured: about 23 ms per new connection). Agents never touch a password or a key. A machine is enrolled once, and monitoring shares its identity |
| **5. Monitoring** (alerts and views): whether services work for their users, what broke and why, for your machines and for the services your agents build | A critical outage reaches agents and people within 5 minutes; one probe location's hiccup disturbs nobody; every failure carries its location and its reason; one call returns the evidence. A new alert can wake an agent, and reaches people in their chat and on their phones. An agent adds a service it built to monitoring itself: its probes, its metrics, its alert rules and a view of them. People see the same views in the console, and users see a public status page |

## Design goals

| Goal | Done when |
| --- | --- |
| **6. Close to how agents work** | Standard protocols: SSH, Prometheus queries, HTTP with JSON, MCP; no configuration language of our own. MCP tools match the agents' own tools in name and meaning: run a command, read a file, edit a file, search, background jobs. Results are clean and structured, without terminal control codes; long output is cut and says how to get the rest. Error codes are stable and say how to put the problem right |
| **7. As good for people** | Each capability holds up against the tool people use for the job today: a password manager, a bastion host, uptime monitoring and dashboards. People manage and grant from the console, the command line and their chat. What a person may do, they can grant to an agent, and no more |
| **8. Credentials stay in Beacon** | Passwords and keys live in Beacon, encrypted, and reach an agent only by grant; machines are reached with certificates valid for minutes; tokens have scopes and expire; Beacon's own secrets are read only from files the configuration names |
| **9. Configured through Beacon** | The catalogue, probe targets, modules and machines live in Beacon's database and are changed through the API, MCP and the command line, with a history of who changed what; files are for import and export |
| **10. Small** | One machine with 2 CPUs and 4 GB is ample for the centre; probes and the connectors on machines stay within 20 MB resident; installing takes 30 minutes |
| **11. Open** | Apache-2.0; everything these goals need is in this repository; no lock-in to a model, a chat service or a cloud |
| **12. Hostable without a fork** | Only the seams are kept (workspace, storage interface, protocol versions, metering), so a hosted deployment stays possible; none of it is built ahead of need |

## Not doing

- **Beacon is not an agent and calls no model.** It wakes agents, and `beacon-oncall` starts the agent program you name when one is woken; reasoning and reviewing are the agents' work. Beacon is where they work.
- **No approval of what agents run.** Deciding what to run is the agent's job, done by its own harness. Approval is for credentials only, once per grant and never per use. On machines, the pause switch and each machine's access are the brakes.
- **No log platform, tracing or APM.** An agent reads logs through the SSH channel (journalctl and the like), the way it knows best. The views are of the metrics and probe results Beacon already keeps.
- **No time-series database of our own.** VictoriaMetrics stores and queries.
- **No remote desktop and no deployment system.** Flux and its like stay; Beacon takes their change events.
- **No code that only one deployment needs.** What is particular to a deployment is configuration, kept outside this repository.

## What landed means

The project has landed (1.0) when all three hold:

1. **Where it runs**, people and agents use Beacon for all four jobs. The team's passwords are in it, and the password manager before it is retired. Every credential an agent uses is granted through it, every machine is reached through it, and every critical alert wakes an agent, which closes most of them.
2. **A team elsewhere** installs Beacon in 30 minutes from the README with the reference deployment. It imports its passwords, enrols a probe, a machine and the agent program it already uses (Claude Code, Codex or another), and has the same running with its own chat.
3. **People have one place to look.** From the card in the chat, one link opens the alert's timeline, each session's replay and the pause switch. Every grant, and every use of a secret, can be traced from the console. People sign in with Beacon's own accounts; there is no external sign-in.

## When goals pull apart

First traceability: nothing done without a record. Then not leaking credentials and not losing alerts. Then closeness to how agents work, and use by people as good as the tools they know. Then staying small. Only then more features.
