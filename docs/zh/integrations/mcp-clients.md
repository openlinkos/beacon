---
sourceHash: sha256-703d9abe
---

# 其他 MCP 客户端

Beacon 的 MCP server 走流式 HTTP，地址是 `/mcp`，无状态，所以凡是会说 MCP over HTTP 的客户端都能连。以 VS Code 为例，把下面这段写进 `.vscode/mcp.json`：

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

## 客户端怎样登录 {#how-a-client-signs-in}

登录有两条路，客户端走其中一条即可：

- **MCP 的 OAuth**，给实现了它的客户端（Claude Code、Codex 和 Cursor 都有，见各自页面）。Beacon 发布 `/mcp` 的受保护资源元数据和授权服务器元数据（RFC 9728、RFC 8414）。它把客户端动态注册为公开客户端（RFC 7591），回调地址只收本机回环或 `https` 的。授权码流程带 PKCE（`S256`）。授权页面问 Agent 可以做什么：和 Claude Code 相同的四件事（见[在 Beacon 的授权页面上允许它](claude-code.md#allow-it-on-beacons-consent-page)）。
- **放在请求头里的令牌**，`Authorization: Bearer lbt_…`，其余一切用它：脚本、CI 任务、没有 OAuth 的客户端。令牌是你的某个 Agent 的凭据，而且会自行续期（[无头 Agent 与 CI](headless.md#make-an-agent-and-its-token)）。OAuth 访问令牌（`lba_…`）在凡收 bearer 令牌的地方都可用，所以登录过的客户端也可以把它放进请求头。

## 检查是否可用 {#check-it-works}

问 Beacon 这个令牌以谁的身份行事：

```bash
curl -H "Authorization: Bearer lbt_…" https://status.example.com/api/v1/token
```

回答的是这个令牌以谁的身份行事，内容是持有者看到的样子。MCP 工具 `whoami` 说的是同样的事。

每个工具都要作用域，每次调用都检查，每次调用都有记录。[MCP 工具](../reference/mcp.md)列出了全部工具和它们需要什么。
