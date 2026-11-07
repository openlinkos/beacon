---
sourceHash: sha256-ea051277
---

# Codex

Codex 的登录方式和 Claude Code 一样，通过浏览器、用 MCP 的 OAuth：

```bash
codex mcp add beacon --url https://status.example.com/mcp
codex mcp login beacon
```

浏览器打开的是同一个授权页面。除非你另起名字，Agent 以客户端的名字命名。

Codex 用它自己的客户端 ID 元数据文档（`https://chatgpt.com/oauth/codex/client.json`）表明身份，所以它也什么都不用注册。它在 `http://127.0.0.1:<port>/callback` 等待回答。没有元数据文档的客户端先自己注册（动态客户端注册），然后以同样的方式登录。

授权页面问什么、一次登录能做什么，每个客户端都相同：见[在 Beacon 的授权页面上允许它](claude-code.md#allow-it-on-beacons-consent-page)、[一次登录能做什么](../guide/people.md#what-a-sign-in-may-do)。

## 不连互联网时 {#without-the-internet}

Claude Code 和 Codex 的元数据文档，Beacon 要从互联网上取（`https://claude.ai/…`、`https://chatgpt.com/…`）。访问不了互联网的 Beacon 在 `config.yaml` 里把它关掉，然后重启：

```yaml
oauth:
  client_id_documents: false     # true by default
```

这样一来，Beacon：

- 在元数据里写明 `client_id_metadata_document_supported: false`；
- 拒绝 URL 形式的 `client_id`；
- 让客户端改为自己注册。`codex mcp login beacon --oauth-client-registration dcr` 让 Codex 这样做。
