---
sourceHash: sha256-b471161f
---

# Cursor

Cursor 经 HTTP 连接 Beacon 的 MCP server，用 MCP 的 OAuth 登录，和 Claude Code 一样：谁都不用复制令牌。把下面这段写进 Cursor 的 MCP 配置：

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

全局文件让 Beacon 到处可用；项目文件放在项目的 `.cursor/` 目录下，只对这个项目生效。同一个 server 也可以在 Cursor 的设置里添加，在 **MCP** 下面。

## 登录 {#sign-in}

第一次连接时，Cursor 自己找到 Beacon 的授权服务器，把自己注册为公开客户端，再打开浏览器：带 PKCE 的授权码流程。Beacon 的授权页面问的和 Claude Code 相同的四件事——Agent 名称（默认按客户端命名，也就是 `cursor`，除非你另起名字）、可以做什么、能连哪些机器、有效多久（见[在 Beacon 的授权页面上允许它](claude-code.md#allow-it-on-beacons-consent-page)）。

Cursor 的回调地址——桌面版是你机器上 `localhost` 的一个端口，从网页连接时是 `www.cursor.com` 上的一个 `https` 地址——都在 Beacon 接受的范围内。控制台上这个 Agent 的页面在 **OAuth 登录**下列出这次登录，可以在那里吊销它。

## 或者发送令牌 {#or-send-a-token}

不能登录的客户端，或者你不想让它登录的，改发令牌：

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

`${env:…}` 由 Cursor 从你的环境变量里填入，令牌本身不进文件。在控制台的 **Agent** 页面上，或用 `beacon agent add`，把它创建为你的某个 Agent 的凭据（[无头 Agent 与 CI](headless.md#make-an-agent-and-its-token)）；这个令牌就以那个 Agent 的身份行事，而且会自行续期。
