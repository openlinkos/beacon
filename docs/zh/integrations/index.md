---
sourceHash: sha256-dc5acaff
---

# 接入

Beacon 怎样和周围的工具配合。这些页面分两组：连到 Beacon 的 Agent 客户端，以及出故障时在 Beacon 和其他系统之间往来的事件与通知。

## Agent 客户端 {#agent-clients}

Agent 作为某个人的 Agent，通过 MCP 或 REST API 连到 Beacon。它能做的事绝不超出这个人的角色。交互式客户端用 MCP 的 OAuth 在浏览器里登录。独自运行的程序用令牌。

1. [Claude Code](claude-code.md)：在浏览器里登录，并用你自己的 ssh、scp 和 rsync 上机器。
2. [Codex](codex.md)：同样的登录方式，以及连不上互联网的 Beacon 怎么办。
3. [Cursor](cursor.md)：在浏览器里登录，或者发送令牌。
4. [其他 MCP 客户端](mcp-clients.md)：任何通过 HTTP 使用 MCP 的客户端，以及两种登录方式。
5. [无头 Agent 与 CI](headless.md)：Agent 的令牌、钩子触发时用来运行 Agent 的 `beacon-oncall`，以及会自己续期的令牌。
6. [值班提示词](on-call-prompt.md)：被叫醒的 Agent 从哪段提示词开始。

## 事件与通知 {#events-and-notifications}

告警触发时从 Beacon 发出去什么，以及什么会进来帮着解释它。

1. [被 webhook 唤醒的 Agent](webhooks.md)：一个签名的 POST 发到你的接收端，以及怎样校验它。
2. [GitHub Actions](github-actions.md)：触发 workflow 的钩子，以及一个运行 Claude Code 的 workflow。
3. [部署与 Flux](change-events.md)：来自 CI 和 Flux 的变更事件，让证据说出告警之前刚改了什么。
4. [配置飞书应用](feishu.md)：告警以卡片发到飞书或 Lark 群里，值班的人用卡片上的按钮处理。

钩子什么时候触发、Agent 接着做什么，见[让 Agent 值班](../guide/on-call.md)。钩子发出的每个字段，见 [Webhook 参考](../reference/webhooks.md)。
