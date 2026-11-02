---
sourceHash: sha256-6dda19e1
---

# 成员、Agent 与登录

Beacon 是给团队用的：人用 Beacon 自己的账号登录，每个人的 Agent 代表这个人行事，能做的事不超过这个人。审计日志和告警群把两者都写明：Alice 在控制台做的事记为 `alice`，她的 Agent 做的事记为 `alice / claude-code`。登录 Beacon 只有 Beacon 自己这一处：不经 GitHub、飞书、OIDC、LDAP 或其他任何地方登录。

Agent 按 Agent 的习惯接入：Claude Code、Codex 这类交互式客户端经浏览器登录（MCP 的 OAuth），无界面的 Agent 或 CI 任务拿一个令牌，连同使用它的命令，直接粘贴即可。谁都不用到处抄权限范围字符串：人有角色，Agent 有预设。

## 成员与角色 {#people-and-roles}

一个成员有用户名（2 到 32 个小写字母、数字、点、短横线或下划线，以字母或数字开头；永不改变）、显示名、角色和密码。每个角色能做前一个角色能做的事，再多一些：

| 角色 | 增加了什么 | 给出的权限范围 |
| --- | --- | --- |
| 查看者 | 能看到全部内容：告警、机器、会话和回放、配置、审计日志 | `status:read`、`sessions:read` |
| 值班 | 处理告警；他的 Agent 经 SSH 通道访问机器 | + `alerts:write`、`machines:ssh` |
| 运维 | 添加和移除机器、暂停 Agent、安排维护 | + `maintenance:write`、`incidents:write`、`machines:write` |
| 所有者 | 修改配置、邀请成员、设置角色、管理所有 Agent | + `config:write`、`accounts:write` |

