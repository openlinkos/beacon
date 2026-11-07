---
sourceHash: sha256-82714d3d
---

# 发布状态页

读完这一页，你的用户就有了一个页面，告诉他们你的服务状况如何。页面由 Beacon 用自己已有的数据生成，不需要人工更新。

页面上写着：

- 服务现在好不好用；
- 最近 90 天表现如何；
- 出了什么事，正在怎么处理；
- 计划做什么维护。

数据来自检测点的结果、告警组和维护窗口。检测点一致认为某服务中断，页面就写中断。告警恢复，事件也随之恢复。

页面由 Beacon 自己提供，读者无需登录：

- 默认页在 `/` 和 `/status`；
- 每一页在 `/status/<name>`；
- 给了独立域名的那一页，还出现在该域名的 `/` 上。

**你需要：**

- Beacon 已经运行，至少两个检测点在汇报（[检测一个服务](monitoring.md)）。页面呈现的是检测点的一致判断。
- 一个带 `config:write` 的凭据。

## 你已经有了的状态页 {#the-page-you-already-have}

参考部署自带一页，`status`，在 `deploy/catalog/status_pages.yaml` 里。标题是「Example Status」，有两个组件：

- **Website**：服务 `example.com`。
- **Status and alerts**：`beacon`，Beacon 自己的状态主机。

按[装在服务器上](../self-hosting/install.md)装好后，打开 `https://status.example.com/`。第一个检测点汇报之前，页面显示**暂无数据**。第一批结果到达一两分钟后，各组件显示状态。两个都正常时，整页显示**所有服务运行正常**。

从上到下，一个状态页依次是：

- **整体状态**一行：所有服务运行正常、有组件中断或部分中断、正在维护、暂无数据。
- **进行中的事件**及其进展；**维护**，进行中和已计划的。
- **组件**：各自当前的状态，加一条最近 90 天的长条（手机上只显示最近 30 天）。一格一天：正常、部分中断、中断、维护或无数据。每格的提示给出日期和当天的可用率。
- **最近 30 天**：已结束的事件和维护，各自链到自己的页面（`/status/<name>/incidents/<id>`）。
- **检测点**的公开名称，以及进入控制台的**登录**。

页面用的是家族的站点设计（`@openlinkos/ui`），可以只有英文、只有中文或两者都有。手机可读，深浅色跟随读者系统，不需要任何脚本。

把两个示例组件换成你自己的服务；或者删掉这个文件，不提供公开页。

## 定义你自己的页面 {#define-your-own-page}

页面属于目录，放在 `status_pages` 下，与检测点、心跳、资源并列。一个页面：

- 首次启动和 `beacon config import` 时从目录文件导入；
- 可以经 API、MCP 和命令行修改；
- 与整个配置一起检查；
- 是历史里的一个版本，可以回退。

```yaml
status_pages:
  - name: public                     # 小写字母、数字和连字符：/status/public
    title: Example Status            # 文本可以是一个字符串，也可以按语言各给一个：{en: …, zh: …}
    description: Whether Example's public services work, from where their users are.
    default: true                    # 在 / 和 /status 上的那一页（否则按名字排第一的）
    host: uptime.example.com         # 可选：在该域名的 / 上提供这页（Beacon 自己的 / 就不再是它）
    brand: beacon                    # 页面采用的家族品牌
    languages: [en, zh]              # 第一个是默认语言；不写是英文
    components:
      - name: website
        title: Website
        services: [example.com, www.example.com]   # 检测目标上的 service 标签
      - name: mirrors
        title: {en: Container mirrors}
        page: mirrors                # 所有带 page: mirrors 标签的目标所在的服务
    groups:                          # 可选：组件之上的小标题
      - title: Registries
        components: [mirrors]
    alerts: [ServiceDown, ServiceDegraded, MainlandPathDegraded]   # 算作事件的告警（默认就是这三条）
    notes: public                    # 哪些备注会发布：public（默认）或 all
    links:
      home: https://example.com/
      docs: https://docs.example.com/
      abuse: mailto:abuse@example.com
      privacy: https://example.com/privacy
```

也可以走 API，用一个带 `config:write` 的令牌写入页面：

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "https://status.example.com/api/v1/config/status_pages/public?note=the+public+page" \
  -d '{"title": "Example Status", "components": [{"name": "website", "title": "Website", "services": ["example.com"]}]}'
