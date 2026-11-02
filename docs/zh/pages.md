---
sourceHash: sha256-86ebc719
---

# 控制台

控制台是团队里的人查看 Beacon 和 Agent 在做什么的地方：哪些告警在触发、每条告警已经做了什么，Agent 在机器上开过的每一次会话及其回放，令牌，配置及其历史，还有让 Agent 立即停止访问机器的开关。它给值班的人和对机器负责的人用；Agent 自己用的是 [API](reference/api.md) 和 [MCP 工具](reference/mcp.md)。告警卡片上的链接不用登录就能打开这条告警的页面；登录后能看到其余一切。

控制台由 Beacon 自己提供，在 17001 端口的 `/console`，和 API、MCP 在一起，地址在 `public_url` 下。站点根路径 `/` 是公开的：放默认的[状态页](status.md)；没有状态页时，是一个写明这套部署名称的页面。

## 登录 {#signing-in}

目前用 Beacon 令牌登录，像密码一样粘贴进去；登录后控制台显示什么、允许做什么，都由这个令牌的作用域决定。在中心（运行 Beacon 的机器）上给自己建一个令牌：

```bash
docker exec beacon beacon token create you -scopes status:read,sessions:read,alerts:write -expires 30d
```

令牌只显示这一次。打开 `https://status.example.com/console`，会先跳到 `/console/sign-in`，令牌通过后再回到你要的页面。`/` 上的公开页面在页头右侧有**登录**（登录后显示**控制台**）；放在独立域名上的状态页两者都没有，因为控制台不在那个域名上应答。

令牌至少要有 `status:read`。登录失败时，表单会说明原因（`language: zh` 时显示中文）：

| 提示 | 怎么办 |
| --- | --- |
| 「令牌不存在或已过期。」 | 新建一个令牌，或者检查是否粘贴完整（以 `lbt_` 开头） |
| 「令牌 … 没有 status:read，控制台需要它才能显示内容。」 | 新建一个带 `status:read` 的令牌 |
| 「这里登录失败的次数太多，请过几分钟再试。」 | 同一个客户端地址 10 分钟内失败 20 次，这 10 分钟剩下的时间里都会被拒绝，和 API 的规则一样 |
| 「表单已过期，请重新粘贴令牌。」 | 刷新页面。一直这样的话，说明浏览器没有保存表单的 cookie：控制台需要 HTTPS（或 `localhost`），见下文 |

- 一次登录就是 Beacon 数据库里的一个会话，有效 12 小时；令牌更早到期的话，随令牌一起到期。浏览器只保存会话 id，放在 `HttpOnly`、`Secure`、`SameSite=Strict` 的 cookie 里（`__Host-beacon-console`，路径是 `/`，`__Host-` cookie 要求如此）；数据库只存它的 SHA-256，令牌本身不保存。
- 会话和这个令牌绑定：吊销令牌，或者用同名的新令牌替换它（吊销后重建），会话立即结束。作用域在每次请求时重新读取。
- 登录表单带着自己的密钥，别的网站没法用它挑的令牌替你登录。登录会记入审计日志（`console_sign_in`）。
- **退出登录**在侧栏底部的菜单里。
- cookie 是 `Secure` 的，所以控制台需要 HTTPS（或 `localhost`）：把它放在提供 `public_url` 的 TLS 代理后面，参考部署就是这样。

## 各个页面 {#the-pages}

所有页面框架相同：窄侧栏列出各栏目和数量；顶栏是导航路径、标题和数量；列表页左侧是筛选项；页脚是 Beacon 的版本，以及文档、GitHub 上的代码和反馈问题的链接。列表按时间倒序，每页 50 条；每个筛选都是链接，所以每个视图都有可以分享的 URL。