控制台、命令行和 API 说的都是角色；权限范围留在底下，如 [REST API](reference/api.md#scopes) 所列。成员的角色在每次请求时重新读取，所以改动立即生效。Beacon 始终保留至少一个启用的所有者：会让它一个都不剩的改动会被拒绝。

## 第一位所有者 {#the-first-owner}

没有账号的 Beacon 会生成一个用来初始化的一次性链接，有效 30 分钟，启动时写进日志：

```json
{"level":"WARN","msg":"Beacon has no accounts yet: open this one-time link within 30 minutes to make the first, an owner; beacon setup prints a new one","setup_link":"https://status.example.com/console/setup?code=…","expires":"…"}
```

需要时，`docker exec beacon beacon setup` 再打印一个。链接打开 `/console/setup`，在那里选定用户名、显示名和密码：这个账号就是所有者，并且已经登录。初始化链接只能用一次，而 Beacon 一旦有了账号，任何初始化链接都不再有效。链接由 `public_url` 拼成；没有它，命令只打印路径，要自己在前面加上 Beacon 的地址。

## 邀请成员 {#inviting-people}

所有者在控制台的**成员**页面邀请成员，选定角色，写一条备注说明是给谁的；也可以在主机上：

```bash
docker exec beacon beacon user invite -role oncall -note "Bob, on call from Monday"
```

两种方式下，Beacon 都只显示一次链接（`/console/join?code=…`），24 小时内有效，只能用一次：只发给那个人。对方选定用户名和密码后即登录，角色就是邀请给的那个。**成员**页面列出尚未使用的邀请，也能撤回。告警群只会知道有人发了邀请、有人加入了，从不会看到链接。

## 登录 {#signing-in}

`/console/sign-in` 要用户名和密码；未登录的人打开控制台的任何页面都会被带到这里，登录后再回到原来的页面。

- 一次登录有效 12 小时。浏览器只拿着会话的 ID，放在 `HttpOnly`、`Secure`、`SameSite=Strict` 的 cookie 里；数据库只保存它的 SHA-256。
- 同一地址 10 分钟内登录失败 20 次，这个地址在这 10 分钟剩下的时间里会被拒绝，与 API 的令牌一样；同一用户名 10 分钟内密码错 10 次，不论来自哪里，这个用户名在剩下的时间里都会被拒绝，连正确的密码也不例外。
- 用户名错和密码错得到同样的回答，耗时也一样。
- 表单下方的**改用令牌登录**还能粘贴 Beacon 令牌登录，再保留一个版本：给脚本用，也给还没有账号的 Beacon 的管理者用。

## 密码 {#passwords}

密码至少 10 个字符，不能是用户名，也不能是最常见的密码之一；除此之外没有别的规则。Beacon 只保存它的 argon2id 哈希（64 MiB、3 轮、4 条并行），以常数时间比较；这些参数变了，会在下次登录时重新计算哈希。

### 修改你的密码 {#changing-your-password}

在 `/console/account`（侧栏底部菜单里的**你的账号**），输入当前密码修改；当前密码输错，和登录失败一样计入限额。你在其他地方的登录都会结束；修改时所在的这次登录保留。

### 忘了密码 {#a-forgotten-password}

所有者在**成员**页面生成重设链接，或在主机上：

```bash
docker exec beacon beacon user reset bob
```

链接（`/console/reset?code=…`）只显示一次，只能用一次，30 分钟内有效。对方用它设置新密码，他的所有登录随之结束。

## 改角色、停用成员 {#changing-a-role-disabling-someone}

在**成员**页面，或用 `beacon user role bob operator`、`beacon user disable bob` 和 `beacon user enable bob`。角色降低会立即限制这个人的登录和 Agent；停用会结束他的登录、停下他的 Agent，并关闭这些 Agent 在机器上打开着的一切。在控制台上不能停用自己。

## Agent {#agents}

Agent 代表一个人行事，这个人是它的所有者。它有：

- **名称**，2 到 40 个小写字母、数字、点、短横线或下划线，在 Beacon 里唯一；审计日志称它为 `所有者 / 名称`；
- **预设**，它可以做什么；
- 它可经 SSH 通道访问的**机器**，全部，或一份列表；
- 它的 **SSH 密钥**，供它自己的 ssh、scp、sftp 和 rsync 使用；
- 它的**凭据**：令牌（`lbt_…`），给无界面的 Agent 和 CI；OAuth 登录，给交互式客户端。每种都可以有多个。

控制台的 **Agent** 页面列出你的 Agent（所有者看到所有人的）；每个 Agent 自己的页面可以修改预设和机器，列出它的令牌、OAuth 登录（各自带着当初允许的内容）和密钥，吊销其中任何一个，也可以连同全部凭据删除这个 Agent。成员为自己创建 Agent；所有者可以为任何人创建。

### 预设 {#presets}

| 预设 | 申请的权限 |
| --- | --- |
| 只读 | 查看者的权限范围 |
| 值班 | 值班成员的权限范围 |
| 运维 | 运维成员的权限范围 |

Agent 能做的，是它的预设申请的、**并且**它的所有者的角色允许的，每次请求时重新计算：值班成员的 Agent 永远不会超过值班；降低或停用这个人，会立即限制或停下他的 Agent，作用在它们手里已有的令牌上。没有哪个预设给 `config:write` 或 `accounts:write`。创建或修改 Agent 时，超出所有者角色的预设会被拒绝。

**自定义**的 Agent 改为列出自己的权限范围：归入的旧令牌就这样保留它的权限范围，所有者也可以在命令行上创建（`beacon agent add … -scopes status:read,config:write`），同样不超出所有者的角色。

## 接入 Claude Code 或 Codex {#connecting-claude-code-or-codex}

交互式客户端经浏览器、用 MCP 的 OAuth 登录：你添加 Beacon 的 `/mcp`，从客户端登录，在 Beacon 的页面上允许它。不用复制令牌，不用手动保存，也不会贴进聊天里。

### Claude Code {#claude-code}

```bash
claude mcp add --transport http beacon https://status.example.com/mcp
claude mcp login beacon
```

也可以在 Claude Code 里运行 `/mcp`，选择 Beacon。Claude Code 用它的客户端 ID 元数据文档（`https://claude.ai/oauth/claude-code-client-metadata`）表明身份，Beacon 会去取，所以它什么都不用注册；它在 `http://localhost:<port>/callback` 等待回答。浏览器会打开 Beacon 的授权页面；你允许之后，Claude Code 自己保存令牌、自己刷新。手边没有浏览器时（经 ssh），在交互式终端里运行 `claude mcp login beacon --no-browser`，它会打印要在别处打开的地址，再请你把被送回的地址粘贴回来。`claude mcp logout beacon` 也会在 Beacon 这边结束这次登录；只运行 `claude mcp remove beacon` 会丢掉 Claude Code 手里的令牌，却不吊销它们，所以要先退出登录。添加时带了 `Authorization` 头的 server 会用那个令牌，不走 OAuth 登录。

### Codex {#codex}

```bash
codex mcp add beacon --url https://status.example.com/mcp
codex mcp login beacon
```

Codex 用它自己的客户端 ID 元数据文档（`https://chatgpt.com/oauth/codex/client.json`）表明身份，Beacon 会去取，所以它也什么都不用注册；它在 `http://127.0.0.1:<port>/callback` 等待回答。没有元数据文档的客户端会自己注册（动态客户端注册）。

### 不连互联网时 {#without-the-internet}

Claude Code 和 Codex 的元数据文档，Beacon 要从互联网上取（`https://claude.ai/…`、`https://chatgpt.com/…`）。访问不了互联网的 Beacon 把它关掉：

```yaml
oauth:
  client_id_documents: false     # true by default
```

这样它的元数据会写明 `client_id_metadata_document_supported: false`，它拒绝 URL 形式的 `client_id`，客户端改用动态注册来注册自己（`codex mcp login beacon --oauth-client-registration dcr` 让 Codex 这样做）。

### 允许客户端 {#allowing-a-client}

从客户端登录会把浏览器带到 `/oauth/authorize`：没有登录控制台的话先登录，然后看到：

- 谁在申请，用客户端自己给的名字，以及 Beacon 会把你送回的地址（对这些客户端来说，是你机器上 `127.0.0.1` 或 `localhost` 的一个端口）；
- **Agent 名称**：你为这个客户端已有的 Agent，没有的话用客户端的名字生成一个（`claude-code`）。以后用同一个名字会沿用这个 Agent；别人 Agent 的名字会被拒绝；
- **可以做什么**：你的角色范围内的预设，沿用的 Agent 则不会比它自己的预设更强；默认选最强的那个，或客户端申请的预设中最强的那个；你可以选更弱的；
- 它可访问的**机器**：全部，或只限你勾选的；沿用的 Agent 只能在它自己的机器里选；
- **有效多久**：1 天、30 天或 90 天，默认 30 天。不论客户端有没有申请 `offline_access`，它都会拿到在这段时间内有效的刷新令牌，到期后再来询问。

**允许**会按你允许的预设和机器创建一个新 Agent，或原样沿用你已有的那个，并把一个一次性授权码送回客户端；**拒绝**会告诉客户端你不同意。两者都记入审计日志（`oauth_allow`、`oauth_deny`），允许还会回显到告警群。

每次登录都把你允许的内容当作自己的上限：客户端能做的，是这次登录的预设和机器允许的，不超出 Agent 能做的，也不超出你的角色，每次请求时重新读取。允许客户端从不改动已有的 Agent，所以别的客户端的登录、这个 Agent 的令牌和密钥都不会因此放宽；要让沿用的 Agent 做更多，先在它的页面上修改这个 Agent，再从客户端重新登录。每次登录都列在 Agent 页面的 **OAuth 登录**下，显示客户端、当初允许的内容（**允许范围**一栏：预设和机器）、何时创建、最近一次使用、何时结束；**吊销**结束它。用令牌登录控制台的人不能允许客户端：OAuth 客户端总是代表一个人行事。

## 无界面的 Agent 和 CI {#headless-agents-and-ci}

独自运行的程序拿一个令牌。在控制台上，**Agent** → **无界面的 Agent 或 CI 任务**：名称、预设、机器，以及令牌的有效期（默认 365 天）。在主机上：

```bash
docker exec beacon beacon agent add ci-deploy -owner alice -preset oncall -machines edge-1,edge-2 -expires 365d
```

令牌只显示一次（命令行上单独打印在标准输出里），同时给出使用它的命令，直接粘贴即可：带 `--header "Authorization: Bearer …"` 的 Claude Code、带 `--bearer-token-env-var BEACON_TOKEN` 的 Codex、curl，以及 Agent 有了密钥之后的 `~/.ssh/config` 片段。Beacon 只保存它的 SHA-256。令牌会在到期前自行续期（[自行续期的令牌](agents.md#tokens-that-renew-themselves)），所以不用有人再发新的；要另一个，在 Agent 页面上点**新令牌**。`beacon agent list` 列出 Agent，`beacon agent remove NAME` 删除一个。

## SSH 密钥 {#ssh-keys}

SSH 通道凭公钥让 Agent 登录。Agent 用它已有的凭据，经 MCP 登记自己的公钥：

| 工具 | 路径 | 做什么 |
| --- | --- | --- |
| `add_ssh_key` | `POST /api/v1/ssh-keys` | 登记你的公钥（一行 `authorized_keys`），可带备注 |
| `list_ssh_keys` | `GET /api/v1/ssh-keys` | 你的公钥 |
| `remove_ssh_key` | `DELETE /api/v1/ssh-keys` | 按指纹移除一个 |

它们需要 `machines:ssh`。Agent 自己登记的密钥属于它登记时所用的凭据，一个令牌或一次 OAuth 登录（`via`：`token:NAME` 或 `grant:ID`）：只在那个凭据有效时才能登录，能做的也以那个凭据为限，凭据被吊销或 Agent 被删除时随之移除。所有者在 Agent 页面上能看到每个密钥，带备注和添加时间，也能在那里添加或移除（或在 `beacon agent add` 时用 `-ssh-key`）；这样添加的密钥留在 Agent 上，只要 Agent 还有未过期的凭据就能登录。每次添加或移除都会告诉告警群。一个密钥只属于一个 Agent 或令牌，也绝不会是机器的连接密钥，并且只能到它的持有者可访问的机器。

## 旧令牌 {#legacy-tokens}

有账号之前创建的每个令牌都照旧可用，保留自己的权限范围，审计日志仍以它的名字称呼它。控制台的**令牌**页面会把只带成员权限范围、不属于任何 Agent 的令牌标为**旧令牌**；所有者可以把它归为某个成员的 Agent，在这个页面上，或在主机上：

```bash
docker exec beacon beacon token claim claude-code -owner alice
```

令牌保留原有的密钥和到期时间，用着它的程序照常工作；它的机器和 SSH 密钥移到 Agent 上；此后它能做的，是 Agent 的预设在这个成员角色范围内允许的。预设取权限范围与令牌完全一致的那个，都不一致时取自定义（保留令牌的权限范围），所以归入从不放大令牌的权限；`-preset` 另选预设，`-agent` 另起名字，或把令牌加到同一个成员已有的 Agent 上；如果那个 Agent 能做的比令牌多（作用域或机器），这样会被拒绝，这时把令牌归为一个它自己的 Agent。之后用这个令牌在控制台的登录就以 `alice / claude-code` 的身份行事。机器的令牌（检测点、主机、心跳、规则引擎、变更发送方、指标读取方）从来不算旧令牌，也不能归入：它们保持原样。没有人归入任何令牌，也什么都不会坏。

## 审计日志怎么称呼 {#who-the-audit-log-names}

| 谁做的 | 称呼 |
| --- | --- |
| 控制台上的成员 | `alice` |
| Agent，经它的任何凭据或 SSH 密钥 | `alice / claude-code` |
| 旧令牌，或机器的令牌 | 令牌的名字，与以前一样 |
| 主机上的命令行 | `cli:` 加操作系统用户名 |
| Beacon 自己 | `beacon` |

告警群的回显用同样的名字，机器上的会话也是。以前的记录保留原来的名字。

## 停下一个 Agent {#stopping-an-agent}

被吊销或收窄的凭据从下一个请求起就会被拒绝；Agent 经 SSH 通道在机器上打开着的东西（连接、端口转发、工具调用），一旦它不再能访问那台机器，也立即关闭：

- 吊销一个令牌：用它打开的、Agent 用它登记的密钥（以及凭这些密钥登录的会话）；若这是 Agent 最后一个凭据，凭 Agent 其他密钥登录的会话也一起关闭；
- 吊销一次 OAuth 登录，在 Agent 页面上，或由客户端退出登录（`claude mcp logout beacon`）：同上；
- 删除 Agent；
- 把机器从它的列表里去掉，或改用不带 `machines:ssh` 的预设；
- 停用它的所有者，或把所有者降到值班以下；
- 移除会话登录时用的密钥。

Beacon 每分钟再检查一次，处理已过期的凭据。暂停整个通道依然能一下子停下所有 Agent。

## 迁移已有的部署 {#moving-an-existing-deployment}

什么都不会坏：每个令牌都照旧可用。要把成员和 Agent 引进来：

1. 启动这个版本。还没有账号时，它把初始化链接写进日志；`beacon setup` 再打印一个。
2. 打开链接，创建你的所有者账号。
3. 邀请成员（**成员**页面，或 `beacon user invite`），各自给角色。
4. 把 Agent 在用的旧令牌归为对应成员的 Agent（**令牌**页面，或 `beacon token claim`），或者保持原样。成员以前用来登录的令牌，在他们有了账号后最好吊销。
5. 方便时把交互式 Agent 改为 OAuth：`claude mcp remove beacon`，然后不带请求头重新添加并登录。

## 命令行 {#the-command-line}

| 命令 | 做什么 |
| --- | --- |
| `beacon setup` | Beacon 还没有账号时，打印一个新的初始化链接 |
| `beacon user list` | 成员、他们的角色、最近登录和 Agent，以及尚未使用的邀请 |
| `beacon user invite -role ROLE [-note TEXT]` | 打印一个邀请链接 |
| `beacon user role NAME ROLE` | 修改一个成员的角色 |
| `beacon user disable NAME`、`beacon user enable NAME` | 停用或启用一个成员和他的 Agent |
| `beacon user reset NAME` | 打印一个重设密码链接 |
| `beacon agent add NAME -owner USER [-preset P] [-machines a,b] [-expires 365d] [-ssh-key KEY]` | 创建 Agent 和它的令牌（`-ssh-key @FILE` 从文件读取公钥）；令牌在标准输出，命令片段在标准错误 |
| `beacon agent list`、`beacon agent remove NAME` | 列出 Agent；连同凭据删除一个 |
| `beacon token claim NAME -owner USER [-agent NAME] [-preset P]` | 把旧令牌归为某个 Agent 的 |

`beacon token create` 留给机器的令牌和熟手。同样的操作也在 REST API 的 `/api/v1/users` 和 `/api/v1/ssh-keys` 下，以及 MCP 工具 `list_users` 和 `invite_user`（[REST API](reference/api.md#people-and-ssh-keys)、[MCP 工具](reference/mcp.md#people-and-ssh-keys)）。它们需要 `accounts:write`，而没有哪个预设给它：只有所有者在命令行上把这个权限范围给了 Agent，Agent 才能管理成员。重设密码不经 MCP 提供。

## 安全 {#security}

- 登录 Beacon 只有 Beacon 这一处。它只保存哈希，从不保存秘密：密码用 argon2id，令牌、OAuth 访问令牌和刷新令牌、初始化链接、邀请链接和重设链接的代码，以及控制台会话 ID，都用 SHA-256。
- 初始化链接是有意写进日志的，好让第一位所有者不用令牌就能创建：它只有 30 分钟有效，只能用一次，而且只在 Beacon 还没有账号时有效。
- Agent 能做什么，每次请求时都按它的预设和所有者的角色决定；OAuth 客户端能做什么，还要受它那次登录被允许的预设和机器限制。允许客户端从不放宽已有的 Agent，Agent 自己登记的密钥也只在登记时所用的凭据有效时才有效。
- OAuth 访问令牌（`lba_…`）有效一小时，凡是收 bearer 令牌的地方都接受（REST API 和 MCP；SSH 通道凭密钥登录）。刷新令牌（`lbr_…`）的有效期是成员允许的那么久，每用一次就换一个新的；再提交一个已被换掉的，会吊销这整次登录，告警群也会知道。在 `/oauth/revoke` 吊销一次登录的任何一个令牌（客户端退出登录时就会这样做），都会结束这整次登录。授权码只能用一次，60 秒内有效，而且必须带 PKCE（`S256`）：校验码不对就作废，同一个授权码提交两次会吊销它给出的一切。
- 客户端都是公开客户端：没有客户端密钥。客户端注册时的回调地址只能在本机回环上（`http://127.0.0.1`、`http://localhost`、`http://[::1]`，任意端口），或是 `https` 地址，别的都不行；每个地址 10 分钟内最多注册 10 次，从未用过的注册一天后被遗忘。Claude Code 和 Codex 什么都不注册：它们用客户端 ID 元数据文档表明身份。用客户端 ID 元数据文档（一个 `https` 地址）表明身份的客户端，除非 `oauth.client_id_documents` 为 false，Beacon 会去取文档，取时有超时，最多 64 KiB、三次跳转且都必须是 `https`，无论域名解析成什么都绝不连私有地址，取到的保留 10 分钟。
- 对每个授权请求的回答，Beacon 都写明自己（`iss`），与元数据里的颁发者一致；作为申请的 `resource`，只接受它自己的 `/mcp` 或它自己。授权页面的表单只能通往客户端的回调地址；它的表单带着会话的 CSRF 密钥。
- 控制台的 cookie 是 `Secure` 的：要经 HTTPS、在 `public_url` 下提供 Beacon，它也是客户端核对的颁发者。Claude Code 只把凭据发给 `https` 的令牌端点，或 `localhost` 上的。

### OAuth 端点 {#the-oauth-endpoints}

| 路径 | 做什么 |
| --- | --- |
| `GET /.well-known/oauth-protected-resource/mcp`、`GET /.well-known/oauth-protected-resource` | `/mcp` 的受保护资源元数据（RFC 9728），以及整个 Beacon 的。`/mcp` 回答 `401` 时，`WWW-Authenticate` 里指向前一个 |
| `GET /.well-known/oauth-authorization-server` | 授权服务器的元数据（RFC 8414）：各端点、`S256`、作为 scope 的各预设和 `offline_access`、公开客户端、客户端 ID 元数据文档 |
| `POST /oauth/register` | 动态客户端注册（RFC 7591），只接受公开客户端 |
| `GET /oauth/authorize`、`POST /oauth/authorize` | 授权页面，给登录了控制台的成员 |
| `POST /oauth/token` | 用授权码和刷新令牌换取令牌 |
| `POST /oauth/revoke` | 吊销（RFC 7009）：一次登录的任何一个令牌，访问令牌或刷新令牌，都会结束这整次登录，并关闭它在机器上打开着的东西 |
