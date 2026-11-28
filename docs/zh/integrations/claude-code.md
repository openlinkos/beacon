---
sourceHash: sha256-bd2ea9e7
---

# Claude Code

Claude Code 通过浏览器、用 MCP 的 OAuth 登录 Beacon。添加 Beacon 的 `/mcp` 时不带令牌，从 Claude Code 登录，再在 Beacon 的授权页面上允许它。

1. **添加 Beacon**。在运行 Claude Code 的工作站上：

   ```bash
   claude mcp add --scope user --transport http beacon https://status.example.com/mcp
   ```

2. **登录**。在 Claude Code 里执行 `/mcp`，选中 `beacon` 完成认证。也可以在 shell 里执行 `claude mcp login beacon`。
3. **在 Beacon 的授权页面上允许它**，见下一节。

## 在 Beacon 的授权页面上允许它 {#allow-it-on-beacons-consent-page}

浏览器会打开 Beacon 的授权页面「Claude Code 想以你的身份使用 Beacon」。没登录控制台的话，先登录。页面问四件事：

- **Agent 名称**：你之前为这个客户端创建的 Agent，或以它命名的新名字，例如 `claude-code`。表单从不默认给出为别的客户端创建的、或手动创建的 Agent，即使它和客户端同名：你可以手动填写它的名字来沿用它。以后用同一个名字会沿用这个 Agent。别人 Agent 的名字会被拒绝。所以别人已经有一个 `claude-code` 时，表单给出的是 `claude-code-alice`；你自己有一个以别的方式创建的 `claude-code` 时，给出的是 `claude-code-2`。
- **可以做什么**：你的角色范围内的预设。默认选最强的那个，或客户端申请的预设中最强的那个。沿用已有的 Agent 时，不会比这个 Agent 自己的预设更强。你可以选更弱的。
- **机器**：全部，或只限你勾选的。沿用已有的 Agent 时，只能在它自己的机器里选。
- **有效多久**：1 天、30 天或 90 天，默认 30 天。

页面底部写着 Beacon 会把你送回哪里：你这台机器上 `localhost` 的一个端口，Claude Code 在那里等着。

客户端的名称是它自己报的。所以页面顶部还写着 Beacon 另外知道的情况：描述这个客户端的元数据文档在哪个主机上（Claude Code 的是 `claude.ai`），或者这个客户端是什么时候自行注册的。授权结果要送到一个网站而不是你这台机器上的程序时，会有醒目的警告写出这个网站。只允许你自己刚刚发起登录的客户端。

请求有问题时，Beacon 停在自己的页面上，什么也不会发给客户端。只有你已经允许过的客户端，才会在它的回调地址收到错误。

- 点**允许**，Beacon 创建这个 Agent，或原样沿用你已有的那个。它把一个一次性授权码交给 Claude Code，Claude Code 用它换取令牌。之后 `/mcp` 里会显示 `beacon` 已连接。
- 点**拒绝**，Claude Code 会知道你不同意。

两者都记入审计日志（`oauth_allow`、`oauth_deny`），允许还会回显到告警群。

此后 Claude Code 手里有两个令牌：一个有效一小时的访问令牌，一个在你允许的时长内有效的刷新令牌。它自己刷新。你允许的时间到了，它会请你重新登录。整个过程没有令牌要复制、手动保存或贴进聊天里。

- **手边没有浏览器**，比如在 ssh 里：在交互式终端里执行 `claude mcp login beacon --no-browser`。它会打印要在别处打开的地址，再请你把被送回的地址粘贴回来。
- **退出登录**：`claude mcp logout beacon` 也会在 Beacon 这边结束这次登录。只执行 `claude mcp remove beacon` 会丢掉 Claude Code 手里的令牌，却不吊销它们。所以先退出登录，或在 Agent 页面上吊销这次登录。
- **请求头优先**：添加时带了 `--header "Authorization: Bearer …"` 的 server 会用那个令牌，不走 OAuth 登录。这样的令牌是你的某个 Agent 的凭据（[无头 Agent 与 CI](headless.md#make-an-agent-and-its-token)）。

Claude Code 用它的客户端 ID 元数据文档 `https://claude.ai/oauth/claude-code-client-metadata` 表明身份。Beacon 会去取这份文档，所以 Claude Code 什么都不用注册。它在 `http://localhost:<port>/callback` 等待回答。

Agent 能做什么，由它的预设和你的角色决定，每次请求时重新读取（[一次登录能做什么](../guide/people.md#what-a-sign-in-may-do)）。

## 用自己的 ssh、scp 和 rsync 连接 {#connect-with-your-own-ssh-scp-and-rsync}

1. **给这个 Agent 登记密钥**。在运行 Agent 的工作站上生成一把专用密钥。Beacon 只允许把同一个 SSH 密钥指纹登记给一个身份。

   ```bash
   mkdir -p -m 700 ~/.ssh
   ssh-keygen -t ed25519 -f ~/.ssh/beacon-claude-code
   ```

   请 Claude Code *「用 `add_ssh_key` 将 `~/.ssh/beacon-claude-code.pub` 的公钥登记到 Beacon」*。私钥留在工作站。若设置了口令，先运行 `ssh-add ~/.ssh/beacon-claude-code`，在已运行的 `ssh-agent` 中解锁密钥，再请 Claude Code 连接。
2. **取得机器列表**。`list_machines`（MCP）或 `GET /api/v1/machines` 返回机器列表，以及 Agent 能否连每台机器。它还给出一段 `ssh_config` 和一行 `known_hosts`。
3. **把配置段保存到 `~/.ssh/beacon-claude-code.conf`**，把主机密钥行加入 `~/.ssh/known_hosts`。在每台机器的 `Host` 段中添加 `IdentityFile` 和 `IdentitiesOnly`，让客户端选用 Agent 的密钥。`edge-1` 的完整示例如下。

   ```ssh-config
   Host edge-1
     HostName status.example.com
     Port 17002
     User edge-1
     HostKeyAlias beacon
     IdentityFile ~/.ssh/beacon-claude-code
     IdentitiesOnly yes
   ```

4. **连接**。请 Agent 执行 `ssh -F ~/.ssh/beacon-claude-code.conf edge-1 hostname`。`scp`、`sftp` 和 `ssh -L` 同样可用 `-F`，`rsync` 可用 `rsync -e 'ssh -F ~/.ssh/beacon-claude-code.conf'`。打开控制台的**会话**，回放刚执行的命令。操作人应显示为 `人 / Agent`。

用户名告诉 Beacon 要去哪台机器。会话在机器上以 `beacon` 账户运行。`HostKeyAlias beacon` 让 ssh 校验 Beacon 自己的主机密钥，所有机器都是同一把。每台机器固定下来的主机密钥，由 Beacon 自己校验。

通道拒绝连接时，Agent 的 ssh 会打印原因并以 1 退出。比如：`beacon: this agent may not reach the machine edge-2; machines this agent reaches: edge-1`。

各种提示及处理方法见[常见问题](../self-hosting/troubleshooting.md#the-agent-cannot-reach-a-machine)。关于通道的更多内容：

- 它记录什么：[给 Agent 一台机器](../guide/machines.md)。
- 怎样暂停它：[停下 Agent，暂停一切](../guide/stop.md)。
- 它保证什么：[安全模型](../self-hosting/security.md#the-ssh-channel)。