| 页面 | 显示什么 | 需要 |
| --- | --- | --- |
| `/console` 概览 | 按级别统计的触发中告警；应上报的检测点，以及最近 5 分钟内通过网关上报的检测点；机器（几台关键、NAT 后面的有几台已连接）；SSH 通道（运行中，或从何时起暂停）；今天唤醒了哪些 Agent、结果如何；今天的会话；配置版本和修改人；Beacon 版本。每个数字都链接到对应的列表 | `status:read` |
| `/console/alerts` | Beacon 保存的所有告警组（已恢复的保留 7 天），可按状态、级别、服务、是否认领、是否静默筛选，可搜索标题、服务和标签 | `status:read` |
| `/console/alerts/{id}` | 告警页面（见下文），附表单：带备注认领、静默 1、4 或 24 小时、添加备注 | `status:read`；表单要 `alerts:write` |
| `/console/machines` | SSH 通道能到达的机器：地址，NAT 后面的机器则显示是否已连接；是否关键；24 小时内的会话；最近一次会话。上方是整个通道的暂停开关 | `status:read` 或 `machines:ssh`；开关要 `machines:write` |
| `/console/machines/{name}` | 一台机器：地址、账号、主机密钥和备注，最近的会话，以及在上面执行过的命令 | `status:read` 或 `machines:ssh` |
| `/console/sessions` | 所有会话，可按 Agent、机器、类型、途径、退出码和日期筛选，可搜索命令 | `status:read` 或 `machines:ssh` |
| `/console/sessions/{id}` | 会话页面（见下文），带回放 | `sessions:read` 或 `machines:ssh` |
| `/console/tokens` | 所有令牌：名称、作用域、能到达的机器、SSH 密钥指纹、何时由谁创建、何时到期、最近一次自动续期。从不显示密钥 | `status:read`；吊销要 `machines:write` |
| `/console/config` | Beacon 保存的配置，只读：检测点（是否应上报、是否在上报）、心跳、资源、目标集和检测模块；下面是版本历史：谁、何时、通过什么途径、为什么 | `status:read` 或 `config:write` |
| `/console/config/history/{version}` | 某个版本逐项改了什么，以差异形式显示，附撤销表单 | `status:read` 或 `config:write`；撤销要 `config:write` |
| `/console/agents` | 配置的 Agent 钩子（只显示地址的主机名，不显示路径和查询参数）和发出的每次调用：钩子、事件、状态、HTTP 状态码、尝试次数、时间、对应的告警 | `status:read` |
| `/console/audit` | 审计日志，可按操作者、操作和途径筛选，可搜索 | `status:read` |
| `/console/changes` | 来自 CI、Flux 等的变更事件，可按来源、类型、服务和机器筛选 | `status:read` |

令牌缺少某个页面要求的作用域时，页面显示「这个令牌不行」，并写明需要什么作用域、这个令牌有哪些作用域。页面不存在或已不存在（比如恢复超过 7 天的告警组）时，已登录的人看到「找不到」，其他人得到普通的 404。

## 控制台能做什么 {#what-the-console-may-do}

控制台的表单就是 API 的操作，作用域也相同，以登录所用令牌的身份执行，途径记为 `console`：和其他写操作一样记入审计日志，并回显到告警群。

| 表单 | 作用域 | 等同于 |
| --- | --- | --- |
| 认领告警，可带备注 | `alerts:write` | `POST /api/v1/alerts/{id}/ack` |
| 静默告警 1、4 或 24 小时，写明原因 | `alerts:write` | `POST /api/v1/silences` |
| 给告警加备注 | `alerts:write` | `POST /api/v1/alerts/{id}/notes` |
| 暂停或恢复 Agent 对机器的访问 | `machines:write` | `POST /api/v1/ssh/pause`、`/resume` |
| 吊销能到达机器的令牌 | `machines:write` | `beacon token revoke` |
| 撤销一次配置变更 | `config:write` | `POST /api/v1/config/history/{version}/revert` |

每个表单都带着会话的 CSRF 密钥；浏览器表明请求来自别的网站时，一律拒绝。令牌作用域不允许的表单不会出现，硬发过来也会被拒绝。

**在控制台吊销令牌。**令牌在中心上通过管理套接字创建，大多也在那里吊销。控制台只吊销能通过 SSH 通道到达机器的令牌（`machines:ssh`）：这相当于只针对一个 Agent 的暂停开关：从这一刻起，它的新连接和工具调用都会被拒绝，其他 Agent 不受影响，所以它要的作用域和暂停开关一样，是 `machines:write`。它已经打开的连接会保持到自己结束；要连这些一起断开，就暂停通道。吊销同时结束这个令牌在控制台的登录；吊销自己的令牌会让你退出登录。机器和服务的令牌（检测点、主机、心跳、规则引擎、变更发送方、指标读取方）不能在页面上吊销：监控靠它们运转，一条告警都不能丢。

