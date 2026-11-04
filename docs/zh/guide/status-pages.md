---
sourceHash: sha256-a6fad347
---

# 状态页

状态页告诉你的用户：服务现在是否正常，过去 90 天表现如何，正在出什么问题、在怎么处理，以及计划了哪些维护。Beacon 用它已有的东西生成状态页：检测结果、告警组和维护窗口。不用手工更新，也不用另外维护一个第三方状态页：检测点一致认为某个服务故障，状态页就这样显示；告警恢复，事件也随之结束。这一页写给运行 Beacon、想要这样一个公开页面的人，或者想改参考部署自带那个页面的人。

状态页由 Beacon 自己提供，和控制台一起在 17001 端口上，不需要登录：默认状态页在站点根路径 `/` 和 `/status`，每个状态页在 `/status/<name>`；如果给它配了自己的域名，也在那个域名的 `/` 上。页面采用家族网站的设计（`@openlinkos/ui`），可以是英文、中文或两种都有，手机上也好读，跟随读者系统的浅色或深色模式，不需要脚本。

## 你已经有的状态页 {#the-page-you-already-have}

参考部署自带一个状态页，定义在 `deploy/catalog/status_pages.yaml`：名字是 `status`，是默认状态页，标题「Example Status」，有两个组件：**Website**（服务 `example.com`）和 **Status and alerts**（`beacon`，也就是 Beacon 自己的状态域名）。按[30 分钟装好](../self-hosting/install.md)装完后打开 `https://status.example.com/`：第一个检测点上报之前，页面显示 **No data yet**（中文页面上是「暂无数据」）；有了第一批结果，一两分钟后各组件显示各自的状态，两个都正常时整页显示 **All systems operational**（「所有服务运行正常」）。

一个状态页从上到下显示：

