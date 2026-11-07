---
sourceHash: sha256-97af2043
---

# 邀请你的团队

读完这一页，团队里的每个人都用自己的账号和角色登录 Beacon。每个人的 Agent 以本人的名义行事，永远不会超出本人的角色允许的范围。

一切都以人为名。审计日志、告警群和机器上的会话这样记：

- Alice 在控制台上做的事，记作 `alice`；
- Alice 的 Agent 做的事，记作 `alice / claude-code`。

成员只能用 Beacon 自己的账号登录。不支持 GitHub、飞书、OIDC、LDAP 或任何其他登录方式。

你需要第一个账号，即你在[装在服务器上](../self-hosting/install.md)第 4 步里建的所有者。这一页从那里继续。命令在中心上执行，参考部署里是 `docker exec beacon beacon …`。

## 角色 {#roles}

每个角色能做它上面那个角色能做的一切，再多一些：

| 角色 | 能做什么 | 在上一行之外增加 |
| --- | --- | --- |
| 查看者 | 看到一切：告警、机器、会话及回放、配置、审计日志 | `status:read`、`sessions:read` |
| 值班 | 还能处理告警；其 Agent 经 SSH 通道使用机器 | + `alerts:write`、`machines:ssh` |
| 运维 | 还能增删机器、暂停 Agent、安排维护 | + `maintenance:write`、`incidents:write`、`machines:write` |
| 所有者 | 还能改配置、邀请成员、定角色、管理所有 Agent | + `config:write`、`accounts:write` |

