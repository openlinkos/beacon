# Codex

Codex signs in the same way as Claude Code, through the browser with MCP's OAuth:

```bash
codex mcp add beacon --url https://status.example.com/mcp
codex mcp login beacon
```

The browser opens the same consent page, and the agent is named after the client unless you name it otherwise. Codex names itself with its own client ID metadata document (`https://chatgpt.com/oauth/codex/client.json`), so it registers nothing either; it listens on `http://127.0.0.1:<port>/callback`. A client without a metadata document registers itself first (dynamic client registration), then signs in the same way.

What the consent page asks, and what the sign-in may do, is the same for every client: [Allow it on Beacon's consent page](claude-code.md#allow-it-on-beacons-consent-page), [What a sign-in may do](../guide/people.md#what-a-sign-in-may-do).

## Without the internet

Beacon fetches the metadata documents of Claude Code and Codex from the internet (`https://claude.ai/…`, `https://chatgpt.com/…`). A Beacon that cannot reach the internet turns that off in `config.yaml`, and restarts:

```yaml
oauth:
  client_id_documents: false     # true by default
```

Its metadata then says `client_id_metadata_document_supported: false`, it refuses a `client_id` that is a URL, and the clients register themselves instead (`codex mcp login beacon --oauth-client-registration dcr` makes Codex do so).
