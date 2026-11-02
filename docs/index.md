---
layout: home

hero:
  name: Beacon
  text: Put your AI agent on call
  tagline: Self-hosted monitoring and SSH access for the AI agents that run your servers. Beacon checks your services from where your users are, wakes your agent with the evidence when one breaks, lets it reach the server without ever holding your keys, and records every command it runs.
  image:
    light: /logo.svg
    dark: /logo-dark.svg
    alt: Beacon
  actions:
    - theme: brand
      text: Up in 30 minutes
      link: /install
    - theme: alt
      text: How it works
      link: /architecture
    - theme: alt
      text: GitHub
      link: https://github.com/openlinkos/beacon

features:
  - title: Checked from where your users are
    details: Probes on small machines check HTTP, TLS, TCP, container registries, Git and domain expiry, and record why each check failed. A service is down only when several locations agree, so one location's bad network wakes nobody.
    link: /catalog
    linkText: What is checked
  - title: Woken with the evidence
    details: A signed webhook or a GitHub workflow wakes the agent you choose. One call returns every location's result, the other alerts in the same failure domain, the deployments just before, and what has already been done.
    link: /agents
    linkText: Put an agent on call
  - title: No keys for agents
    details: Agents use their own ssh, scp and rsync through Beacon, or its MCP tools. Each connection gets a certificate valid for five minutes, and your servers trust only Beacon. Revoke one agent, or stop them all with one switch.
    link: /ssh
    linkText: The SSH channel
  - title: Every command on record
    details: Sessions replay in the browser, interactive shells are split into commands with their exit codes, and the agent's notes land on the alert. You review what was done, instead of approving each step in advance.
    link: /pages
    linkText: The console
  - title: Fixed means checked again
    details: After a fix the agent asks every location to probe the service at once and sees their answers within a minute. The alert resolves on what the probes see, not on what the agent says.
    link: /agents#probe-again-now
    linkText: Probing on demand
  - title: People stay in the loop
    details: A public status page for your users, a console for your team, alert cards in Feishu that can be acknowledged and silenced, and escalation to phones when nobody answers.
    link: /status
    linkText: Status pages
  - title: Open source
    details: Go under the Apache License 2.0, on VictoriaMetrics; one machine with 2 CPUs and 4 GB is plenty. Beacon calls no model and needs no cloud account.
    link: https://github.com/openlinkos/beacon
    linkText: openlinkos/beacon
---
