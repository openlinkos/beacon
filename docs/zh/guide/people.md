---
sourceHash: sha256-289d3faf
---

# 成员和 Agent

这一页写给 Beacon 的所有者。读完之后：团队里每个人都用自己的账号登录，角色决定他能做什么；每个人的 Agent 代表这个人行事，能做的事绝不超出这个人的角色；Claude Code 和 Codex 通过浏览器登录，谁都不用复制令牌；无界面的 Agent 或 CI 任务拿一个会自行续期的令牌。第一个账号你已经在[30 分钟装好](../self-hosting/install.md#4-the-first-agent)的第 4 步建好了，这一页从那里接着讲。

下面的内容建立在两个想法上。**人有角色，Agent 有预设**，所以谁都不用一个个敲作用域。**一切都记在某个人名下**：审计日志、告警群和会话记录里，Alice 在控制台做的事记为 `alice`，她的 Agent 做的事记为 `alice / claude-code`。人只能用 Beacon 自己的账号登录：不经 GitHub、飞书、OIDC、LDAP 或其他任何地方。

## 角色 {#roles}

每个角色能做上一个角色能做的一切，再多一些：

| 角色 | 能做什么 | 给出的作用域 |
| --- | --- | --- |
| 查看者 | 看到全部内容：告警、机器、会话和回放、配置、审计日志 | `status:read`、`sessions:read` |
| 值班 | 还能处理告警；他的 Agent 经 SSH 通道访问机器 | + `alerts:write`、`machines:ssh` |
| 运维 | 还能添加和移除机器、暂停 Agent、安排维护 | + `maintenance:write`、`incidents:write`、`machines:write` |
| 所有者 | 还能修改配置、邀请成员、设置角色、管理所有 Agent | + `config:write`、`accounts:write` |

控制台、命令行和 API 说的都是角色；作用域留在底下，[REST API](../reference/api.md#scopes) 里列着它们。角色在每次请求时重新读取，所以改动对这个人和他的 Agent 都立即生效。Beacon 始终保留至少一个启用的所有者，会让它一个都不剩的改动一律拒绝。

## 第一位所有者 {#the-first-owner}

还没有账号的 Beacon 启动时，会把一个用于初始化的一次性链接写进日志：

```json
{"time":"…","level":"WARN","msg":"Beacon has no accounts yet: open this one-time link within 30 minutes to make the first, an owner; beacon setup prints a new one","setup_link":"https://status.example.com/console/setup?code=…","expires":"…"}
```

用 `docker compose logs beacon | grep setup_link` 就能找到。过期了的话，在中心上再打印一个：

```bash
docker exec beacon beacon setup
```

```
Open this link within 30 minutes (until 2026-11-02 10:30:00) to make the first account, an owner. It works once:
https://status.example.com/console/setup?code=…
```

链接打开的是**初始化 Beacon**页面：设置用户名、显示名和密码。这个账号就是所有者，而且已经登录。每个链接只能用一次；Beacon 一旦有了账号，任何初始化链接都不再有效，此后成员都凭邀请加入。链接由 `public_url` 拼成；没设 `public_url` 时，`beacon setup` 只打印路径，要你自己在前面加上 Beacon 的地址。

第一个账号建好之前，登录页面只提供令牌登录，并告诉你初始化链接在哪里。

## 邀请成员 {#invite-people}

所有者按每个人需要的角色发出邀请：在控制台的**成员**页面点**邀请成员**，选好角色，写一条备注说明是给谁的；也可以在中心上：

```bash
docker exec beacon beacon user invite -role oncall -note "Bob, on call from Monday"
```

```
An invitation to join as oncall, until 2026-11-03 09:00:00. Send the link to that person only; it works once:
https://status.example.com/console/join?code=…
```

链接只在这时显示一次，只能用一次，24 小时内有效。对方打开链接，设置用户名和密码，就以邀请给的角色登录了。**成员**页面列出尚未使用的邀请，点**撤回**可以作废一份。告警群只会知道有人发了邀请、有人加入了，从不会看到链接。

用户名是 2 到 32 个小写字母、数字、点、短横线或下划线，以字母或数字开头。审计日志用它称呼这个人，所以它永远不变；`beacon`、`root`、`rules` 和 `unknown` 是保留的。显示名只决定控制台上怎么显示这个人。

## 登录 {#sign-in}

`/console/sign-in` 要用户名和密码。没登录的人打开控制台的任何页面都会被带到这里，登录后再回到原来那一页。一次登录有效 12 小时；cookie 的细节和表单上每条提示的含义见[控制台](console.md#signing-in)。

- 同一地址 10 分钟内登录失败 20 次，这个地址在这 10 分钟剩下的时间里都会被拒绝，和 API 的令牌一样。同一用户名 10 分钟内密码错 10 次，不论来自哪里，这个用户名在剩下的时间里都会被拒绝，连正确的密码也不例外。
- 用户名错和密码错得到同样的回答，「用户名或密码不对。」，耗时也一样。
- 表单下方的**改用令牌登录**还能粘贴 Beacon 令牌登录，再保留一个版本：给脚本用，也给还没有账号的 Beacon 用。

## 密码 {#passwords}

密码至少 10 个字符，不能是用户名，也不能是最常见的密码之一；除此之外没有别的规则。Beacon 只保存它的 argon2id 哈希（64 MiB、3 轮、4 条并行），用常数时间比较；这些参数变了，会在下次登录时重新计算哈希。

**修改自己的密码**：打开侧栏底部菜单里的**你的账号**（`/console/account`），填上当前密码和新密码。当前密码输错，和登录失败一样计入限额。你在其他地方的登录都会结束，修改时所在的这次登录保留。

**忘了密码**由所有者来重设：在**成员**页面点**重设密码链接**，或在中心上：

```bash
docker exec beacon beacon user reset bob
```

```
With this link bob chooses a new password, once, until 2026-11-02 10:30:00; their sign-ins end then:
https://status.example.com/console/reset?code=…
```

链接只显示一次，只能用一次，30 分钟内有效。对方用它设置新密码，他的所有登录随之结束。

## 改角色、停用成员 {#change-a-role-disable-someone}

在**成员**页面，或在中心上：

```bash
docker exec beacon beacon user role bob operator   # bob is operator now; their sessions and agents follow at once
docker exec beacon beacon user disable bob         # bob is disabled
docker exec beacon beacon user enable bob          # bob is enabled
```

角色降低会立即限制这个人的登录和他的 Agent。停用一个人会结束他的登录、停下他的 Agent，并关闭这些 Agent 在机器上打开着的一切。在控制台上不能停用自己。`beacon user list` 列出所有人，带角色、状态、最近登录和 Agent，后面是尚未使用的邀请。

## Agent {#agents}

Agent 是代表某一个人行事的程序，这个人是它的所有者：某人工作站上的 Claude Code，Hook 唤醒时 `beacon-oncall` 启动的 Agent，一个 CI 任务。它有：

- **名称**，2 到 40 个小写字母、数字、点、短横线或下划线，在 Beacon 里唯一；审计日志称它为 `所有者 / 名称`；
- **预设**，决定它能做什么；
- 它可经 SSH 通道访问的**机器**：全部，或一份列表；
- 它的 **SSH 密钥**，给它自己的 ssh、scp、sftp 和 rsync 用；
- 它的**凭据**：令牌（`lbt_…`）给无界面的 Agent 和 CI，OAuth 登录给交互式客户端；需要的话，每种都可以有多个。

| 预设 | 申请的作用域 |
| --- | --- |
| 只读 | 查看者的作用域 |
| 值班 | 值班成员的作用域：还能处理告警、访问机器 |
| 运维 | 运维成员的作用域 |

Agent 能做的，是它的预设申请的、**并且**它的所有者的角色允许的，每次请求时重新计算。值班成员的 Agent 永远不会超出值班；降低或停用这个人，会立即限制或停下他的 Agent，作用在它们手里已有的令牌上。没有哪个预设给 `config:write` 或 `accounts:write`。创建或修改 Agent 时，超出所有者角色的预设会被拒绝。

**自定义**的 Agent 不用预设，而是列出自己的作用域：归入的旧令牌就这样保留原有的作用域（[从令牌迁移](#moving-from-tokens)），所有者也可以在命令行上创建一个（`beacon agent add … -scopes status:read,config:write`），同样不超出所有者的角色。

控制台的 **Agent** 页面列出你的 Agent；所有者看到所有人的。每个 Agent 自己的页面显示它现在能做什么，可以修改预设和机器，列出它的 OAuth 登录、令牌和密钥并吊销或移除其中任何一个，生成新令牌，也可以连同全部凭据删除这个 Agent。每个人创建和管理自己的 Agent；所有者可以管理任何人的。

## 接入 Claude Code {#connect-claude-code}

Claude Code 通过浏览器、用 MCP 的 OAuth 登录，既能用 Beacon 的工具，也能用它自己的 ssh、scp 和 rsync 访问机器：见[接入 Claude Code](../integrations/claude-code.md)。

## 接入 Codex {#connect-codex}

Codex 的登录方式相同，不连互联网的 Beacon 也能收下两者：见[接入 Codex](../integrations/codex.md)。

## 一次登录能做什么 {#what-a-sign-in-may-do}

每次登录都把你允许的内容当作它自己的上限：客户端能做的，是这次登录的预设和机器允许的，不超出 Agent 能做的，也不超出你的角色，每次请求时重新读取。允许客户端从不改动已有的 Agent，所以别的客户端的登录、这个 Agent 的令牌和密钥都不会因此放宽。要让沿用的 Agent 做更多，先在它的页面上修改这个 Agent，再从客户端重新登录。

Agent 页面的 **OAuth 登录**下列出每次登录：客户端、当初允许的内容（**允许范围**：预设和机器）、何时创建、最近一次使用、何时结束；点**吊销**结束它。用令牌登录控制台的人不能允许客户端：OAuth 客户端总是代表某个人行事。

## 无界面的 Agent 和 CI {#headless-agents-and-ci}

独自运行的程序拿一个令牌，在控制台的 **Agent** 页面或用 `beacon agent add` 创建，令牌还会自行续期；被钩子唤醒的 Agent 用 `beacon-oncall` 这个参考接收程序：见[无头 Agent 与 CI](../integrations/headless.md)。

## SSH 密钥 {#ssh-keys}

SSH 通道凭公钥让 Agent 登录。Agent 用它已有的凭据，经 MCP 或 API 自己登记公钥，谁都不用到处复制密钥：

| 工具 | 接口 | 做什么 |
| --- | --- | --- |
| `add_ssh_key` | `POST /api/v1/ssh-keys` | 登记一把公钥（一行 `authorized_keys`），可带备注 |
| `list_ssh_keys` | `GET /api/v1/ssh-keys` | 这个 Agent 的公钥 |
| `remove_ssh_key` | `DELETE /api/v1/ssh-keys` | 按指纹移除一把；凭它登录的连接随即关闭 |

它们需要 `machines:ssh`。对 Claude Code 说 *「把我的 SSH 公钥 ~/.ssh/id_ed25519.pub 登记到 Beacon」*，它就会调用 `add_ssh_key`；之后 `list_machines` 会给出它自己的 ssh 用的 `ssh_config` 片段（[接入 Claude Code](../integrations/claude-code.md#connect-with-your-own-ssh-scp-and-rsync)）。

- **Agent 自己登记的密钥**属于它登记时所用的凭据，一个令牌或一次 OAuth 登录（`via`：`token:NAME` 或 `grant:ID`）。只有那个凭据有效时它才能登录，能做的也以那个凭据为限；凭据被吊销或 Agent 被删除时，它随之移除。
- **所有者添加的密钥**，在 Agent 页面上点**添加 SSH 密钥**，或在 `beacon agent add` 时用 `-ssh-key`，留在 Agent 上，只要 Agent 还有未过期的凭据就能登录。
- 一个 Agent 最多 20 把密钥。一把密钥只属于一个 Agent 或令牌，绝不会是机器的连接器密钥，而且只能到达它的持有者能到达的机器。每次添加或移除都会告诉告警群。

## 停下一个 Agent {#stop-an-agent}

被吊销或收窄的凭据从下一个请求起就会被拒绝；Agent 经 SSH 通道在机器上打开着的东西（连接、端口转发、工具调用），一旦它不再能访问那台机器，也会立即关闭。以下情况都会这样：

- 吊销一个令牌（Agent 页面上的**吊销**，或 `beacon token revoke NAME`）：关闭用它打开的东西、Agent 用它登记的密钥以及凭这些密钥登录的会话；如果这是 Agent 的最后一个凭据，凭 Agent 其他密钥登录的会话也一起关闭；
- 吊销一次 OAuth 登录，在 Agent 页面上，或由客户端退出登录（`claude mcp logout beacon`）：同上；
- 删除 Agent；
- 把一台机器从它的列表里去掉，或它的预设不再给 `machines:ssh`；
- 停用它的所有者，或把所有者降到值班以下；
- 移除会话登录时用的那把密钥。

Beacon 还会每分钟检查一次，处理这期间过期的凭据。要一下子停下所有 Agent，就暂停通道（[暂停](machines.md#pausing)）。

## 审计日志怎么称呼 {#who-the-audit-log-names}

| 谁做的 | 称呼 |
| --- | --- |
| 控制台上的成员 | `alice` |
| Agent，经它的任何凭据或 SSH 密钥 | `alice / claude-code` |
| 旧令牌，或机器的令牌 | 令牌的名字，与以前一样 |
| 中心上的命令行 | `cli:` 加操作系统用户名 |
| Beacon 自己 | `beacon` |

告警群的回显和机器上的会话用的是同样的名字。以前的记录保留原来的名字。

## 从令牌迁移 {#moving-from-tokens}

在有账号之前就在运行的 Beacon 上，每个令牌都照旧可用，保留自己的作用域，审计日志仍以它的名字称呼它。什么都不做，也什么都不会坏。要把成员和 Agent 引进来：

1. **启动这个版本。**还没有账号时，它把初始化链接写进日志，并写明找到了多少个旧令牌（`legacy_tokens`）。打开链接，建好你的所有者账号（[第一位所有者](#the-first-owner)）。
2. **邀请成员**（**成员**页面，或 `beacon user invite`），各自给角色。
3. **把 Agent 在用的令牌归入**对应成员的 Agent（见下文），或者保持原样。
4. **吊销成员以前用来登录控制台的令牌**，在他们有了账号之后。
5. **方便时把交互式 Agent 改为 OAuth**：`claude mcp remove beacon`，然后不带请求头重新添加，再登录。

**归入一个令牌。**控制台的**令牌**页面会把只带成员作用域、不属于任何 Agent 的令牌标为**旧令牌**。所有者可以把它归为某个成员的 Agent：在这个页面上点**归入**，或在中心上：

```bash
docker exec beacon beacon token claim claude-code -owner alice
```

```
token claude-code is agent alice / claude-code's now (oncall, may do status:read,sessions:read,alerts:write,machines:ssh); it keeps its secret and expiry
```

- 令牌保留原有的密钥和到期时间，用着它的程序照常工作。它的机器和 SSH 密钥移到 Agent 上，此后它能做的，是 Agent 的预设在这个成员角色范围内允许的。之后用这个令牌在控制台的登录，也以 `alice / claude-code` 的身份行事。
- 预设取作用域与令牌完全一致的那个，都不一致时取自定义（保留令牌的作用域），所以归入从不放大令牌的权限。`-preset` 可以另选预设。
- `-agent` 给 Agent 另起名字，或指定同一个成员已有的某个 Agent，把令牌加进去。如果那个 Agent 能做的比令牌现在能做的多（作用域或机器），这样会被拒绝：这时把令牌归为一个它自己的 Agent。
- 机器的令牌（检测点、主机、心跳、规则引擎、变更发送方、指标读取方）从来不算旧令牌，也不能归入：它们保持原样。

## 命令行 {#the-command-line}

| 命令 | 做什么 |
| --- | --- |
| `beacon setup` | Beacon 还没有账号时，打印一个新的初始化链接 |
| `beacon user list` | 成员，他们的角色、状态、最近登录和 Agent，以及尚未使用的邀请 |
| `beacon user invite -role ROLE [-note TEXT]` | 打印一个邀请链接 |
| `beacon user role NAME ROLE` | 修改一个成员的角色 |
| `beacon user disable NAME`、`beacon user enable NAME` | 停用或启用一个成员和他的 Agent |
| `beacon user reset NAME` | 打印一个重设密码链接 |
| `beacon agent add NAME -owner USER [-preset P] [-machines a,b] [-expires 365d] [-ssh-key KEY]` | 创建 Agent 和它的令牌（`-ssh-key @FILE` 从文件读取公钥；用 `-scopes a,b` 代替 `-preset` 则为自定义）；令牌在标准输出，命令在标准错误 |
| `beacon agent list`、`beacon agent remove NAME` | 列出 Agent；连同凭据删除一个 |
| `beacon token claim NAME -owner USER [-agent NAME] [-preset P]` | 把旧令牌归为某个 Agent 的 |

`ROLE` 是 `viewer`、`oncall`、`operator` 或 `owner`；预设是 `readonly`、`oncall` 或 `operator`。`beacon token create` 仍然保留，给机器的令牌和熟手用。同样的操作也在 REST API 的 `/api/v1/users` 和 `/api/v1/ssh-keys` 下，以及 MCP 工具 `list_users` 和 `invite_user`（[REST API](../reference/api.md#people-and-ssh-keys)、[MCP 工具](../reference/mcp.md#people-and-ssh-keys)）。它们需要 `accounts:write`，而没有哪个预设给它：只有所有者在命令行上把这个作用域给了 Agent，Agent 才能管理成员。修改角色、停用成员和重设密码都不经 MCP 提供。

## 安全 {#security}

- **登录 Beacon 只有 Beacon 这一处，它只保存哈希，从不保存秘密**：密码用 argon2id；令牌、OAuth 访问令牌和刷新令牌、初始化链接、邀请链接和重设链接的代码，以及控制台会话 ID，都用 SHA-256。
- **初始化链接是有意写进日志的**，好让第一位所有者不用令牌就能建好。它只有 30 分钟有效，只能用一次，而且只在 Beacon 还没有账号时有效。
- **Agent 能做什么，每次请求时都重新决定**：按它的预设和所有者的角色；对 OAuth 客户端，还要看它那次登录被允许的预设和机器。允许客户端从不放宽已有的 Agent；Agent 自己登记的密钥，也只在登记时所用的凭据有效期间才有效。
- **OAuth 访问令牌**（`lba_…`）有效一小时，凡是收 bearer 令牌的地方都接受：REST API 和 MCP；SSH 通道凭密钥登录。**刷新令牌**（`lbr_…`）的有效期是成员允许的那么久，每用一次就换一个新的；再提交一个已被换掉的，会吊销这整次登录，告警群也会知道。在 `/oauth/revoke` 吊销一次登录的任何一个令牌（客户端退出登录时就会这样做），都会结束这整次登录。**授权码**只能用一次，60 秒内有效，而且必须带 PKCE（`S256`）：校验码不对就作废，同一个授权码提交两次，会吊销它换来的一切。
- **客户端都是公开客户端**：没有客户端密钥。客户端注册时的回调地址只能在本机回环上（`http://127.0.0.1`、`http://localhost`、`http://[::1]`，任意端口），或是 `https` 地址，别的都不行；每个地址 10 分钟内最多注册 10 次，从未用过的注册一天后作废。用客户端 ID 元数据文档（一个 `https` 地址）表明身份的客户端，除非 `oauth.client_id_documents` 为 false，Beacon 会去取这份文档：有超时，最多 64 KiB、三次跳转且都必须是 `https`，无论域名解析成什么都绝不连私有地址，取到的保留 10 分钟。
- **Beacon 写明自己的身份**（`iss`）：对每个授权请求的回答都带上它，与元数据里的颁发者一致；作为申请的 `resource`，只接受它自己的 `/mcp` 或它自己。授权页面的表单只能通往客户端的回调地址，它的表单都带着会话的 CSRF 密钥。
- **全程 HTTPS**：控制台的 cookie 是 `Secure` 的，所以要经 HTTPS、在 `public_url` 下提供 Beacon，`public_url` 也是客户端核对的颁发者。Claude Code 只把凭据发给 `https` 的令牌端点，或 `localhost` 上的。

### OAuth 端点 {#the-oauth-endpoints}

| 接口 | 做什么 |
| --- | --- |
| `GET /.well-known/oauth-protected-resource/mcp`、`GET /.well-known/oauth-protected-resource` | `/mcp` 的受保护资源元数据（RFC 9728），以及整个 Beacon 的。`/mcp` 回答 `401` 时，`WWW-Authenticate` 里指向前一个 |
| `GET /.well-known/oauth-authorization-server` | 授权服务器的元数据（RFC 8414）：各端点、`S256`、作为 scope 的各预设和 `offline_access`、公开客户端、客户端 ID 元数据文档 |
| `POST /oauth/register` | 动态客户端注册（RFC 7591），只接受公开客户端 |
| `GET /oauth/authorize`、`POST /oauth/authorize` | 授权页面，给已登录控制台的成员 |
| `POST /oauth/token` | 用授权码或刷新令牌换取令牌 |
| `POST /oauth/revoke` | 吊销（RFC 7009）：一次登录的任何一个令牌，访问令牌或刷新令牌，都会结束这整次登录，并关闭它在机器上打开着的东西 |