- **整体状态**，一行字：所有服务运行正常、某个组件故障或部分故障、正在维护，或者暂无数据。
- **进行中的事件**，以及它们的进展。
- **维护**：进行中的和已计划的，提前公告。
- **组件**：每个组件现在的状态，以及过去 90 天的状态条，每格一天：正常、部分中断、中断、维护或无数据；每一格的提示写着日期和当天的可用率，状态条旁是 90 天的可用率。手机上状态条只显示最近 30 天。
- **最近 30 天**：已经结束的事件和维护，每个事件链接到它自己的页面（`/status/<name>/incidents/<id>`）。
- **检测点**，用公开名称显示（目录里的 `name` 和 `network`），以及状态页配置的链接：主页、文档、举报滥用、隐私说明；页眉和页脚里还有家族菜单。
- 页眉右侧的**登录**，通往 `/console` 的控制台（已登录的人看到的是**控制台**）；页脚里有一行**由开源的 Beacon 驱动**（[品牌与署名](#branding)）。

把这两个示例组件换成你自己的服务；不想要公开页面，删掉这个文件即可。

## 定义自己的状态页 {#define-your-own-page}

状态页是目录的一部分，写在 `status_pages` 下，和检测点、心跳、到期资源并列（[检测什么，从哪里检测](monitoring.md)）。和它们一样，状态页在第一次启动时和执行 `beacon config import` 时从目录文件导入，通过 API、MCP 和命令行修改，和其余配置一起校验，每次修改都是历史里一个可以撤销的版本：

```yaml
status_pages:
  - name: public                     # 小写字母、数字和连字符：/status/public
    title: {en: Example Status, zh: 示例服务状态}   # 文字可以是一个字符串，也可以每种语言一个
    description: {en: "Whether Example's public services work, from where their users are.", zh: 从用户所在的地方看，示例的公共服务是否正常。}
    default: true                    # 放在 / 和 /status 的状态页（没有标的话，取名字排第一的）
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
        page: mirrors                # 有检测目标带 page: mirrors 标签的所有服务
    groups:                          # 可选：组件上方的小标题
      - title: {en: Registries, zh: 镜像仓库}
        components: [mirrors]
    alerts: [ServiceDown, ServiceDegraded, MainlandPathDegraded]   # 算作事件的告警（默认就是这三个）
    notes: public                    # 哪些备注算作进展：public（默认）或 all
    links:
      home: https://example.com/
      docs: https://docs.example.com/
      abuse: mailto:abuse@example.com
      privacy: https://example.com/privacy
    menu:                            # 页眉里的家族菜单，也是页脚的几列
      title: Example
      groups:
        - links: [{title: {en: Example home, zh: 示例主页}, url: "https://example.com/"}]
        - title: {en: Free public services, zh: 免费公共服务}
          links: [{title: Example Mirrors, url: "https://mirrors.example.com/"}]
```

也可以通过 API，用带 `config:write` 的令牌：

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "https://status.example.com/api/v1/config/status_pages/public?note=the+public+page" \
  -d '{"title": "Example Status", "components": [{"name": "website", "title": "Website", "services": ["example.com"]}]}'
```

MCP 工具是 `get_config`、`put_config` 和 `delete_config`，`kind` 用 `status_pages`。状态页导出为 `catalog/status_pages.yaml`。通不过下面这些校验的状态页会被拒绝：名字格式正确且不重复；有标题（最多 120 个字符；描述最多 1000 个字符）；至少一个组件；每个组件都用 `services` 或 `page` 标签指明服务；分组只能列本页的组件，一个组件最多在一个分组里；链接是 `https`、`http` 或 `mailto`；域名不和别的状态页重复；最多一个默认状态页。

### 组件与标签 {#components-and-labels}

组件背后是某些服务的检测目标。`services` 用检测目标的 `service` 标签指定服务，每个检测目标都有这个标签（[检测目标](monitoring.md#targets)）。`page` 用检测目标上的另一个标签来指定：`page: mirrors` 的组件，背后是所有带 `page: mirrors` 标签的检测目标所属的服务，所以在检测目标里加一个镜像，状态页上也就有了它。带 `visibility: internal` 标签的检测目标，不会通过 `page` 进入任何组件。

组件里的服务还没有任何检测目标时，照样接受，但会给出警告（在这次修改的 `warnings` 里，`check-config` 也会列出），比如 `status page "public": component "website" names service "www.example.com", which no probe target has; it shows no data until one does`：检测目标常常比展示它的状态页加得晚。在有检测目标之前，这个组件显示无数据。

### 分组、语言和链接 {#groups-languages-and-links}

每段文字（`title`、`description`，分组或链接的 `title`）可以是所有语言共用的一个字符串，也可以每种语言一个：`{en: Status, zh: 状态}`。有两种语言的状态页，页眉里有语言菜单，页面和链接带 `?lang=zh`；默认语言不需要参数。家族菜单（`menu`）就是家族其他网站页眉里的那个菜单：一个按钮，点开是几组链接，比如组织主页、开源项目和免费公共服务。有标题的分组同时也是页脚的几列。`links` 是页脚的链接。

## 组件的状态 {#the-state-of-a-component}

组件现在的状态，取下面几项里最坏的：

- **维护**：有维护窗口覆盖它的某个服务时；
- 它的服务上正在触发的**事件**：`ServiceDown` 是中断，其他告警是部分中断；
- 最近几分钟**检测点的一致结论**，逻辑和参考规则（`deploy/rules/probes.yml`）相同：一个检测点连续 3 分钟测不通，才算它认为这个服务失败；至少两个检测点失败，并且不少于有数据的检测点的一半，服务就是故障；至少两个失败但不到一半，就是部分故障。单个检测点的抖动永远不会显示出来。

所以，只要还有关于某个服务的告警在触发，状态页就不会说它正常；检测点一致认定的故障，状态页会比告警早一两分钟显示出来，因为告警还要再等一分钟确认。由几个服务组成的组件，取其中最坏的状态。

**无数据**是指没有任何上报：最近几分钟里，这个组件的服务没有收到任何检测结果。常见原因有三个：没有检测目标带这个服务名；组件的 `page` 标签只出现在内部检测目标上；检测点连不上网关。[常见问题](../self-hosting/troubleshooting.md#a-status-page-shows-no-data)里逐一讲了怎么排查。

## 历史 {#history}

状态条的每一天，是检测点逐分钟的一致结论：由 VictoriaMetrics 按规则的逻辑在检测结果上直接计算，不需要录制规则；然后叠加事件（事件覆盖的分钟，至少是事件所说的状态）和维护窗口（维护中的分钟既不算可用，也不算不可用）。

| 一天显示为 | 条件 |
| --- | --- |
| 中断 | 检测点一致认为故障的时间至少两分钟，或者有中断事件涉及这一天 |
| 部分中断 | 故障或部分故障的时间至少两分钟，或者有部分中断事件涉及这一天 |
| 维护 | 当天有一部分在维护窗口里，窗口之外没有故障 |
| 正常 | 有检测结果，并且以上都不是 |
| 无数据 | 没有任何上报 |

可用率是：有检测结果、又不在维护中的那些分钟里，没有故障的分钟所占的比例；部分中断算作可用。每天从配置里 `timezone` 时区的零点开始。Beacon 把算好的日子放在内存里，启动时计算最近 90 天，每天一次查询，覆盖所有状态页的服务；今天每分钟重算一次，过去的某一天每 10 分钟重算一次，直到它结束六小时后为止，好收进检测点在网络中断期间排队、稍后才送到的结果。

## 事件 {#incidents}

事件是状态页服务上级别为严重或警告的告警组，而且告警名是这个状态页计入的（`alerts`；默认是 `ServiceDown`、`ServiceDegraded` 和 `MainlandPathDegraded`，即参考规则里的可用性告警，所以证书快到期不会变成公开的事件）。事件标题由 Beacon 按状态页的语言生成，内容是影响程度和涉及的组件，比如「服务中断：网站」或「部分中断：网站」；告警自己的文字可能提到内部的东西，永远不会显示。

事件的进展，新的在前：

- **发现**：事件开始时。中断显示「至少一半的检测点检测到失败。」，部分中断显示「部分检测点检测到失败。」；
- **处理中**：有人认领时，显示「我们正在处理。」；
- 人和 Agent 在告警组上留下的**备注**；
- **已恢复**：告警恢复时，显示「检测点检测到已恢复正常。」。

备注是写给处理告警的人看的，所以默认只公开以 `public:` 开头的备注，公开时去掉这个前缀；`notes: all` 的状态页公开所有备注。想告诉用户进展，就在控制台里、用 `add_note`，或者用 `POST /api/v1/alerts/{id}/notes` 加带 `alerts:write` 的令牌，留一条这样的备注：

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/alerts/0cdfbada62dd/notes \
  -d '{"text": "public: 大部分用户已经恢复访问，我们还在观察最后一个地区。"}'
```

通过 API 或 MCP 留下的备注标为 Agent 留的，在控制台或卡片上留下的标为人留的；不显示任何令牌名或人名。Beacon 把每个事件另存一份，保留 91 天，所以事件和它的页面比告警组留得久（已恢复的告警组 7 天后清理）。

## 维护 {#maintenance}

维护窗口是对某些服务的计划工作：状态页提前公告，维护期间把相关组件显示为维护，并在历史里标出这些天。用 `POST /api/v1/maintenance` 或 MCP 工具 `schedule_maintenance` 安排（作用域 `maintenance:write`）；用 `DELETE /api/v1/maintenance/{id}` 或 `end_maintenance` 提前结束或取消：

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/maintenance \
  -d '{"services": ["example.com"], "starts_at": "2026-11-03T02:00:00Z", "ends_at": "2026-11-03T03:00:00Z",
       "title": "Database upgrade", "description": "The site is read-only for a few minutes."}'
