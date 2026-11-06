# Integrations

How Beacon works with the tools around it. The pages come in two groups: the agent clients that connect to Beacon, and the events and notifications that pass between Beacon and other systems when something breaks.

## Agent clients {#agent-clients}

An agent reaches Beacon over MCP or the REST API as one of a person's agents, and never does more than that person's role allows. An interactive client signs in through the browser with MCP's OAuth; a program that runs alone uses a token.

1. [Claude Code](claude-code.md): sign in through the browser, and reach machines with your own ssh, scp and rsync.
2. [Codex](codex.md): the same sign-in, and a Beacon without the internet.
3. [Cursor](cursor.md): sign in through the browser, or send a token.
4. [Other MCP clients](mcp-clients.md): any client that speaks MCP over HTTP, and the two ways to sign in.
5. [Headless agents and CI](headless.md): an agent's token, `beacon-oncall` to run an agent when a hook fires, and tokens that renew themselves.
6. [The on-call prompt](on-call-prompt.md): the prompt a woken agent starts from.

## Events and notifications {#events-and-notifications}

What goes out from Beacon when an alert fires, and what comes in to explain it.

1. [Agents woken by a webhook](webhooks.md): a signed POST to a receiver of yours, and how to check it.
2. [GitHub Actions](github-actions.md): a hook that dispatches a workflow, and a workflow that runs Claude Code.
3. [Deployments and Flux](change-events.md): change events from CI and Flux, so that the evidence says what changed just before an alert.
4. [Set up the Feishu app](feishu.md): alerts as cards in a Feishu or Lark group, with buttons for the people on call.

When a hook fires and what the agent does next is in [Put an agent on call](../guide/on-call.md); what a hook sends, field by field, is in the [Webhooks reference](../reference/webhooks.md).
