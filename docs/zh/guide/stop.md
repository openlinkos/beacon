---
sourceHash: sha256-4ff1954c
---

# 停下 Agent，暂停一切

读完这一页，你就知道 Beacon 的每一个刹车，以及每一个会关掉什么：

- **停下单个 Agent 的那些开关**：吊销令牌、吊销登录、删除密钥、收窄预设、停用所有者；
- **一次停下全部的那一个**：通道暂停。

哪一个都不靠运气。Agent 能做什么，在每个请求上重新计算。答案一变，它在机器上打开着的一切立刻关闭。

你需要一个所有者账号，或一个角色带 `machines:write`、能扳暂停开关的人（[邀请你的团队](people.md)）。命令在中心上执行，参考部署里是 `docker exec beacon beacon …`。

## 停下一个 Agent {#stop-one-agent}

想停多少，就选哪个开关：

| 开关 | 怎么扳 | 会关掉什么 |
| --- | --- | --- |
| 吊销令牌 | Agent 页面上的**Revoke**，或 `beacon token revoke NAME`。控制台的**Tokens**页删除能到达机器的令牌，限持有 `machines:write`、并持有该令牌全部权限的人（[删除令牌](../reference/permissions.md#deleting-tokens)） | 令牌从下一个请求起被拒绝。用它打开的东西（连接、转发端口、工具调用）立刻关闭，Agent 用它登记的密钥一并消失 |
| 吊销 OAuth 登录 | Agent 页面上的**Revoke**，或客户端自己登出（`claude mcp logout beacon`） | 同上，作用于这次登录打开的东西。客户端得重新登录 |
| 删除 SSH 密钥 | `remove_ssh_key`，或 Agent 的页面 | 用这把密钥登录的连接关闭 |
| 删除 Agent | `beacon agent remove NAME`，或它的页面 | 它的全部凭据（令牌、登录、密钥），及这些凭据打开的一切。它做过的事留在记录里 |
| 收窄 Agent | 它的页面，或中心上的 `beacon agent`：从其机器清单里去掉一台，或把预设降到 `machines:ssh` 以下 | 它在不再能到达的机器上打开的东西关闭。其余照常 |
| 停用个人 SSH 密钥 | 在**你的账号 → 个人 SSH 密钥**下点**移除** | 用这把密钥登录的连接关闭 |
| 停下所有者 | `beacon user disable NAME`，或 `beacon user role NAME viewer`（[改角色、停用某个成员](people.md#change-a-role-disable-someone)） | 本人及其 Agent 的 SSH 连接关闭。停用账号还会结束此人的控制台登录 |

吊销令牌也会结束用它登录的控制台会话。吊销你正用来登录的那个令牌，你会被登出。

Agent 做过的事（会话、录像、备注）保留 `ssh.keep`（180 天）。停下 Agent 永远不会删除记录。

Beacon 每分钟还会检查一遍期间过期的凭据。所以过了有效期的令牌不需要谁动手，也会关掉它打开着的东西。

## 暂停整条通道 {#pause-the-whole-channel}

暂停 SSH 通道可以同时停止人和 Agent 的访问。通道会关闭正在进行的客户端会话，并拒绝新会话，直到恢复。可以从命令行、控制台的**机器**页、API 或 MCP 暂停。只有人（在控制台上）或主机上的 `beacon ssh resume` 能恢复：Agent 或令牌来恢复会被拒绝，返回 `ssh.resume_forbidden`，所以 Agent 撤不掉这道刹车。

```bash
docker exec beacon beacon ssh pause     # 恢复是 ssh resume
```

你会看到 `agents' access to machines is paused since …`。`ssh status` 显示当前状态，控制台**Machines**页机器列表上方的开关也一样。

暂停期间：

- Agent 自己的 ssh 会收到 `beacon: agents' access to machines is paused since …; a person resumes it with beacon ssh resume`；
- MCP 工具和 API 也被同样的话拒绝；
- 监控不受影响：检测点照常汇报，告警照常到达。

暂停记入审计日志。API 路由是 `POST /api/v1/ssh/pause` 和 `/resume`，MCP 工具是 `pause_ssh` 和 `resume_ssh`。两者都要 `machines:write`，恢复还要是人，并都回显到告警群，所以通道暂停不会没人注意到。

暂停是整个机群的刹车。上面的开关只停一个 Agent，不停别人。

## 关掉什么，什么时候关 {#what-closes-and-when}

上面所有开关底下是同一个机制。Beacon 重新读一遍谁现在能做什么：

- 令牌或登录的 scope；
- Agent 的预设；
- 所有者的角色；
- 机器清单。

答案不再允许的部分（连接、转发端口、工具调用）立刻关闭，不等 Agent 再请求什么。正在执行命令的 Agent 会发现应答被截断。再试下一条命令则被拒绝，原因写在拒绝里。

这里的任何一项都不会停掉录像和审计。被开关关掉的会话保有其记录和回放。扳了哪个开关（`revoke_token`、`ssh_pause`、`user_disable`）、是谁扳的，都在审计日志里。

## 检查一下 {#check-it}

1. **暂停通道，让 Agent 执行一条命令。** 它会被上面那句话拒绝。**Machines**页和 `beacon ssh status` 都显示通道自何时起暂停。
2. **恢复之后，吊销某个 Agent 手里的一把令牌。** 在**Sessions**页上看它打开着的会话关闭。会话标记为已结束，回放完好。

Agent 因为别的原因连不上机器时，[Agent 连不上机器](../self-hosting/troubleshooting.md#the-agent-cannot-reach-a-machine)列出了每条消息和对应的处理。

## 下一步 {#next}

- [邀请你的团队](people.md)：这些开关所收窄的角色与预设。
- [给 Agent 一台机器](machines.md)：被暂停的那条通道，以及它期间的记录。
- [复查 Agent 做了什么](console.md)：被停下的 Agent 留下的会话，回放也在。