```

你会看到新的版本号和这一页的条目，改动也会回显到告警群。走 MCP 时，工具是 `get_config`、`put_config` 和 `delete_config`，带 `kind: status_pages`。

一个页面要通过这些检查才会被接受：

- 名字形式正确，且不重复；
- 有标题，至多 120 字符；描述至多 1000 字符；
- 至少一个组件；
- 每个组件由 `services` 或 `page` 标签支撑；
- 分组引用本页自己的组件，每个组件至多属于一组；
- 链接是 `https`、`http` 或 `mailto`；
- 域名不与其他页面重复；
- 默认页至多一个。

`menu` 给页面配上家族其他站点那种页头家族菜单，也是页脚的各栏。页面的每一个字段都在目录参考里：[目录](../reference/catalogue.md)。

### 组件与标签 {#components-and-labels}

组件由若干服务的检测目标支撑。它有两种方式点名这些服务：

- **`services`** 按目标的 `service` 标签点名，那是每个目标都有的标签（[目标](monitoring.md#targets)）。
- **`page`** 按另一个标签点名。`page: mirrors` 的组件，由所有带 `page: mirrors` 标签的目标所在的服务支撑。这样往目标里加一个镜像，页面就多一个镜像。

带 `visibility: internal` 标签的目标，永远不会经 `page` 支撑组件。

组件点名的服务一个目标都没有时，这个组件照样被接受，但带一条警告，因为目标常常在页面之后才加上。警告在改动的 `warnings` 里，`check-config` 也会给出。它是这样的：`status page "public": component "website" names service "www.example.com", which no probe target has; it shows no data until one does`。

### 语言 {#languages}

每段文本（`title`、`description`、分组的或链接的 `title`）可以是一个字符串，也可以按语言各给一个：`{en: Status, zh: …}`。两种语言的页面在页头有语言菜单，页面和链接接受 `?lang=zh`。

不带 `?lang=` 时，读者看到的页面语言是：

- 浏览器要的语言（`Accept-Language`）里，页面有的第一个；
- 浏览器两种都没要时（搜索引擎通常如此），页面的第一个语言。

JSON、订阅源和徽章是给程序读的。除非 `?lang=` 另有指定，它们都用第一个语言。

## 页面说什么，依据是什么 {#what-the-page-says-and-why}

组件当前的状态取以下几项里最坏的：

- **维护**：维护窗口正覆盖它的某个服务；
- 其服务上**正在告警的事件**：`ServiceDown` 算中断，其余告警算部分中断；
- 最近几分钟**检测点的一致判断**，按参考规则的逻辑（`deploy/rules/probes.yml`）。

检测点的一致判断是这样算的：

- 一个检测点连续失败 3 分钟，才算它失败。
- 至少两个检测点失败，且不少于汇报数的一半，服务算**中断**。
- 至少两个失败但不到一半，算**部分中断**。
- 单个检测点的抖动永远不会显示。

所以只要关于某服务的告警还在，页面就不会说它正常。检测点一致看到的故障，页面会比告警早一两分钟显示，因为告警还要再等一分钟确认。多个服务的组件，取其中最坏的。

什么都没有汇报是**暂无数据**：最近几分钟没有任何检测点给该组件的服务发过结果。原因和处理见[状态页显示无数据](../self-hosting/troubleshooting.md#a-status-page-shows-no-data)。

长条里的每一天，是同样的一致判断逐分钟算出的，再叠上事件和维护窗口。可用率是有结果的分钟里（维护除外）不中断的占比。

按配置的 `timezone` 零点分日。Beacon 计算：

- 最近 90 天，在启动时；
- 今天，每分钟重算一次；
- 过去的一天，每 10 分钟重算一次，直到其结束六小时后，以补上检测点断路期间排队的结果。

## 事件与进展 {#incidents-and-updates}

事件是页面服务上的一个告警组。它是 critical 或 warning，其告警在页面计入之列（`alerts`）。

默认计入 `ServiceDown`、`ServiceDegraded` 和 `MainlandPathDegraded`，即参考规则里的可用性告警。所以一张快过期的证书不会变成公开事件。

事件的标题由 Beacon 按页面语言、从影响和涉及的组件生成，例如「服务中断：Website」。告警自己的文字可能提到内部事物，永远不会显示。

每个事件 Beacon 保留一份副本 91 天，所以事件和它的页面比告警组活得久。已恢复的告警组只留 7 天。

事件的进展，最新的在前：

- **发现**：开始时间。
- **处理中**：有人确认（ack）时。
- 成员和 Agent 留在告警组上的**备注**。
- **已恢复**：告警恢复时。

备注是写给处理告警的人看的。所以默认只发布以 `public:` 开头的备注，去掉前缀之后发布。`notes: all` 的页面发布所有备注。

要告诉用户进展，从控制台、用 `add_note`，或用一个带 `alerts:write` 的令牌留这样一条备注：

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/alerts/0cdfbada62dd/notes \
  -d '{"text": "public: The site is back for most users; we are watching the last region."}'
```

## 维护 {#maintenance}

维护窗口是对某些服务的计划内工作。页面提前公告，进行期间把相关组件显示为维护中，并把那些天标出来。

| 要做的事 | REST | MCP 工具 |
| --- | --- | --- |
| 安排一个窗口 | `POST /api/v1/maintenance` | `schedule_maintenance` |
| 提前结束或整个取消 | `DELETE /api/v1/maintenance/{id}` | `end_maintenance` |

两者都需要作用域 `maintenance:write`。安排一个窗口：

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/maintenance \
  -d '{"services": ["example.com"], "starts_at": "2026-11-03T02:00:00Z", "ends_at": "2026-11-03T03:00:00Z",
       "title": "Database upgrade", "description": "The site is read-only for a few minutes."}'
