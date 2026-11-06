---
sourceHash: sha256-f101fc8e
---

# Claude Code

Claude Code 通过浏览器、用 MCP 的 OAuth 登录 Beacon：添加 Beacon 的 `/mcp` 时不带令牌，从 Claude Code 登录，再在 Beacon 的授权页面上允许它。在运行 Claude Code 的工作站上：

```bash
claude mcp add --scope user --transport http beacon https://status.example.com/mcp
```

然后在 Claude Code 里执行 `/mcp`，选中 `beacon` 完成认证；也可以在 shell 里执行 `claude mcp login beacon`。

## 在 Beacon 的授权页面上允许它 {#allow-it-on-beacons-consent-page}

没登录控制台的话先登录，然后浏览器会打开 Beacon 的授权页面「Claude Code 想以你的身份使用 Beacon」。页面问四件事：

- **Agent 名称**：`claude-code`，或者你已有的那个 Claude Code 的 Agent。以后用同一个名字会沿用这个 Agent。别人 Agent 的名字会被拒绝，所以别人已经有一个 `claude-code` 时，表单给出的是 `claude-code-alice`。
- **可以做什么**：你的角色范围内的预设，默认选最强的那个，或客户端申请的预设中最强的那个；沿用已有的 Agent 时，不会比这个 Agent 自己的预设更强。你可以选更弱的。
- **机器**：全部，或只限你勾选的；沿用已有的 Agent 时，只能在它自己的机器里选。
- **有效多久**：1 天、30 天或 90 天，默认 30 天。

页面底部写着 Beacon 会把你送回哪里：你这台机器上 `localhost` 的一个端口，Claude Code 在那里等着。点**允许**，Beacon 创建这个 Agent，或原样沿用你已有的那个，并把一个一次性授权码交给 Claude Code，Claude Code 用它换取令牌；之后 `/mcp` 里会显示 `beacon` 已连接。点**拒绝**，Claude Code 会知道你不同意。两者都记入审计日志（`oauth_allow`、`oauth_deny`），允许还会回显到告警群。

此后 Claude Code 手里有一个有效一小时的访问令牌，和一个在你允许的时长内有效的刷新令牌，它自己刷新；你允许的时间到了，它会请你重新登录。整个过程没有令牌要复制、手动保存或贴进聊天里。

- **手边没有浏览器**，比如在 ssh 里：在交互式终端里执行 `claude mcp login beacon --no-browser`，它会打印要在别处打开的地址，再请你把被送回的地址粘贴回来。
- **退出登录**：`claude mcp logout beacon` 也会在 Beacon 这边结束这次登录。只执行 `claude mcp remove beacon` 会丢掉 Claude Code 手里的令牌，却不吊销它们，所以先退出登录，或在 Agent 页面上吊销这次登录。
- **请求头优先**：添加时带了 `--header "Authorization: Bearer …"` 的 server 会用那个令牌，不走 OAuth 登录。这样的令牌是你的某个 Agent 的凭据（[无头 Agent 与 CI](headless.md#make-an-agent-and-its-token)）。

Claude Code 用它的客户端 ID 元数据文档 `https://claude.ai/oauth/claude-code-client-metadata` 表明身份，Beacon 会去取，所以它什么都不用注册；它在 `http://localhost:<port>/callback` 等待回答。

Agent 能做什么，由它的预设和你的角色决定，每次请求时重新读取（[一次登录能做什么](../guide/people.md#what-a-sign-in-may-do)）。

## 用自己的 ssh、scp 和 rsync 连接 {#connect-with-your-own-ssh-scp-and-rsync}

Beacon 还需要这个 Agent 的公钥：对 Claude Code 说 *「把我的 SSH 公钥 ~/.ssh/id_ed25519.pub 登记到 Beacon」*，它就会调用 `add_ssh_key`（[SSH 密钥](../guide/people.md#ssh-keys)）。

`list_machines`（MCP）或 `GET /api/v1/machines` 返回机器列表、Agent 能否连每台机器，以及一段 `ssh_config` 和一行 `known_hosts`。把这段配置放进 `~/.ssh/config`，把那一行放进 `~/.ssh/known_hosts`：

```
Host edge-1
  HostName ssh.example.com
  Port 17002
  User edge-1
  HostKeyAlias beacon
```

之后 `ssh edge-1 'systemctl status relay'`、`scp`、`sftp`、`rsync -e ssh` 和 `ssh -L` 都和平常一样用。用户名告诉 Beacon 要去哪台机器；会话在机器上以 `beacon` 账户运行。`HostKeyAlias beacon` 让 ssh 校验 Beacon 自己的主机密钥，所有机器都是同一把；每台机器固定下来的主机密钥，由 Beacon 自己校验。

通道拒绝连接时，Agent 的 ssh 会打印原因并以 1 退出，比如 `beacon: this agent may not reach the machine edge-2; machines this agent reaches: edge-1`。各种提示及处理方法见[常见问题](../self-hosting/troubleshooting.md#the-agent-cannot-reach-a-machine)。通道记录什么，见[给 Agent 一台机器](../guide/machines.md)；怎样暂停，见[停下 Agent，暂停一切](../guide/stop.md)；它保证什么，见[安全模型](../self-hosting/security.md#the-ssh-channel)。
