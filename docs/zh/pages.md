---
sourceHash: sha256-06f13ac1
---

# 控制台

控制台是 Beacon 自己的工作台：人看 Agent 和告警在做什么，都在这一个地方。它由 `beacon` 自己提供，在 17001 端口的 `/console` 上，与 API 和 MCP 在一起，地址在 `public_url` 下；站点根路径 `/` 是公开的，放默认的[状态页](status.md)，没有状态页时是一个写明这套部署是什么的页面。从群里的卡片点一个链接，不用登录就能打开这条告警的页面（检测看到了什么、时间线、备注、唤醒了哪些 Agent、告警相关机器上的每一次会话，带命令和回放）；登录以后，同一个页面就在其他页面之间，暂停开关点一下就到。

人用自己的 Beacon 账号登录（[成员、Agent 与登录](accounts.md)），控制台能做什么，取决于他的角色。

## 登录 {#signing-in}

`/console/sign-in` 要用户名和密码；未登录的人打开控制台的任何页面都会被带到这里，登录后再回到原来的页面。`/` 上的公开页面在页头右侧有**登录**（已登录时是**控制台**），通往 `/console`；放在自己域名上的状态页两者都没有，因为控制台在那个域名上不应答。第一个账号是所有者，用一次性的初始化链接创建，Beacon 在还没有账号时把它写进日志；其他人凭邀请加入（[成员、Agent 与登录](accounts.md#the-first-owner)）：

```bash
docker exec beacon beacon setup     # while Beacon has no account: a new link to make the first, an owner
```

- 登录就是 Beacon 数据库里的一个会话，有效 12 小时。浏览器只拿着会话的 ID，放在 `HttpOnly`、`Secure`、`SameSite=Strict` 的 cookie 里（`__Host-beacon-console`，路径是 `/`，`__Host-` cookie 必须如此）；数据库只保存它的 SHA-256。
- 控制台能做什么取决于这个人的角色，每次请求时重新读取：角色降低立即生效，停用账号会结束它的会话。设置新密码会结束这个人的其他会话；重设密码会结束全部会话。
- 登录失败与 API 共用同一个限额：同一客户端地址 10 分钟内失败 20 次，就会在这 10 分钟剩下的时间里被拒绝。同一用户名 10 分钟内密码错 10 次，这个用户名也会被拒绝，不论来自哪里。
- 登录、初始化、邀请和重设表单都带着自己的密钥，其他网站无法替人登录它选定的账号。登录记入审计日志（`console_sign_in`）。
- **你的账号**（修改密码）和**退出登录**在侧栏底部的菜单里。
- 表单下方的**改用令牌登录**仍可粘贴一个至少有 `status:read` 的 Beacon 令牌，再保留一个版本：给脚本用，也给还没有账号的 Beacon 用。这样的会话与令牌绑定：吊销令牌，或者用同名的另一个令牌替换它，会话就结束；它能做的就是令牌能做的。
- cookie 是 `Secure` 的，所以控制台需要 HTTPS（或 `localhost`）：放在提供 `public_url` 的 TLS 代理后面，参考部署就是这样做的。

## 页面 {#the-pages}

所有页面共用设计系统的工作台：窄侧栏列出各栏目和数量，顶栏就是页头（导航路径、标题、数量），列表的筛选在左边，页脚是 Beacon 的版本，以及文档、GitHub 上的代码和反馈问题的链接。列表按时间倒序，每页 50 条；每个筛选都是链接，所以每个视图都有自己的 URL。

| 页面 | 显示什么 | 需要 |
| --- | --- | --- |
| `/console` 概览 | 按严重级别统计的触发中告警；应上报的检测点和最近 5 分钟内经网关上报的检测点；机器（多少台关键、NAT 后的有多少已连接）；SSH 通道（运行中，或自何时起暂停）；今天唤醒了哪些 Agent、结果如何；今天的会话；配置版本和修改人；Beacon 版本。每个数字都链接到对应的列表 | `status:read` |
| `/console/alerts` | Beacon 保存的所有告警组（已恢复的保留 7 天），可按状态、严重级别、服务、是否认领、是否静默筛选，可搜索标题、服务和标签 | `status:read` |
| `/console/alerts/{id}` | 告警页面（见下文），附表单：带备注认领、静默 1、4 或 24 小时、添加备注 | `status:read`；表单要 `alerts:write` |
| `/console/machines` | SSH 通道能访问的机器：地址，或 NAT 后的机器是否已连接；是否关键；24 小时内的会话；最近一次会话。上方是整个通道的暂停开关 | `status:read` 或 `machines:ssh`；开关要 `machines:write` |
| `/console/machines/{name}` | 一台机器：地址、账户、主机密钥和备注，最近的会话，以及在上面执行过的命令 | `status:read` 或 `machines:ssh` |
| `/console/sessions` | 所有会话，可按 Agent、机器、类型、途径、退出码和日期筛选，可搜索命令 | `status:read` 或 `machines:ssh` |
| `/console/sessions/{id}` | 会话页面（见下文），带回放 | `sessions:read` 或 `machines:ssh` |
| `/console/tokens` | 所有令牌：名称、权限范围、能访问的机器、SSH 密钥指纹、何时由谁创建、何时到期、最近一次自行续期，以及属于谁的 Agent；有账号之前的令牌标为**旧令牌**，附把它归为某个成员的 Agent 的表单。从不显示密钥本身 | `status:read`；吊销要 `machines:write`；归入要 `accounts:write` |
| `/console/config` | Beacon 保存的配置，只读：检测点（是否应上报、是否在上报）、心跳、资源、检测目标组和检测模块；然后是版本历史：谁、何时、经什么途径、为什么 | `status:read` 或 `config:write` |
| `/console/config/history/{version}` | 一个版本逐条改了什么，以差异显示，附撤销表单 | `status:read` 或 `config:write`；撤销要 `config:write` |
| `/console/agents` | 你的 Agent（所有者看到所有人的）：预设、当前能做什么、机器、凭据、密钥；用 OAuth 接入 Claude Code 或 Codex 的命令，以及给无界面 Agent 发令牌的表单，令牌只显示一次，连同使用它的命令 | 已登录：成员为自己创建 Agent，所有者为任何人 |
| `/console/agents/{name}` | 一个 Agent：当前能做什么、它的预设和机器（在这里修改）、它的 OAuth 登录（各自带着当初允许的内容）、令牌和 SSH 密钥，都可在这里吊销或移除，发新令牌，删除 Agent | 它的所有者，或 `accounts:write` |
| `/console/hooks` | 配置的 Agent Hook（只显示地址的主机名，不显示路径和查询参数）和发出的每次调用：Hook、事件、状态、HTTP 状态码、尝试次数、时间、对应的告警 | `status:read` |
| `/console/people` | 成员：角色、最近登录、Agent、是否启用；邀请成员（链接只显示一次）、尚未使用的邀请、修改角色、停用和启用、重设密码链接 | `accounts:write`（所有者） |
| `/console/account` | 你的账号，以及修改密码 | 已登录 |
| `/console/setup`、`/console/join`、`/console/reset`、`/oauth/authorize` | 一次性链接打开的页面（第一位所有者、邀请、新密码），以及 MCP 客户端把人送去的授权页面（[成员、Agent 与登录](accounts.md)） | 链接里的代码；授权页面要用账号登录 |
| `/console/audit` | 审计日志，可按操作者、操作和途径筛选，可搜索 | `status:read` |
| `/console/changes` | 来自 CI、Flux 等的变更事件，可按来源、类型、服务和机器筛选 | `status:read` |

## 控制台能做什么 {#what-the-console-may-do}

控制台的表单与 API 和命令行的操作相同，以登录的成员身份（`alice`；用令牌登录的，以那个令牌的身份）、途径 `console` 执行：与其他所有写操作一样记入审计日志，并回显到告警群。

| 表单 | 权限范围 | 等同于 |
| --- | --- | --- |
| 认领告警，可带备注 | `alerts:write` | `POST /api/v1/alerts/{id}/ack` |
| 静默告警 1、4 或 24 小时，写明原因 | `alerts:write` | `POST /api/v1/silences` |
| 给告警添加备注 | `alerts:write` | `POST /api/v1/alerts/{id}/notes` |
| 暂停或恢复 Agent 对机器的访问 | `machines:write` | `POST /api/v1/ssh/pause`、`/resume` |
| 吊销能访问机器的令牌，或任何 Agent 的令牌 | `machines:write` | `beacon token revoke` |
| 撤销一次配置变更 | `config:write` | `POST /api/v1/config/history/{version}/revert` |
| 邀请成员、撤回邀请 | `accounts:write` | `POST /api/v1/users/invitations`、`beacon user invite` |
| 修改角色、停用或启用成员 | `accounts:write` | `PATCH /api/v1/users/{name}`、`beacon user role`、`disable`、`enable` |
| 生成重设密码链接 | `accounts:write` | `POST /api/v1/users/{name}/reset`、`beacon user reset` |
| 把旧令牌归为某个成员的 Agent | `accounts:write` | `beacon token claim` |
| 创建带令牌的 Agent，修改它的预设和机器，给它新令牌，删除它 | 它的所有者，或 `accounts:write` | `beacon agent add`、`beacon agent remove` |
| 添加或移除 Agent 的 SSH 密钥 | 它的所有者，或 `accounts:write` | `POST /api/v1/ssh-keys`、`DELETE /api/v1/ssh-keys`，由 Agent 自己调用 |
| 吊销 Agent 的令牌或 OAuth 登录 | 它的所有者，或 `accounts:write` | `beacon token revoke`、`POST /oauth/revoke` |
| 修改自己的密码，需要当前密码 | 用账号登录 | |

每个表单都带着会话的 CSRF 密钥；浏览器表明来自其他网站的请求会被拒绝。这个人的角色（或令牌的权限范围）不允许的表单不会显示，硬发过来也会被拒绝；Agent 的预设不能超出它的所有者的角色。

**在控制台吊销令牌。**Agent 的所有者在 Agent 页面上吊销它的令牌和 OAuth 登录，也在那里删除这个 Agent。在**令牌**页面上，有 `machines:write` 就能吊销任何能经 SSH 通道访问机器的令牌（`machines:ssh`）和任何 Agent 的令牌：这相当于针对单个 Agent 的暂停开关，停下它在机器上做的事，而不影响其他 Agent，所以它用暂停开关的权限范围。Agent 凭被吊销的凭据在机器上打开着的东西（连接、转发的端口、工具调用）会立即关闭，这个令牌在控制台的登录也随之结束。机器和服务的令牌（检测点、主机、心跳、规则引擎、变更发送方、指标读取方）不能在页面上吊销：它们在主机上经管理套接字创建和吊销，因为它们维持着监控，而告警一条都不能丢。

## 告警页面：`/console/alerts/{id}` {#the-alerts-page-consolealertsid}

卡片上的**时间线**按钮打开它。页面显示：

- 告警本身：标题、状态、级别、从何时起（或何时恢复、持续多久）、描述、服务、标签、处理手册和面板；
- 人收到了什么、做了什么：认领、静默、加急、电话通知；
- **检测**：这条告警的服务在每个检测点的最新结果和原因（也就是 `get_evidence` 返回的）：正常、失败自何时起、无数据、失败原因、耗时、HTTP 状态、尝试次数、证书到期时间；
- **相关告警**：同一故障域或同一服务下其他正在触发的告警，各自链接到自己的页面；
- **Agent**：Agent 现在能否访问机器，暂停的话从何时起；唤醒了哪些钩子、何时；一行「N 次会话，M 台机器，K 条命令」；
- **会话**：告警时间范围内，在它的标签所指的机器上，或由认领、留备注的 Agent 发起的每一次会话：谁、经什么途径、在哪台机器、做了什么、退出码；交互 shell 则列出每条命令的退出码和耗时；每次会话都有**回放**链接；
- **备注**、**变更**（告警前后的变更事件）、**标签**；
- **时间线**：以上一切按时间排列，与 `get_timeline` 返回的一致。

时间范围从告警开始前 30 分钟起，到现在，或到恢复后 30 分钟，与证据一致。

## 会话页面：`/console/sessions/{id}` {#the-sessions-page-consolesessionsid}

谁、在哪台机器、经什么途径（ssh、MCP 或 API）、什么类型的会话、何时开始、持续多久、退出码，以及用 [asciinema-player](https://github.com/asciinema/asciinema-player) 在页面里回放的录像（asciicast v3，正是 SSH 通道录下的格式：输出、输入、窗口大小、每条命令处的标记、退出码）。播放器下面是交互 shell 的各条命令及退出码，或者一条命令的输出。录像也可以下载。仍在进行的会话只显示记录，结束后才能回放。

## 带签名的链接 {#signed-links}

页面链接以 `?s=<到期时间>.<签名>` 结尾：到期时间是 Unix 秒数，签名是对方法、路径和到期时间（各自后跟一个换行）做的 HMAC-SHA256，base64url 编码，密钥是 32 字节，Beacon 第一次启动时生成并保存在数据库里。链接的有效期是 `pages.link_ttl`（默认 30 天）：

```yaml
public_url: https://beacon.example.com   # 控制台的地址；没有它就没有链接
pages:
  link_ttl: 720h
```

一个链接只授权一个页面，而且只读：告警页面，或会话页面及其录像，到期为止。页面内部的链接（到会话、到录像）与打开它的那个链接同时到期。路径不同、方法不同、改过到期时间的链接都会失败，过期的也一样；所有失败都是普通的 404，与不存在的页面一样，所以错的链接什么都泄露不了。**拿到链接的任何人都能看到那个页面，包括录像，无需登录**：链接发往的群必须是私密的，链接也不应转发到录像不该去的地方。目前还没有在到期前撤销链接的命令（那意味着更换密钥）；`link_ttl` 在群的使用习惯允许的范围内尽量短。

用链接打开的页面只显示这一页，侧栏里有**登录**，登录后能看其余页面。已登录时，同一个链接会在控制台里打开这个页面，并显示令牌权限范围允许的表单。

**0.3.0 之前发出的链接。**在那之前控制台在站点根路径上，群里已有的链接写的是 `/alerts/{id}` 和 `/sessions/{id}`（以及 `/sessions/{id}/recording`），而且路径就在签名里。这些路径仍然应答：签名对旧路径校验通过的链接，会被转到（`308`）页面在 `/console` 下的路径，并换上对新路径的签名，到期时间与原来的相同。伪造的或过期的链接转过去时不带签名，于是和以前一样得到 404；已登录的话，照旧能打开页面。控制台的其他旧路径（`/sign-in`、`/alerts`、`/machines`、`/sessions`、`/tokens`、`/config`、`/agents`、`/audit`、`/changes` 及其下的页面）都带着查询参数转到 `/console` 下的对应位置（原先显示 Hook 的 `/agents` 转到 `/console/hooks`），所以书签还能用。表单只提交到 `/console`。

Beacon 在人可能拿到链接的每个地方都给出链接：

- 卡片：**时间线**按钮和 Agent 区块；
- 群里回显 Agent 在机器上做了什么的消息：每次会话一个**回放**链接；
- API 和 MCP 的结果，让 Agent 能把链接交给人：告警上的 `page`（`GET /api/v1/alerts/{id}`、`get_alert`）、证据里的 `links.page`、时间线里会话事件的 `url`、会话上的 `page`（`list_sessions`、`get_session`、证据里的 `done.sessions[]`）。

## 用令牌代替 {#tokens-instead}

任何带着所需权限范围的凭据，打开告警页面和会话页面的规则都与对应的 API 路由一致，而且只读：令牌（`lbt_…`，Agent 的或独立的）或 OAuth 访问令牌（`lba_…`），告警页面要 `status:read`，会话页面和录像要 `sessions:read` 或 `machines:ssh`；Agent 的权限是它的预设在所有者角色范围内允许的。以 `Authorization: Bearer …` 发送；这样打开的页面，里面到会话和录像的链接有效一小时。缺少权限的凭据、未知的凭据、没有凭据，得到的都是同样的 404。

## 怎么做的 {#how-it-is-built}

- 基于 OpenLinkOS 设计系统 [`@openlinkos/ui`](https://github.com/openlinkos/design/tree/master/packages/ui) 0.6.1 的工作台外框和布局，与 Origin 的管理工作台一样，让这一系列产品看起来是一家。它的样式表、图标和 Geist 字体放在 `internal/web/static/ol` 下，许可证和哈希在 `NOTICE` 里；`scripts/update-ui.sh` 从 npm 更新它们。Beacon 的主题色（`data-brand="beacon"`，一种温暖的烽火橙）是暂定的，写在 `static/beacon-brand.css` 里，等设计系统发布这个品牌后删掉。
- 所有屏幕用同一个页面：1200px 以上是完整侧栏，860px 起是图标栏，再窄就是抽屉；手机宽度下列表重排成卡片，全靠 CSS。服务器从不过问屏幕有多宽。
- 不从 Beacon 之外加载任何东西：样式表、字体、播放器的 JavaScript 和 CSS 都嵌在二进制里，图标是页面内的 sprite，唯一的脚本是播放器的，没有内联脚本或样式。`Content-Security-Policy` 只允许来自 Beacon 自己的脚本、样式、字体、请求和表单（`'wasm-unsafe-eval'` 是因为播放器的终端是 WebAssembly）。页面标为 `noindex`（`/robots.txt` 也让搜索引擎不进 `/console`），不发送 referrer，因为链接带着签名。
- Beacon 是开源软件，控制台也这样写明：每个页面的页脚都有 Beacon 的版本，以及文档（[beacon.openlinkos.com](https://beacon.openlinkos.com/zh/)，按控制台的语言）、[GitHub](https://github.com/openlinkos/beacon) 上的代码和[反馈问题](https://github.com/openlinkos/beacon/issues/new/choose)的链接；登录页在表单下方也写着。只有这几个链接会自己通往 Beacon 之外。`branding.powered_by`（[状态页](status.md#branding)）不影响它们。
- 来自 Beacon 之外的文字（告警注释、备注、命令、变更事件）经桥接层清理，输出时再转义。
- 控制台用配置的 `language` 显示，中文或英文，跟随系统的浅色或深色模式。

## 控制台不做什么 {#what-the-console-does-not-do}

- 不经别处登录：Beacon 自己的账号是唯一的入口，不经 GitHub、飞书、OIDC、LDAP 或其他任何地方。
- 没有审批流程：这些表单是令牌持有者本来就能经 API 做的事；控制台让它们看得见、做得快，而不是设卡。人看，Agent 做，暂停开关让它们停下。
- 不编辑配置，只能撤销变更：修改经 API、MCP 和命令行进行，每次都像检查文件那样检查。它会创建 Agent 的令牌，但不创建机器的令牌，那是主机上管理套接字的事。
