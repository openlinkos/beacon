---
sourceHash: sha256-693935e2
---

# 状态页

Beacon 自己提供公开的状态页，内容来自它已有的东西：检测结果、告警组和维护窗口。一个状态页说明每个组件现在是否正常、过去 90 天每天的情况、正在出什么问题、在怎么处理，以及计划了哪些维护。不需要手工填写，也不需要再维护一个第三方状态页：检测点一致认为某个服务不可用时，状态页就这样显示；告警恢复时，事件也随之结束。

状态页用的是家族网站的设计（`@openlinkos/ui`），可以是英文、中文或两种语言，手机上也好读，跟随读者系统的浅色或深色模式，不需要脚本。它们在 17001 端口上与控制台一起提供，不需要登录：默认状态页在站点根路径 `/` 和 `/status` 上，每个状态页在 `/status/<name>` 上，也可以放在自己的域名的 `/` 上。

## 状态页显示什么 {#what-a-page-shows}

- **整体状态**，一行字：所有服务运行正常、某个组件不可用或部分不可用、正在维护，或者暂无数据。
- **进行中的事件**及其进展。
- **维护**：进行中的和已计划的，提前公告。
- **组件**：每个组件现在的状态，以及过去 90 天的状态条，每格一天：正常、部分中断、中断、维护或无数据；每一格的提示写明日期和当天的可用率，状态条下方是 90 天的可用率。手机上状态条只显示最近 30 天。
- **最近 30 天**：已经结束的事件和维护，每个事件都链接到它自己的页面（`/status/<name>/incidents/<id>`）。
- **检测点**，用它们的公开名称（目录里的 `name` 和 `network`），以及状态页列出的链接：主页、文档、举报滥用、隐私说明；页眉和页脚里有家族菜单。
- 页眉右侧的**登录**，通往 `/console` 的控制台（已登录时是**控制台**）；页脚里的一行**由开源的 Beacon 驱动**（[品牌与署名](#branding)）。

## 定义状态页 {#defining-a-page}

状态页是目录的一部分，写在 `status_pages` 下，与检测点、心跳和资源并列（[目录、检测目标和检测模块](catalog.md)）。和它们一样，状态页在首次启动时和执行 `beacon config import` 时从目录文件导入，通过 API、MCP 和命令行修改，与其余配置一起校验，每次修改都是历史里的一个版本，可以撤销：

```yaml
status_pages:
  - name: public                     # 小写字母、数字和连字符：/status/public
    title: {en: Example Status, zh: 示例服务状态}   # 文字可以是一个字符串，也可以每种语言一个
    description: {en: "Whether Example's public services work, from where their users are.", zh: 从用户所在的地方看，示例的公共服务是否正常。}
    default: true                    # 放在 / 和 /status 的状态页（否则按名称排第一的那个）
    host: uptime.example.com         # 可选：同时放在这个域名的 / 上
    brand: beacon                    # 采用设计系统里的哪个品牌：beacon、linkos、linkos-status、linkos-mirrors、openlinkos
    languages: [en, zh]              # 第一个是默认语言；不写就是 en
    components:
      - name: website
        title: {en: Website, zh: 网站}
        description: example.com 和 www.example.com
        services: [example.com, www.example.com]   # 检测目标的 service 标签
      - name: mirrors
        title: {en: Container mirrors, zh: 容器镜像}
        page: mirrors                # 有检测目标带 page: mirrors 标签的每个服务
    groups:                          # 可选：给组件加小标题
      - title: {en: Registries, zh: 镜像仓库}
        components: [mirrors]
    alerts: [ServiceDown, ServiceDegraded, MainlandPathDegraded]   # 算作事件的告警（默认就是这三个）
    notes: public                    # 哪些备注算进展：public（默认）或 all
    links:
      home: https://example.com/
      docs: https://docs.example.com/
      abuse: mailto:abuse@example.com
      privacy: https://example.com/privacy
    menu:                            # 页眉里的家族菜单，也是页脚的几列
      title: Example
      groups:
        - links: [{title: {en: Example home, zh: 示例主页}, url: "https://example.com/"}]
        - title: {en: Free public services, zh: 公共服务}
          links: [{title: Example Mirrors, url: "https://mirrors.example.com/"}]
```

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "https://status.example.com/api/v1/config/status_pages/public?note=the+public+page" \
  -d '{"title": "Example Status", "components": [{"name": "website", "title": "Website", "services": ["example.com"]}]}'
```

MCP 工具是 `get_config`、`put_config` 和 `delete_config`，`kind` 用 `status_pages`。状态页导出为 `catalog/status_pages.yaml`。校验内容：名称格式正确且不重复，有标题，至少一个组件，每个组件都由 `services` 或 `page` 标签指明服务，分组只列本页的组件，链接是 `https`、`http` 或 `mailto`，域名不和别的状态页重复，最多一个默认状态页。

### 组件与标签 {#components-and-labels}

一个组件背后是某些服务的检测目标。`services` 按检测目标的 `service` 标签指定服务，每个检测目标都有这个标签（[检测目标](catalog.md#targets)）。`page` 按检测目标上的另一个标签指定：`page: mirrors` 的组件，背后是有检测目标带 `page: mirrors` 标签的每个服务，所以在检测目标里加一个镜像，状态页上也就有了它。带 `visibility: internal` 标签的检测目标，不会经 `page` 进入任何组件。

组件的服务还没有任何检测目标时，照样接受，但会给出警告（在这次修改的 `warnings` 里，`check-config` 也会列出）：检测目标常常比展示它的状态页加得晚。在有检测目标之前，它显示无数据。

### 分组、语言和链接 {#groups-languages-and-links}

每段文字（`title`、`description`，分组或链接的 `title`）可以是所有语言共用的一个字符串，也可以每种语言一个：`{en: Status, zh: 状态}`。有两种语言的状态页，页眉里有语言菜单，页面和链接带 `?lang=zh`；默认语言不需要参数。家族菜单（`menu`）就是家族其他网站页眉里的那个菜单：一个按钮，里面是几组链接，比如组织的主页、开源项目和公共服务。有标题的分组同时也是页脚的几列。`links` 是页脚的链接。

## 组件的状态 {#the-state-of-a-component}

一个组件现在的状态，取下面几项里最坏的：

- **维护**：有维护窗口覆盖它的某个服务时；
- 它的服务上正在触发的**事件**：`ServiceDown` 是中断，其他告警是部分中断；
- 最近几分钟**检测点的一致结论**，按参考规则的逻辑（`deploy/rules/probes.yml`）：一个检测点连续 3 分钟检测失败，才算这个检测点认为服务失败；至少两个、且不少于上报检测点一半的检测点失败，服务就是不可用；至少两个但不到一半，就是部分不可用。单个检测点的抖动永远不会显示出来。

所以，只要还有关于某个服务的告警在触发，状态页就不会说它正常；检测点一致认定的故障，状态页会比告警早一两分钟显示，因为告警要再等一分钟确认。没有任何上报就是无数据。由几个服务组成的组件，取其中最坏的。

## 历史 {#history}

状态条的每一天，是检测点逐分钟的一致结论：由 VictoriaMetrics 按规则的逻辑在检测结果上计算，不需要录制规则；然后叠加事件（事件覆盖的分钟，至少是事件所说的状态）和维护窗口（维护中的分钟不计入，也不扣分）。

| 一天显示为 | 条件 |
| --- | --- |
| 中断 | 检测点一致认为不可用的时间至少两分钟，或有中断事件涉及这一天 |
| 部分中断 | 不可用或部分不可用的时间至少两分钟，或有部分中断事件涉及这一天 |
| 维护 | 当天有一部分在维护窗口里，窗口之外没有不可用 |
| 正常 | 有检测结果，且以上都不是 |
| 无数据 | 没有任何上报 |

可用率是：有检测结果、且不在维护中的分钟里，没有不可用的分钟所占的比例；部分中断算作可用。日子按配置里 `timezone` 时区的零点划分。Beacon 把算好的日子放在内存里，启动时计算最近 90 天，每天一次查询，覆盖所有状态页的服务；今天每分钟重算一次，过去的某一天在它结束后六小时内都会重算，以便收进检测点在网络中断期间排队、稍后才送达的结果。

## 事件 {#incidents}

事件是状态页服务上的告警组，级别为严重或警告，且告警名是这个状态页计入的（`alerts`；默认是 `ServiceDown`、`ServiceDegraded` 和 `MainlandPathDegraded`，即参考规则里的可用性告警，所以证书快到期不会成为公开的事件）。事件标题由 Beacon 按状态页的语言，根据影响和涉及的组件生成，比如“服务中断：网站”；告警自己的文字可能提到内部的东西，永远不会显示。

事件的进展，新的在前：

- **发现**：开始的时候：至少一半的检测点，或部分检测点，检测到失败；
- **处理中**：有人认领的时候；
- **备注**：人和 Agent 在告警组上留下的备注（`add_note`、`POST /api/v1/alerts/{id}/notes`、控制台）。备注是写给处理告警的人看的，所以默认只公开以 `public:` 开头的备注，并去掉这个前缀；`notes: all` 的状态页公开所有备注。经 API 或 MCP 留下的备注标为 Agent 的，在控制台或卡片上留下的标为人工的；不显示任何令牌或人的名字；
- **已恢复**：告警恢复的时候。

Beacon 把每个事件另存一份，保留 91 天，所以事件和它的页面比告警组活得久（已恢复的告警组 7 天后清理）。

## 维护 {#maintenance}

维护窗口是对某些服务的计划工作：状态页提前公告，进行期间把相关组件显示为维护，并在历史里标出这些天。用 `POST /api/v1/maintenance` 或 MCP 工具 `schedule_maintenance` 安排（作用域 `maintenance:write`）；用 `DELETE /api/v1/maintenance/{id}` 或 `end_maintenance` 提前结束或取消：

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/maintenance \
  -d '{"services": ["example.com"], "starts_at": "2026-11-03T02:00:00Z", "ends_at": "2026-11-03T03:00:00Z",
       "title": "Database upgrade", "description": "The site is read-only for a few minutes."}'
```

一个窗口最长 7 天，在 90 天内开始；开始时间在过去的，按现在算，所以窗口永远不会覆盖已经发生的事。它为每个服务建一个覆盖整个窗口的静默，备注是 `maintenance: <标题>`，这样维护中出的问题不会叫醒任何人；结束窗口时这些静默也随之结束。安排和结束都记入审计日志，并回显到告警群。

用别的方式建的静默（`POST /api/v1/silences`、`beacon silence create`），只要备注以 `maintenance:` 开头，也会显示为维护：涉及的组件是其 `service` 匹配条件匹配到的服务所在的组件，持续到静默结束，只要 Beacon 还保留着它。

## 自己的域名 {#a-host-of-its-own}

设了 `host` 的状态页，也放在那个域名的 `/` 上：事件在 `/incidents/<id>`，JSON 在 `/status.json`，订阅在 `/feed.atom`，徽章在 `/badge.svg` 和 `/components/<name>/badge.svg`。在这个域名上，Beacon 的其他东西都不应答：控制台、API 和 MCP 都不行，所以页面上也没有**登录**。把域名指向反向代理，并原样转发 Host；参考部署里是 `deploy/Caddyfile` 中的一段：

```
uptime.example.com {
	import common
	reverse_proxy beacon:17001 {
		header_up Host {host}
		header_up X-Forwarded-For {client_ip}
	}
}
```

Caddy 像为其他域名一样为它申请证书。Beacon 按请求的 `Host` 头决定提供哪个状态页，所以代理不能改写它。

## JSON、订阅和徽章 {#json-feed-and-badges}

| 路径 | 内容 |
| --- | --- |
| `/status/<name>.json`（默认状态页是 `/status.json`） | 状态页的状态：组件及其状态和 90 天历史，事件（进行中的，然后是最近 30 天的）及其进展，维护窗口，链接。结构保持稳定：只增加字段，不修改也不删除 |
| `/status/<name>/feed.atom` | 事件和维护窗口的 Atom 订阅，每个事件带它的进展 |
| `/status/<name>/badge.svg` | 显示状态页状态的小徽章：正常、部分中断、中断、维护或无数据，用设计系统的颜色 |
| `/status/<name>/components/<component>/badge.svg` | 某个组件的徽章 |

它们和状态页一样接受 `?lang=`，可以被其他网站嵌入和读取（`Access-Control-Allow-Origin: *`）。Agent 用 `GET /api/v1/status/<name>`（`status:read`）或 MCP 工具 `get_status_page` 拿到同样的 JSON。其中不含任何 service 标签和令牌：公开的只有状态页自己的文字。

## 品牌与署名 {#branding}

每个公开页面都写明自己运行在什么上：页脚有一行**由开源的 Beacon 驱动**，带 Beacon 的标志，链接到 [beacon.openlinkos.com](https://beacon.openlinkos.com/)（中文页面链接到它的中文文档）和 [GitHub](https://github.com/openlinkos/beacon) 上的代码。希望状态页只出现自己名字的团队，可以去掉这一行：

```yaml
branding:
  title: Example Operations   # 没有状态页时 / 上那个页面的标题（不写就是 Beacon）
  powered_by: false           # 公开页面上不写「由开源的 Beacon 驱动」（默认 true）
```

`powered_by` 只管公开页面；控制台总是显示 Beacon 的版本和项目的链接（[控制台](pages.md)）。

一个状态页都没有时，`/` 是同样设计的一个小页面：这套部署的标题（`branding.title`）、一句话说明 Beacon 是什么、**登录**，以及页脚的那一行。

## 缓存与搜索引擎 {#caching-and-search-engines}

一个状态页最多每 30 秒计算一次，每个应答都是公开、可缓存的：`Cache-Control: public, max-age=30` 和 `ETag`，浏览器或边缘缓存再次请求时代价很小，会得到 `304 Not Modified`。已登录控制台的人请求的页面，页头写的是**控制台**而不是**登录**，这样的应答是 `private, max-age=30`，共享缓存不会保存它；两者都通往 `/console`，所以缓存里写着**登录**的页面也无妨。和控制台的页面不同，状态页不标 `noindex`；`/robots.txt` 让搜索引擎读取 `/`、`/status` 和它们加载的文件，不进 `/console`、`/api/`、`/mcp` 以及控制台链接的旧路径（`/alerts/`、`/sessions/`）；状态页自己的域名上全部可读。它们使用控制台的 `Content-Security-Policy`，但不允许任何脚本，不发送 referrer，也不能被嵌入框架。
