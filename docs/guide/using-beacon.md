# Using Beacon

These pages are the work you do with Beacon once it runs, grouped by what Beacon does. Each page is one task: what you need, the steps, how to check the result, and what to read next. New to Beacon? Start with [What is Beacon](index.md), [Try it on your laptop](quickstart.md) and [How Beacon works](concepts.md).

## Monitoring and alerts {#monitoring-and-alerts}

What Beacon checks, from where, and what it tells your users.

1. [Monitor a service](monitoring.md): probe locations, target sets and modules. Every change is checked, takes effect at once and can be reverted.
2. [Heartbeats and expiring resources](heartbeats.md): jobs that must report success often enough, and domains and machines that expire.
3. [Publish a status page](status-pages.md): what the probes agree on, made public, with incidents and maintenance windows.

Cards in the chat for the people on call are set up in [Set up the Feishu app](../integrations/feishu.md).

## Agents on call {#agents-on-call}

An alert wakes an agent; the agent reads the evidence, fixes what it can, checks the fix and leaves a note.

1. [Put an agent on call](on-call.md): the hook that wakes an agent, the evidence in one call, probing on demand and notes.
2. [Rehearse an outage](drills.md): the whole loop on Beacon's own drill service, before a real outage.
3. [Review what agents did](console.md): the console, where people see what needs attention, the alerts, silences and maintenance, and every session with its replay.

The agent clients and the other end of a hook are in [Integrations](../integrations/index.md).

## Machines and the SSH channel {#machines-and-the-ssh-channel}

How an agent reaches a server without holding a key, and how to stop it.

1. [Give agents a machine](machines.md): turn the channel on, add machines (behind NAT too) and give an agent access.
2. [Stop an agent, pause everything](stop.md): every brake, and what each one closes.

## The team and permissions {#the-team-and-permissions}

Who may do what, people and their agents alike.

1. [Invite your team](people.md): accounts and roles, agents and their presets, OAuth sign-ins, SSH keys, and moving from tokens.

Every role, preset and scope is listed in [Roles, presets and scopes](../reference/permissions.md).