```

一个窗口至多 7 天，至多提前 90 天。开始时间在过去就按现在算，窗口不会覆盖已经发生的事。

窗口会为每个服务建一条同样时长的静默，备注是 `maintenance: <标题>`。这样施工碰坏的东西不会吵醒任何人。窗口结束，静默也结束。

用其他方式建的静默（`POST /api/v1/silences`、`beacon silence create`），只要备注以 `maintenance:` 开头，也会显示为维护。安排和结束都记入审计日志并回显到告警群。

## 独立域名 {#a-host-of-its-own}

带 `host` 的页面，还在该域名的 `/` 上提供。JSON 在 `/status.json`，订阅在 `/feed.atom`，徽章在 `/badge.svg`。

Beacon 的其他一切都不在这个域名上应答：控制台、API、MCP 都不是。所以页面在那里没有**登录**。

这一页从此住在它自己的域名上。它是默认页时，Beacon 自己的根路径不再是它，而是落地页。落地页通往 Beacon 提供的每一个状态页和控制台。于是 Beacon 的域名归控制台，页面的域名归页面。

在 Beacon 的域名上，`/status/<name>` 照样能打开这一页，页面声明的规范地址是它自己的域名。它的 JSON、订阅和徽章在那里也照常应答，已经发出去的链接不会失效。

在参考部署里：

1. **打开 `deploy/Caddyfile` 末尾那段注释**，把 `uptime.example.com` 换成你的域名。Beacon 按请求的 `Host` 头决定提供哪一页，所以反向代理不能改写它：

   ```
   uptime.example.com {
   	import common
   	reverse_proxy beacon:17001 {
   		header_up Host {host}
   		header_up X-Forwarded-For {client_ip}
   	}
   }
   ```

2. **重启 Caddy**，让它重读配置：在 `/opt/beacon` 下执行 `docker compose restart caddy`。Caddy 会像其他域名一样自动取得证书。

## JSON、订阅与徽章 {#json-feed-and-badges}

| 路径 | 内容 |
| --- | --- |
| `/status/<name>.json`（默认页是 `/status.json`） | 页面的状态：各组件的当前状态和 90 天历史、事件及其进展、维护窗口、链接。格式是稳定的：只加字段，不改不删 |
| `/status/<name>/feed.atom` | 事件和维护窗口的 Atom 订阅 |
| `/status/<name>/badge.svg` 和 `/status/<name>/components/<component>/badge.svg` | 一枚徽章，页面或单个组件的状态，用设计系统的配色 |

它们与页面一样接受 `?lang=`。其他站点可以嵌入、取用它们（`Access-Control-Allow-Origin: *`）。

Agent 拿同一份 JSON，走 `GET /api/v1/status/<name>`（`status:read`）或 MCP 工具 `get_status_page`。里面没有服务标签，也没有令牌：公开的只有页面自己的文字。

## 品牌标识 {#branding}

每个公开页面都说明自己跑在什么上。页脚有一行**Powered by Beacon, open source**，链到 [beacon.openlinkos.com](https://beacon.openlinkos.com/) 和 [GitHub](https://github.com/openlinkos/beacon) 上的源码。想让状态页只挂自己团队名字的，可以去掉这一行：

```yaml
branding:
  title: Example Operations   # 没有状态页时 / 上的那一页（不写是 Beacon）
  powered_by: false           # 公开页面不再显示 "Powered by Beacon"（默认 true）
```

`powered_by` 只影响公开页面。控制台始终显示 Beacon 的版本和项目链接（[复查 Agent 做了什么](console.md)）。

一个页面至多每 30 秒计算一次。每个应答都是公开可缓存的（`Cache-Control: public, max-age=30` 加 `ETag`），浏览器或边缘缓存可以便宜地再问一次，拿到 `304 Not Modified`。登录者看到的页面是 `private, max-age=30`，不会进任何共享缓存。与控制台页面不同，状态页不带 `noindex`。

## 检查一下 {#check-it}

1. **读一个组件的长条。** 检测点开始汇报后，打开页面。最近这些天每格都应是正常，格子的提示里有当天的可用率。
2. **把演练服务加进你的页面**：一个 `services: [drill]` 的组件，自带的页面并没有它。
3. **[演练一次故障](drills.md)。** 演练告警触发期间，组件显示**中断**，下面挂着事件。
4. **给告警组留一条 `public:` 开头的备注。** 看它出现在事件的进展里。
5. **结束演练。** 看两者一起消失，再把这个组件删掉。

如果组件显示**暂无数据**，见[状态页显示无数据](../self-hosting/troubleshooting.md#a-status-page-shows-no-data)。

## 下一步 {#next}

- [演练一次故障](drills.md)：在 Beacon 自己的演练服务上，抢在用户之前看页面变化。
- [邀请你的团队](people.md)：页面进展背后的成员和角色。
- [复查 Agent 做了什么](console.md)：同样的事件，加上背后的会话和备注，给团队看。
