---
sourceHash: sha256-6f17b763
---

# 控制台

控制台是 Beacon 自己的工作台：人看 Agent 和告警在做什么，都在这一个地方。它由 `linkos-beacon` 自己提供，在 17001 端口的 `/` 上，与 API 和 MCP 在一起，地址在 `public_url` 下。从群里的卡片点一个链接，不用登录就能打开这条告警的页面（检测看到了什么、时间线、备注、唤醒了哪些 Agent、告警相关机器上的每一次会话，带命令和回放）；登录以后，同一个页面就在其他页面之间，暂停开关点一下就到。

Beacon 没有自己的登录系统：人用一个 Beacon 令牌登录，控制台能做什么，取决于这个令牌的权限范围。

## 登录 {#signing-in}

`/sign-in` 要一个 Beacon 令牌，像密码一样粘贴进去。至少有 `status:read` 的令牌才能登录；没有它的、不存在的、已过期的都不行。在主机上为自己创建一个：

```bash
linkos-beacon token create you -scopes status:read,sessions:read,alerts:write -expires 30d
```

- 登录就是 Beacon 数据库里的一个会话，有效 12 小时，令牌更早到期的话随令牌到期。浏览器只拿着会话的 ID，放在 `HttpOnly`、`Secure`、`SameSite=Strict` 的 cookie 里（`__Host-beacon-console`）；数据库只保存它的 SHA-256，令牌本身不保存。
- 会话与这个令牌绑定：吊销令牌，或者用同名的另一个令牌替换它（吊销后重新创建），会话立即结束。权限范围在每次请求时重新读取。
- 登录失败与 API 共用同一个限额：同一客户端地址 10 分钟内失败 20 次，就会在这 10 分钟剩下的时间里被拒绝。
- 登录表单带着自己的密钥，其他网站无法用它选的令牌替人登录。登录记入审计日志（`console_sign_in`）。
- **退出登录**在侧栏底部的菜单里。
- cookie 是 `Secure` 的，所以控制台需要 HTTPS（或 `localhost`）：放在提供 `public_url` 的 TLS 代理后面，参考部署就是这样做的。

## 页面 {#the-pages}

所有页面共用设计系统的工作台：窄侧栏列出各栏目和数量，顶栏就是页头（导航路径、标题、数量），列表的筛选在左边。列表按时间倒序，每页 50 条；每个筛选都是链接，所以每个视图都有自己的 URL。

| 页面 | 显示什么 | 需要 |
| --- | --- | --- |
| `/` 概览 | 按严重级别统计的触发中告警；应上报的检测点和最近 5 分钟内经网关上报的检测点；机器（多少台关键、NAT 后的有多少已连接）；SSH 通道（运行中，或自何时起暂停）；今天唤醒了哪些 Agent、结果如何；今天的会话；配置版本和修改人；Beacon 版本。每个数字都链接到对应的列表 | `status:read` |
| `/alerts` | Beacon 保存的所有告警组（已恢复的保留 7 天），可按状态、严重级别、服务、是否认领、是否静默筛选，可搜索标题、服务和标签 | `status:read` |
| `/alerts/{id}` | 告警页面（见下文），附表单：带备注认领、静默 1、4 或 24 小时、添加备注 | `status:read`；表单要 `alerts:write` |
| `/machines` | SSH 通道能访问的机器：地址，或 NAT 后的机器是否已连接；是否关键；24 小时内的会话；最近一次会话。上方是整个通道的暂停开关 | `status:read` 或 `machines:ssh`；开关要 `machines:write` |
| `/machines/{name}` | 一台机器：地址、账户、主机密钥和备注，最近的会话，以及在上面执行过的命令 | `status:read` 或 `machines:ssh` |
| `/sessions` | 所有会话，可按 Agent、机器、类型、途径、退出码和日期筛选，可搜索命令 | `status:read` 或 `machines:ssh` |
| `/sessions/{id}` | 会话页面（见下文），带回放 | `sessions:read` 或 `machines:ssh` |
| `/tokens` | 所有令牌：名称、权限范围、能访问的机器、SSH 密钥指纹、何时由谁创建、何时到期、最近一次自行续期。从不显示密钥本身 | `status:read`；吊销要 `machines:write` |
| `/config` | Beacon 保存的配置，只读：检测点（是否应上报、是否在上报）、心跳、资源、检测目标组和检测模块；然后是版本历史：谁、何时、经什么途径、为什么 | `status:read` 或 `config:write` |
| `/config/history/{version}` | 一个版本逐条改了什么，以差异显示，附撤销表单 | `status:read` 或 `config:write`；撤销要 `config:write` |
| `/agents` | 配置的 Agent Hook（只显示地址的主机名，不显示路径和查询参数）和发出的每次调用：Hook、事件、状态、HTTP 状态码、尝试次数、时间、对应的告警 | `status:read` |
| `/audit` | 审计日志，可按操作者、操作和途径筛选，可搜索 | `status:read` |
| `/changes` | 来自 CI、Flux 等的变更事件，可按来源、类型、服务和机器筛选 | `status:read` |

