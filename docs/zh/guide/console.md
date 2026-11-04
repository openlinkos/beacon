---
sourceHash: sha256-564c3115
---

# 复查 Agent 做了什么

读完这一页，你可以在一个地方看到 Beacon 和 Agent 们正在做的一切：正在告警的有哪些、每一条做了什么处理，Agent 在机器上开过的每一次会话及其回放，成员和他们的 Agent 及其凭据，配置及其历史——还有那个停下 Agent 机器访问的开关。控制台是给值班的人、对机器负责的人用的；Agent 自己走 [REST API](../reference/api.md) 和 [MCP 工具](../reference/mcp.md)。告警卡片上的链接不用登录就能打开那一页告警；登录之后能看到其余的。

你需要一个账号（[邀请你的团队](people.md)）；查看者能看一切，对看到的东西动手则需要相应角色。控制台由 Beacon 自己提供，在 `public_url` 下的 17001 端口、与 API 和 MCP 并排的 `/console` 上；站点根路径 `/` 是公开的（[发布状态页](status-pages.md)）。

## 登录 {#signing-in}

打开 `https://status.example.com/console`：你会被送到 `/console/sign-in`，输入用户名和密码，然后回到刚才想打开的页面。一次登录保持 12 小时；浏览器只保存会话 id，放在 `HttpOnly`、`Secure`、`SameSite=Strict` 的 cookie 里，Beacon 那边只存它的 SHA-256。会话能做什么由成员当时的角色决定，每个请求都重读：调低角色立刻生效，停用账号或改密码会结束其会话。cookie 是 `Secure` 的，所以控制台要走 HTTPS（或 `localhost`）；登录失败时表单会说明原因——[登录控制台失败](../self-hosting/troubleshooting.md#signing-in-to-the-console-fails)列出了每条消息和对应的处理。

表单下方的**用令牌登录**仍然接受粘贴进来的 Beacon 令牌，再保留一个版本：给脚本用，也给还没有账号的 Beacon——那里只有这一种方式。令牌至少要有 `status:read`；会话持续 12 小时并绑定该令牌，吊销令牌即结束会话。用令牌登录的会话不能允许 MCP 客户端：OAuth 客户端总是以某个人的名义行事。

## 各个页面 {#the-pages}

每个页面同一个框架：窄侧栏里的各分区（带计数），列表左侧的筛选（facet），页面底部 Beacon 的版本和指向文档、源码、问题反馈的链接。列表最新的在前，每页 50 条；每个筛选都是链接，所以每个视图都有可以分享的 URL。

| 页面 | 显示什么 |
| --- | --- |
| `/console` 总览 | 正在告警的告警，按严重程度分组；应汇报与已汇报的检测点；机器、通道及是否暂停；今天唤醒的 Agent；今天的会话；配置版本。每个数字都链到对应列表 |
| `/console/alerts`、`/console/alerts/{id}` | 所有告警组（已恢复的留 7 天），可按状态、严重程度、服务、已确认、已静默筛选；告警页（见下）带确认、静默、加备注的表单 |
| `/console/machines`、`/console/machines/{name}` | 通道能到达的机器、NAT 后的机器是否连着、最近 24 小时会话；单台机器的地址、主机密钥、最近会话和上面执行过的命令。列表上方是整条通道的暂停开关 |
| `/console/sessions`、`/console/sessions/{id}` | 所有会话，可按 Agent、机器、类型、via、退出状态、日期筛选，可搜命令；会话页带回放（见下） |
| `/console/agents`、`/console/agents/{name}` | 你的 Agent（所有者看到所有人的）：各自现在能做什么、凭据、密钥；OAuth 接入命令、无界面 Agent 的令牌表单；每个 Agent 的页面可修改、可吊销 |
| `/console/tokens` | 所有令牌：scope、机器、密钥、到期、属于谁的 Agent；账号出现之前的标成**Legacy**，旁边是认领表单。永远不会出现密钥本身 |
| `/console/config`、`/console/config/history/{version}` | 配置只读（检测点、心跳、资源、目标集、模块）及其历史：谁、何时、经什么、为什么，每个版本逐项显示为 diff，可回退 |
| `/console/hooks` | 已配置的 Agent 钩子和每一次调用：钩子、事件、状态、尝试次数、时间、为哪条告警 |
| `/console/people`、`/console/account` | 成员和角色；邀请、改角色、停用、密码重置（[邀请你的团队](people.md)）；你自己的账号和密码 |
| `/console/audit`、`/console/changes` | 审计日志，可按谁、动作、via 筛选；来自 CI 和 Flux 的变更事件 |
| `/console/setup`、`/console/join`、`/console/reset`、`/oauth/authorize` | 一次性链接打开的页面，和 MCP 客户端引导成员前往的授权页 |

控制台的表单就是 API 和命令行的操作，scope 相同，以登录成员的身份、`via` 记作 `console` 执行：和每一次写入一样记入审计日志并回显到告警群。角色不允许的表单不会出现，强行提交也会被拒绝。角色不允许的页面显示「Not with your role」，并说明需要哪个 scope、你是什么角色。

## 告警页 {#the-alerts-page}

卡片上的**Timeline**按钮打开它。从上到下：告警本身（标题、状态、严重程度、起始时间、标签、runbook）；人们被告知和做过什么（确认、静默、呼叫）；**Probes**——告警服务在每个检测点的最新结果和原因，与 `get_evidence` 给的一致；同一故障域里的**相关告警**；**Agents**——能否使用机器、哪些钩子被唤醒过、一行「N sessions on M machines, K commands」；**Sessions**——告警时间窗内、其标签点名的机器上的每一次会话，各带**Replay**链接；还有**Notes**、**Changes**和**Timeline**——以上一切按时间排好，与 `get_timeline` 给的一致。时间窗从告警开始前 30 分钟起，与[证据](on-call.md#the-evidence)相同。

## 会话页与回放 {#the-sessions-page-and-its-replay}

谁、在哪台机器上、经什么（ssh、MCP 还是 API）、何时开始、用了多久、退出状态——以及嵌在页面里回放的录像，用 [asciinema-player](https://github.com/asciinema/asciinema-player) 播放。录像格式是 asciicast v3，与 SSH 通道录制的一致：输出、输入、窗口尺寸变化、每条命令的标记、退出状态。播放器下面是交互 shell 的各条命令及其退出状态，或一条命令的输出；录像也可以下载。还在进行的会话显示其记录；录像在会话结束后可播放。

## 签名链接 {#signed-links}

Beacon 放进告警群里的链接，不用登录就能打开一个页面。这样的链接以 `?s=<expiry>.<signature>` 结尾：过期时间是 Unix 秒，签名是方法、路径和过期时间的 base64url HMAC-SHA256，密钥由 Beacon 保存在自己的数据库里。链接的有效期是 `pages.link_ttl`，默认 30 天，且只授予一个页面、只读：告警页，或带录像的会话页。指向别的路径、被改过的过期时间或已过期的链接都会失败，而每种失败都是普通的 404，与不存在的页面一样，错误的链接不会泄露任何东西。

**拿到链接的任何人都能看那一页，包括录像，不需要在任何地方登录。**链接发往的群必须是私密的，链接也不应转发到录像不该到的地方；`link_ttl` 在群的习性允许的范围内尽量设短。凡是成员可能顺手拿到链接的地方 Beacon 都给：卡片的**Timeline**按钮、告警群回显里每个会话一条的**Replay**链接、以及 API 和 MCP 的应答，这样 Agent 也能把链接递给人。

0.3.0 之前生成的链接指向站点根下的 `/alerts/{id}` 和 `/sessions/{id}`；这些路径仍然应答，转发（`308`）到 `/console` 下的新路径。控制台其余旧路径也都带着查询参数转发到 `/console` 下的新位置，书签继续可用（[控制台在旧链接上显示 404](../self-hosting/troubleshooting.md#the-console-says-404-on-an-old-link)）。

## 也可以用令牌 {#tokens-instead}

任何带相应 scope 的凭据都能像打开对应 API 路由一样打开告警页和会话页，只读：令牌（`lbt_…`）或 OAuth 访问令牌（`lba_…`），告警页要 `status:read`，会话页及其录像要 `sessions:read` 或 `machines:ssh`。以 `Authorization: Bearer …` 发送；缺 scope 的、未知的和什么都不带的，得到的是同一个 404。

## 它怎么做的，它不做什么 {#how-it-is-built-what-it-does-not-do}

控制台基于 OpenLinkOS 设计系统（[`@openlinkos/ui`](https://github.com/openlinkos/design/tree/master/packages/ui)），用它的工位框架和 Beacon 的品牌，家族的工具长得一样；样式表、字体和播放器的代码都嵌在 Beacon 的二进制里，不从任何其他地方取东西——`Content-Security-Policy` 只允许来自 Beacon 的脚本、样式、字体和表单。一套页面适配所有屏幕：1200px 起完整侧栏，860px 起图标栏，手机宽度下列表重排成卡片，全靠 CSS。来自 Beacon 之外的文字（告警注解、备注、命令）经 bridge 清理、输出时转义。控制台跟随配置的语言（中文或英文）和系统的深浅色；其页面带 `noindex`，因为页面链接里带着签名。

它不做的三件事：不从任何别处登录——Beacon 自己的账号是唯一的入口；没有审批流——表单就是成员角色经 API 本来就允许的操作，刹车是暂停开关；也不改配置、最多回退一次改动，回退和文件一样过同样的检查。

## 检查一下 {#check-it}

以人的身份走一遍循环：从卡片或 `/console/alerts` 打开一条告警，读它的检测点和时间线，再从那里打开一次会话的回放。你会看到每个检测点的最新结果及其原因、告警机器上的每一次会话及其**Replay**链接，以及按时间排好的各事件。然后确认记录完整：把 `get_timeline`（或 API 的 `GET /api/v1/alerts/{id}/timeline`）与告警页对照，两边应是同样的事件——钩子的调用、会话、`probe_now` 的结果和备注。

## 下一步 {#next}

- [让 Agent 值班](on-call.md)：控制台显示的这些记录从哪里来。
- [发布状态页](status-pages.md)：同一个故事，公开的、一次只讲一个服务。
- [停下 Agent，暂停一切](stop.md)：机器页上的暂停开关，以及其他刹车。