```

一个窗口最长 7 天，必须在 90 天内开始；开始时间在过去的，按现在算，所以窗口永远不会覆盖已经发生的事。它为每个服务建一条覆盖整个窗口的静默，说明写成 `maintenance: <标题>`，这样维护期间弄坏的东西不会叫醒任何人；窗口结束，这些静默也随之结束。安排和结束都记入审计日志，并回显到告警群。

用别的方式建的静默（`POST /api/v1/silences`、`beacon silence create`），只要说明以 `maintenance:` 开头，也显示为维护：显示在 `service` 匹配条件所匹配的服务对应的组件上，一直到静默结束，前提是 Beacon 还保留着这条静默。

## 自己的域名 {#a-host-of-its-own}

设了 `host` 的状态页，也放在那个域名的 `/` 上：事件在 `/incidents/<id>`，JSON 在 `/status.json`，订阅在 `/feed.atom`，徽章在 `/badge.svg` 和 `/components/<name>/badge.svg`。在这个域名上，Beacon 的其他部分都不应答：控制台、API 和 MCP 都没有，所以页面上也没有**登录**。把域名指向反向代理，并原样转发 Host。在参考部署里，取消 `deploy/Caddyfile` 末尾那段的注释，把 `uptime.example.com` 换成你的域名，再重启 Caddy 让它重新读取文件（在 `/opt/beacon` 里执行 `docker compose restart caddy`）：

```
uptime.example.com {
	import common
	reverse_proxy beacon:17001 {
		header_up Host {host}
		header_up X-Forwarded-For {client_ip}
	}
}
```

Caddy 会像为其他域名一样为它申请证书。Beacon 按请求的 `Host` 头决定显示哪个状态页，所以代理不能改写它。

## JSON、订阅和徽章 {#json-feed-and-badges}

| 路径 | 内容 |
| --- | --- |
| `/status/<name>.json`（默认状态页是 `/status.json`） | 状态页的状态：组件及其状态和 90 天历史，事件（先是进行中的，再是最近 30 天的）及其进展，维护窗口，链接。结构保持稳定：只增加字段，不修改也不删除 |
| `/status/<name>/feed.atom` | 事件和维护窗口的 Atom 订阅，每个事件带它的进展 |
| `/status/<name>/badge.svg` | 显示状态页状态的小徽章：正常、部分中断、中断、维护或无数据，用设计系统的颜色 |
| `/status/<name>/components/<component>/badge.svg` | 某一个组件的徽章 |

它们和状态页一样接受 `?lang=`，可以被其他网站嵌入和读取（`Access-Control-Allow-Origin: *`）。Agent 用 `GET /api/v1/status/<name>`（`status:read`）或 MCP 工具 `get_status_page` 拿到同样的 JSON。里面不含任何 service 标签和令牌：公开的只有状态页自己的文字。

## 品牌与署名 {#branding}

每个公开页面都写明自己运行在什么上：页脚有一行**由开源的 Beacon 驱动**，带 Beacon 的标志，链接到 [beacon.openlinkos.com](https://beacon.openlinkos.com/zh/)（中文页面链接到中文文档）和 [GitHub](https://github.com/openlinkos/beacon) 上的代码。希望状态页上只出现自己名字的团队，可以去掉这一行：

```yaml
branding:
  title: Example Operations   # 没有状态页时，/ 上那个页面的标题（不写就是 Beacon）
  powered_by: false           # 公开页面上不显示「由开源的 Beacon 驱动」（默认 true）
