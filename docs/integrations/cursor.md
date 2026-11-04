# Cursor

Cursor reaches Beacon's MCP server over HTTP and signs in with MCP's OAuth, like Claude Code: no token is copied. Put this in Cursor's MCP configuration:

::: code-group

```json [~/.cursor/mcp.json]
{
  "mcpServers": {
    "beacon": {
      "url": "https://status.example.com/mcp"
    }
  }
}
```

```json [.cursor/mcp.json]
{
  "mcpServers": {
    "beacon": {
      "url": "https://status.example.com/mcp"
    }
  }
}
```

:::

The global file adds Beacon everywhere; the project file, in a project's `.cursor/` directory, adds it to that project only. The same server can also be added from Cursor's settings, under **MCP**.

## Sign in

On the first connection, Cursor discovers Beacon's authorization server on its own, registers itself as a public client and opens the browser: the authorization-code flow with PKCE. Beacon's consent page asks the same four things as for Claude Code — the agent's name (named after the client, `cursor`, unless you name it otherwise), what it may do, the machines it reaches, and for how long ([Allow it on Beacon's consent page](claude-code.md#allow-it-on-beacons-consent-page)).

Cursor's redirect addresses — a `localhost` port on your machine for the desktop app, an `https` address on `www.cursor.com` when you connect from the web — are among those Beacon accepts. The agent's page on the console lists the sign-in under **OAuth sign-ins**, where you can revoke it.

## Or send a token

A client that cannot sign in, or one you would rather not, sends a token instead:

```json
{
  "mcpServers": {
    "beacon": {
      "url": "https://status.example.com/mcp",
      "headers": { "Authorization": "Bearer ${env:BEACON_TOKEN}" }
    }
  }
}
```

Cursor fills `${env:…}` from your environment, so the token itself stays out of the file. Make it as one of your agents' credentials, on the console's **Agents** page or with `beacon agent add` ([Headless agents and CI](headless.md#make-an-agent-and-its-token)); the token then acts as that agent, and it renews itself.