控制台、命令行和 API 说的是角色。scope 在角色之下，[REST API](../reference/api.md#scopes) 里有清单。每个请求都会重读角色，所以改动立刻生效，对本人和对他的 Agent 都是。Beacon 始终保留至少一个启用状态的所有者，会让它一个所有者都不剩的改动将被拒绝。

## 第一个所有者 {#the-first-owner}

1. **找到初始化链接。** 还没有账号的 Beacon 在启动时把一次性初始化链接写进日志：`docker compose logs beacon | grep setup_link`（[装在服务器上](../self-hosting/install.md)第 4 步有示例）。链接过期后，趁 Beacon 还没有账号，在中心上再打印一条：

   ```bash
   docker exec beacon beacon setup
   ```

2. **打开链接。** 它打开**Set Beacon up**。选一个用户名、显示名和密码。这个账号是所有者，且你已登录。

链接只生效一次，Beacon 有了账号之后任何链接都不再有效。从那时起，成员凭邀请加入。第一个账号存在之前，登录页只收令牌，并说明去哪里找初始化链接。

## 邀请成员 {#invite-people}

1. **按每个人需要的角色发出邀请。** 在控制台的**People**页上，用**Invite someone**给出角色和一条说明这是给谁的备注。或者在中心上：

   ```bash
   docker exec beacon beacon user invite -role oncall -note "Bob, on call from Monday"
   ```

   你会看到：

   ```
   An invitation to join as oncall, until 2026-11-03 09:00:00. Send the link to that person only; it works once:
   https://status.example.com/console/join?code=…
   ```

2. **只把链接发给那个人。** 链接只在那时显示一次，只生效一次，有效期 24 小时。对方打开它，选好用户名和密码，就以邀请写明的角色登录了。

**People**页列出尚未使用的邀请，**Withdraw**可以撤回一条。告警群会听到有人受邀、有人加入，但永远不会听到链接本身。

用户名：

- 是 2 到 32 个小写字母、数字、点、连字符和下划线，以字母或数字开头；
- 是成员在审计日志里的名字，所以永不更改；
- 不能是 `beacon`、`root`、`rules` 或 `unknown`，这些是保留名。

显示名只决定控制台怎么称呼这个人。

## 密码 {#passwords}

密码至少 10 个字符，不能是用户名，也不能是最常见的那批密码。没有别的规则。Beacon 只保存它的 argon2id 哈希，比较在常数时间内完成。

- **改自己的密码**：打开控制台侧栏底部菜单里的**Your account**。输入当前密码和新密码。你的其他登录会全部结束。
- **忘记密码**由所有者重置：**People**页上的**Password reset link**，或在中心上执行 `beacon user reset bob`。它打印一条只显示一次、只生效一次、有效期 30 分钟的链接。对方用它设置新密码，其所有登录随之结束。

## 改角色、停用某个成员 {#change-a-role-disable-someone}

在**People**页上，或在中心上：

```bash
docker exec beacon beacon user role bob operator   # bob 现在是运维；其会话和 Agent 随之立刻变化
docker exec beacon beacon user disable bob         # bob 被停用
docker exec beacon beacon user enable bob          # bob 被重新启用
```

调低角色会立刻收紧此人的登录和他的 Agent。停用某人会结束其登录、停下其 Agent、关闭这些 Agent 在机器上打开着的一切。控制台上没有人能停用自己。

`beacon user list` 列出所有人，带角色、状态、最近登录和 Agent。后面是尚未使用的邀请。

## Agent {#agents}

Agent 是替某个人（它的所有者）行事的程序。可以是某人工作站上的 Claude Code、钩子唤醒后由 `beacon-oncall` 启动的 Agent，或一个 CI 任务。一个 Agent 有：

- 一个**名字**：2 到 40 个小写字母、数字、点、连字符和下划线，全 Beacon 唯一。审计日志记作 `所有者 / 名字`。
- 一个**预设**（见下）；
- 它能经 SSH 通道使用的**机器**：全部，或一组清单；
- 它的 **SSH 密钥**；
- 它的**凭据**：无界面 Agent 和 CI 用令牌（`lbt_…`），交互式客户端用 OAuth 登录。

| 预设 | 请求的权限 |
| --- | --- |
| 只读 | 查看者的 scope |
| 值班 | 值班成员的 scope：还能处理告警、使用机器 |
| 运维 | 运维成员的 scope |

一个 Agent 能做的，是**它的预设所请求**、且**其所有者角色所允许**的交集，每个请求都重新计算。降级或停用所有者，立刻收紧或停下其 Agent，用他们手里已有的令牌也一样。

- 没有任何预设给 `config:write` 或 `accounts:write`。
- 预设超出所有者角色的，在创建或修改 Agent 时会被拒绝。
- **custom** 预设的 Agent 自行列出 scope，仍以所有者角色为上限（`beacon agent add … -scopes status:read,config:write`）。

控制台的**Agents**页列出你的 Agent；所有者能看到所有人的。每个 Agent 的页面：

- 显示它现在能做什么；
- 修改其预设和机器；
- 列出其 OAuth 登录、令牌和密钥，可吊销或删除其中任何一项；
- 可以连同全部凭据一起删除这个 Agent。

成员创建和管理自己的 Agent。所有者可以管理任何人的。

## 接入 Claude Code {#connect-claude-code}

Claude Code 经浏览器、用 MCP 的 OAuth 登录。它既能用 Beacon 的工具，也能用它自己的 ssh、scp 和 rsync 使用机器：见[接入 Claude Code](../integrations/claude-code.md)。Codex 的登录方式相同：见[接入 Codex](../integrations/codex.md)。

## 一次登录能做什么 {#what-a-sign-in-may-do}

每次 OAuth 登录把你允许的东西记为它自己的上限。客户端能做的是这次登录的预设和机器允许的，再受 Agent 本身、再受你的角色约束。每次请求都重新读取。

允许一个客户端不会改变任何已有的 Agent。所以它不会放宽另一个客户端的登录、Agent 的令牌或密钥。要让一个复用的 Agent 权限更大，先在它的页面上改好 Agent，再从客户端重新登录。

Agent 页面的**OAuth sign-ins**下列出每次登录：客户端、被允许的内容、最近使用时间。**Revoke**可以结束它。

## 无界面 Agent 与 CI {#headless-agents-and-ci}

独立运行的程序拿一个令牌，在控制台的**Agents**页或用 `beacon agent add` 创建。令牌到期前它自己续期。`beacon-oncall` 是钩子唤醒 Agent 的参考执行器。见[无界面 Agent 与 CI](../integrations/headless.md)。

## SSH 密钥 {#ssh-keys}

SSH 通道按公钥让 Agent 登录。Agent 用手里已有的凭据、经 MCP 或 API 自己登记公钥，谁都不用把密钥复制到任何地方。这些工具都要 `machines:ssh`：

- `add_ssh_key`（`POST /api/v1/ssh-keys`）；
- `list_ssh_keys`；
- `remove_ssh_key`。

对 Claude Code 说*「把我的 SSH 公钥 ~/.ssh/id_ed25519.pub 登记到 Beacon」*，它就会调用 `add_ssh_key`。`list_machines` 随后给出供它自己的 ssh 用的 `ssh_config` 段（[接入 Claude Code](../integrations/claude-code.md#connect-with-your-own-ssh-scp-and-rsync)）。

密钥属于谁：

- **Agent 自己登记的密钥**属于它登记时所用的凭据（`via`：`token:NAME` 或 `grant:ID`）。只在那份凭据有效期间可用。凭据被吊销或 Agent 被删除时一并消失。
- **所有者添加的密钥**留在 Agent 上。所有者在 Agent 页面上用**Add an SSH key**添加，或用 `beacon agent add` 的 `-ssh-key`。

一个 Agent 至多持有 20 把密钥，一把密钥只能到达其持有者能到达的机器。密钥的每次添加和删除，告警群都会听到。

## 停下一个 Agent {#stop-an-agent}

被吊销或收窄的凭据从下一个请求起被拒绝。Agent 在机器上打开着的一切立刻关闭。能触发这件事的每一个开关，以及一次停下所有 Agent 的那一个，见[停下 Agent，暂停一切](stop.md)：

- 吊销令牌或登录；
- 删除 Agent；
- 收窄其机器或预设；
- 停用或降级其所有者。

## 审计日志里写谁 {#who-the-audit-log-names}

审计日志、告警群的回显和机器上的会话，这样记下每个动手的人或程序：

| 谁做的 | 记作 |
| --- | --- |
| 成员 | `alice` |
| 其 Agent，无论它用哪份凭据 | `alice / claude-code` |
| 遗留令牌或机器令牌 | 令牌名 |
| 中心上的命令行 | `cli:` 加操作系统用户 |
| Beacon 自己 | `beacon` |

之前的记录保持原来的名字。完整的对照表在权限参考里（[角色、预设与权限](../reference/permissions.md)）。

## 从只有令牌的时代迁移 {#moving-from-tokens}

在账号功能出现之前就在运行的 Beacon 上，每个令牌照旧工作，带着自己的 scope。审计日志按名字称呼它。什么都不做也不会坏。要引入成员和 Agent：

1. **启动这个版本。** 还没有账号时，它把初始化链接写进日志，并写明找到多少个遗留令牌（`legacy_tokens`）。打开链接建好所有者账号。链接过期了，`beacon setup` 再打印一条，但只在 Beacon 还没有账号时可以。
2. **邀请成员**（**People**，或 `beacon user invite`），按各自的角色。
3. **把 Agent 用的令牌认领**为其所有者的 Agent（[见下](#claiming-a-token)），或保持原样。
4. **吊销成员用来登录控制台的那些令牌**，等他们都有了账号之后。
5. **方便时把交互式 Agent 改成 OAuth**：`claude mcp remove beacon`，再不带请求头地添加一次并登录。

### 认领令牌 {#claiming-a-token}

只带人的 scope、又不属于任何 Agent 的令牌，控制台的**Tokens**页会标成**Legacy**。所有者把它认领为某人的 Agent：在该页点**Claim**，或在中心上：

```bash
docker exec beacon beacon token claim claude-code -owner alice
```

```
token claude-code is agent alice / claude-code's now (oncall, may do status:read,sessions:read,alerts:write,machines:ssh); it keeps its secret and expiry
```

令牌的密钥和有效期不变，用它的一切照常工作。它的机器和 SSH 密钥移交给 Agent。

预设取 scope 恰与令牌一致的那个，没有完全一致的就用 custom。所以认领永远不会放宽令牌能做的事。

- `-preset` 可以另选预设。
- `-agent` 把令牌加到此人已有的某个 Agent 上。该 Agent 当前能做的超出令牌的，会被拒绝。

机器的令牌永远不会被标为 Legacy，也永远不被认领：检测点、主机、心跳、规则引擎、变更来源、读指标的令牌都是。

## 命令行 {#the-command-line}

管理成员和 Agent 的命令：

- `beacon user list`
- `beacon user invite -role ROLE [-note TEXT]`
- `beacon user role NAME ROLE`
- `beacon user disable` 和 `beacon user enable NAME`
- `beacon user reset NAME`
- `beacon agent add NAME -owner USER [-preset P] [-machines a,b] [-expires 365d] [-ssh-key KEY]`
- `beacon agent list`
- `beacon agent remove NAME`
- `beacon token claim NAME -owner USER`

`ROLE` 是 `viewer`、`oncall`、`operator` 或 `owner`。预设是 `readonly`、`oncall` 或 `operator`。每个子命令和参数见命令行参考：[命令行](../reference/cli.md)。

同样的操作在 REST API 的 `/api/v1/users` 下（[REST API](../reference/api.md#people-and-ssh-keys)），需要 `accounts:write`，没有任何预设给出它。改角色、停用成员和重置密码不经 MCP 提供。

## 安全 {#security}

账号、令牌和 OAuth 登录是如何保管的，见[安全模型](../self-hosting/security.md)。那里讲 argon2id 与 SHA-256、一次性链接，以及 Agent 能做什么如何在每个请求上重新决定。

### OAuth 端点 {#the-oauth-endpoints}

MCP 客户端用到的 OAuth 路由列在[REST API](../reference/api.md#other-routes-on-port-17001)里：元数据文档、注册、授权页、令牌与吊销端点。

## 检查一下 {#check-it}

1. **让你邀请的人登录。** `beacon user list` 里列出这个人、其角色和最近登录。此人做的每件事在审计日志里都记作 `alice`。
2. **给他建一个 Agent，让 Agent 调 `GET /api/v1/token`。** 应答里写着 `alice / name`、它的预设和它现在能做什么。
3. **把这个人的角色降为查看者，再问一次。** Agent 用手里已有的令牌，能做的事也立刻变少。

## 下一步 {#next}

- [让 Agent 值班](on-call.md)：服务出事时这些 Agent 做什么。
- [复查 Agent 做了什么](console.md)：审计日志、会话和回放。
- [停下 Agent，暂停一切](stop.md)：各个刹车，以及每一个会关掉什么。
