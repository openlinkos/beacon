# Other MCP clients

Beacon's MCP server speaks streamable HTTP at `/mcp`, stateless, so any client that speaks MCP over HTTP reaches it. In VS Code, for example, put this in `.vscode/mcp.json`:

```json
{
  "servers": {
    "beacon": {
      "type": "http",
      "url": "https://status.example.com/mcp",
      "headers": { "Authorization": "Bearer lbt_…" }
    }
  }
}
```

## How a client signs in

There are two ways, and a client needs either one of them:

- **MCP's OAuth**, for a client that implements it (Claude Code, Codex and Cursor do; see their pages). Beacon publishes the protected-resource metadata of `/mcp` and the authorization server's metadata (RFC 9728, RFC 8414), registers clients dynamically as public clients (RFC 7591) with redirect URIs on the loopback interface or `https`, and takes the authorization-code flow with PKCE (`S256`). The consent page asks what the agent may do — the same four things as for Claude Code ([Allow it on Beacon's consent page](claude-code.md#allow-it-on-beacons-consent-page)).
- **A token in the header**, `Authorization: Bearer lbt_…`, for everything else: a script, a CI job, a client without OAuth. The token is one of your agents' credentials, and it renews itself ([Headless agents and CI](headless.md#make-an-agent-and-its-token)). An OAuth access token (`lba_…`) is accepted wherever a bearer token is, so a client that has signed in can also use it as a header.

## Check it works

```bash
curl -H "Authorization: Bearer lbt_…" https://status.example.com/api/v1/token
```

answers who the token acts for, as its holder sees it; the MCP tool `whoami` says the same. Every tool takes a scope, checked on every call, and every call is recorded: [MCP tools](../reference/mcp.md) lists them all with what they need.