## 控制台能做什么 {#what-the-console-may-do}

控制台的表单与 API 的操作相同，权限范围也相同，以登录的令牌身份、途径 `console` 执行：与其他所有写操作一样记入审计日志，并回显到告警群。

| 表单 | 权限范围 | 等同于 |
| --- | --- | --- |
| 认领告警，可带备注 | `alerts:write` | `POST /api/v1/alerts/{id}/ack` |
| 静默告警 1、4 或 24 小时，写明原因 | `alerts:write` | `POST /api/v1/silences` |
| 给告警添加备注 | `alerts:write` | `POST /api/v1/alerts/{id}/notes` |
| 暂停或恢复 Agent 对机器的访问 | `machines:write` | `POST /api/v1/ssh/pause`、`/resume` |
| 吊销能访问机器的令牌 | `machines:write` | `linkos-beacon token revoke` |
| 撤销一次配置变更 | `config:write` | `POST /api/v1/config/history/{version}/revert` |

每个表单都带着会话的 CSRF 密钥；浏览器表明来自其他网站的请求会被拒绝。令牌权限范围不允许的表单不会显示，硬发过来也会被拒绝。

**在控制台吊销令牌。**令牌在主机上经管理套接字创建，大多也在那里吊销。控制台只吊销能经 SSH 通道访问机器的令牌（`machines:ssh`）：这相当于针对单个 Agent 的暂停开关，立即停下它在机器上做的事，而不影响其他 Agent，所以它用暂停开关的权限范围 `machines:write`。吊销同时结束这个令牌在控制台的登录。机器和服务的令牌（检测点、主机、心跳、规则引擎、变更发送方、指标读取方）不能在页面上吊销：它们维持着监控，而告警一条都不能丢。

## 告警页面：`/alerts/{id}` {#the-alerts-page-alertsid}

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

## 会话页面：`/sessions/{id}` {#the-sessions-page-sessionsid}

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

Beacon 在人可能拿到链接的每个地方都给出链接：

- 卡片：**时间线**按钮和 Agent 区块；
- 群里回显 Agent 在机器上做了什么的消息：每次会话一个**回放**链接；
- API 和 MCP 的结果，让 Agent 能把链接交给人：告警上的 `page`（`GET /api/v1/alerts/{id}`、`get_alert`）、证据里的 `links.page`、时间线里会话事件的 `url`、会话上的 `page`（`list_sessions`、`get_session`、证据里的 `done.sessions[]`）。

## 用令牌代替 {#tokens-instead}

Bearer 令牌打开告警页面和会话页面的规则与对应的 API 路由一致，而且只读：告警页面要 `status:read`，会话页面和录像要 `sessions:read` 或 `machines:ssh`。以 `Authorization: Bearer lbt_…` 发送；这样打开的页面，里面到会话和录像的链接有效一小时。缺少权限的令牌、未知的令牌、没有令牌，得到的都是同样的 404。

## 怎么做的 {#how-it-is-built}

- 基于 OpenLinkOS 设计系统 [`@openlinkos/ui`](https://github.com/openlinkos/design/tree/master/packages/ui) 0.6.1 的工作台外框和布局，与 Origin 的管理工作台一样，让这一系列产品看起来是一家。它的样式表、图标和 Geist 字体放在 `internal/web/static/ol` 下，许可证和哈希在 `NOTICE` 里；`scripts/update-ui.sh` 从 npm 更新它们。Beacon 的主题色（`data-brand="beacon"`，一种温暖的烽火橙）是暂定的，写在 `static/beacon-brand.css` 里，等设计系统发布这个品牌后删掉。
- 所有屏幕用同一个页面：1200px 以上是完整侧栏，860px 起是图标栏，再窄就是抽屉；手机宽度下列表重排成卡片，全靠 CSS。服务器从不过问屏幕有多宽。
- 不从 Beacon 之外加载任何东西：样式表、字体、播放器的 JavaScript 和 CSS 都嵌在二进制里，图标是页面内的 sprite，唯一的脚本是播放器的，没有内联脚本或样式。`Content-Security-Policy` 只允许来自 Beacon 自己的脚本、样式、字体、请求和表单（`'wasm-unsafe-eval'` 是因为播放器的终端是 WebAssembly）。页面标为 `noindex`，不发送 referrer，因为链接带着签名。
- 来自 Beacon 之外的文字（告警注释、备注、命令、变更事件）经桥接层清理，输出时再转义。
- 控制台用配置的 `language` 显示，中文或英文，跟随系统的浅色或深色模式。

## 控制台不做什么 {#what-the-console-does-not-do}

- 没有账号，没有密码：令牌就是身份，Beacon 不另设别的。
- 没有审批流程：这些表单是令牌持有者本来就能经 API 做的事；控制台让它们看得见、做得快，而不是设卡。人看，Agent 做，暂停开关让它们停下。
- 不编辑配置，只能撤销变更：修改经 API、MCP 和命令行进行，每次都像检查文件那样检查。也不创建令牌，那是主机上管理套接字的事。