```

`powered_by` 只管公开页面；控制台总是显示 Beacon 的版本和项目的链接（[控制台](console.md)）。

一个状态页都没有时，`/` 是同样设计的一个小页面：这套部署的标题（`branding.title`）、一句话说明 Beacon 是什么、**登录**，以及页脚那一行。

## 缓存与搜索引擎 {#caching-and-search-engines}

一个状态页最多每 30 秒计算一次，每个应答都是公开、可缓存的：`Cache-Control: public, max-age=30` 加上 `ETag`，浏览器或边缘缓存再次请求时代价很小，会得到 `304 Not Modified`。已登录控制台的人请求的页面，页眉写的是**控制台**而不是**登录**，这样的应答是 `private, max-age=30`，共享缓存不会保存它；两者都通往 `/console`，所以缓存里写着**登录**的页面也无妨。和控制台的页面不同，状态页不标 `noindex`；`/robots.txt` 允许搜索引擎读取 `/`、`/status` 和它们加载的文件（`/static/`），不允许进入 `/console`、`/api/`、`/mcp`，以及控制台链接的旧路径（`/alerts/`、`/sessions/`）；状态页自己的域名上全部允许。状态页使用控制台的 `Content-Security-Policy`，但不允许任何脚本，不发送 referrer，也不能被嵌入框架。