## 告警页面：`/console/alerts/{id}` {#the-alerts-page-consolealertsid}

卡片上的**时间线**按钮打开它。从上到下依次是：

- 告警本身：标题、状态、级别、从何时开始（或何时恢复、持续了多久）、描述、服务、标签、处理手册和面板；
- 通知了谁、谁做了什么：认领、静默、加急、电话通知；
- **检测**：这条告警的服务在每个检测点的最新结果和原因，和 `get_evidence` 返回的一样：正常、从何时开始失败、无数据、失败原因、耗时、HTTP 状态码、尝试次数、证书到期时间；
- **相关告警**：同一故障域或同一服务下其他正在触发的告警，各自链接到自己的页面；
- **Agent**：Agent 现在能否访问机器，通道暂停的话从何时起；唤醒了哪些钩子、何时唤醒；一行「N 次会话，M 台机器，K 条命令」；
- **会话**：告警时间范围内，在它的标签所指的机器上，或由认领过、留过备注的 Agent 发起的每一次会话：谁、怎么连的、在哪台机器、做了什么、退出码；交互 shell 会列出每条命令的退出码和耗时；每次会话都有**回放**链接；
- **备注**、**变更**（告警前后的变更事件）、**标签**；
- **时间线**：以上一切按时间排列，和 `get_timeline` 返回的一致。

时间范围从告警开始前 30 分钟起，到现在为止；告警已恢复的，到恢复后 30 分钟为止，和[证据](agents.md#the-evidence)一致。

## 会话页面：`/console/sessions/{id}` {#the-sessions-page-consolesessionsid}

谁、在哪台机器、通过什么（ssh、MCP 或 API）、什么类型的会话、何时开始、持续多久、退出码，以及用 [asciinema-player](https://github.com/asciinema/asciinema-player) 在页面里回放的录像。录像是 asciicast v3 格式，和 SSH 通道录下的一样：输出、输入、窗口大小变化、每条命令处的标记、退出码。播放器下方是交互 shell 的各条命令及其退出码，或者一条命令的输出。录像也可以下载。还没结束的会话只显示记录，结束后才能回放。

## 签名链接 {#signed-links}

Beacon 发到群里的链接，不用登录就能打开一个页面。这种链接以 `?s=<到期时间>.<签名>` 结尾：到期时间是 Unix 秒数；签名是对方法、路径和到期时间（每项后面跟一个换行）算出的 HMAC-SHA256，用 base64url 编码，密钥有 32 字节，Beacon 第一次启动时生成并存在数据库里。链接的有效期是 `pages.link_ttl`，默认 30 天：

```yaml
public_url: https://beacon.example.com   # 控制台的地址；没有它就没有链接
pages:
  link_ttl: 720h
```

一个链接只授权一个页面，只读，到期为止：告警页面，或者会话页面及其录像。页面里的链接（到会话、到录像）和打开它的链接同时到期。换了路径、换了方法、改过到期时间的链接都会失败，过期的链接也一样。所有失败都是普通的 404，和不存在的页面没有区别，所以错的链接不会泄露任何东西。

**拿到链接的任何人，不用在任何地方登录，就能看到那个页面，包括录像。**所以链接发往的群必须是私密的，链接也不要转发到录像不该去的地方。目前还没有在到期前撤销链接的命令（那意味着更换密钥）；`link_ttl` 在群里的使用习惯允许的范围内设得越短越好。

用链接打开的页面只显示这一页，侧栏里有**登录**，登录后能看其余页面。已经登录的话，同一个链接会在控制台里打开这个页面，并显示令牌作用域允许的表单。

**0.3.0 之前发出的链接。**在那之前控制台在站点根路径上，群里已有的链接写的是 `/alerts/{id}` 和 `/sessions/{id}`（以及 `/sessions/{id}/recording`），签名也是对这些路径算的。这些路径仍然应答：签名对旧路径校验通过的链接，会转到（`308`）页面在 `/console` 下的路径，并换上对新路径的签名，到期时间和原来相同。伪造或过期的链接转过去时不带签名，于是和以前一样得到 404；已登录的话，照样能打开页面。控制台的其他旧路径（`/sign-in`、`/alerts`、`/machines`、`/sessions`、`/tokens`、`/config`、`/agents`、`/audit`、`/changes` 及其下的页面）都带着查询参数转到 `/console` 下的对应位置，所以书签照样能用。表单只提交到 `/console`。

凡是人可能拿到链接的地方，Beacon 都会附上：

- 卡片：**时间线**按钮和 Agent 区块；
- 群里回显 Agent 在机器上做了什么的消息：每次会话一个**回放**链接；
- API 和 MCP 的结果，方便 Agent 把链接交给人：告警上的 `page`（`GET /api/v1/alerts/{id}`、`get_alert`）、证据里的 `links.page`、时间线里会话事件的 `url`、会话上的 `page`（`list_sessions`、`get_session`、证据里的 `done.sessions[]`）。

## 用令牌打开 {#tokens-instead}

Bearer 令牌也能打开告警页面和会话页面，规则和对应的 API 路由相同，只读：告警页面要 `status:read`，会话页面及其录像要 `sessions:read` 或 `machines:ssh`。以 `Authorization: Bearer lbt_…` 发送；这样打开的页面，里面到会话和录像的链接有效 1 小时。缺少作用域的令牌、不存在的令牌、不带令牌，得到的都是同样的 404。

## 实现方式 {#how-it-is-built}

- 基于 OpenLinkOS 设计系统 [`@openlinkos/ui`](https://github.com/openlinkos/design/tree/master/packages/ui) 0.7.0，用它的工作台框架、布局和 Beacon 的品牌（`data-brand="beacon"`），和 Origin 的管理工作台一样，让同一系列的产品看起来是一家。样式表、图标和 Geist 字体放在 `internal/web/static/ol` 下，许可证和哈希写在那里的 `NOTICE` 里；`scripts/update-ui.sh` 从 npm 更新它们。
- 所有屏幕共用一个页面：1200px 以上是完整侧栏，860px 起是图标栏，再窄是抽屉；手机宽度下列表改排成卡片，全由 CSS 完成。服务器从不过问屏幕多宽。
- 除了 Beacon 自己，不从任何地方加载东西：样式表、字体、播放器的 JavaScript 和 CSS 都嵌在二进制里，图标是页面内的 sprite，唯一的脚本是播放器，没有内联脚本和内联样式。`Content-Security-Policy` 只允许来自 Beacon 的脚本、样式、字体、请求和表单（有 `'wasm-unsafe-eval'` 是因为播放器的终端是 WebAssembly）。页面标记为 `noindex`（`/robots.txt` 也不让搜索引擎进 `/console`），并且不发送 referrer，因为链接里带着签名。
- Beacon 是开源软件，控制台也这样写明：每个页面的页脚都有 Beacon 的版本，以及文档（[beacon.openlinkos.com](https://beacon.openlinkos.com/zh/)，随控制台的语言）、[GitHub](https://github.com/openlinkos/beacon) 上的代码和[反馈问题](https://github.com/openlinkos/beacon/issues/new/choose)的链接；登录页也在表单下方写明。只有这几个链接会主动通往 Beacon 之外。`branding.powered_by`（[状态页](status.md#branding)）不影响它们。
- 来自 Beacon 之外的文字（告警注释、备注、命令、变更事件）先由桥清理，输出时再转义。
- 控制台使用配置的 `language`，中文或英文，跟随系统的浅色或深色模式。

## 控制台不做什么 {#what-the-console-does-not-do}

- 不设审批流程：这些表单都是令牌持有者本来就能通过 API 做的事，控制台只是让它们看得见、做得快，而不是多一道关卡。人看着，Agent 动手，暂停开关让它们停下。
- 不编辑配置，只能撤销已有的变更：修改通过 API、MCP 和命令行完成，每次修改都像检查文件一样检查。也不创建令牌，那是中心上管理套接字的事。
