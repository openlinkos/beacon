---
sourceHash: sha256-7e916cd9
layout: home

hero:
  name: Beacon
  text: 让 AI Agent 来值班
  tagline: 服务出问题时，Beacon 叫醒你的 Agent，把证据交到它手上，让它不拿一把密钥就能上机器处理，整个过程有录像可以回放。人只需要看着，不用爬起来。
  image:
    light: /logo.svg
    dark: /logo-dark.svg
    alt: Beacon
  actions:
    - theme: brand
      text: 30 分钟装好
      link: /zh/install
    - theme: alt
      text: 目标
      link: /zh/goals
    - theme: alt
      text: GitHub
      link: https://github.com/openlinkos/beacon

features:
  - title: 叫醒 Agent，证据一次给齐
    details: 严重告警触发时 Beacon 调用一个 webhook。Agent 问一句，就拿到每个检测点看到的结果和失败原因、同一故障域里的其他告警、故障前刚做的变更、已经做过的事。没有面板要盯。
    link: /zh/agents
    linkText: Agent 与告警
  - title: 密钥从不离开 Beacon
    details: Agent 用自己的 ssh、scp、rsync，或者 MCP 工具，经 Beacon 的通道到机器上。每次连接签一张五分钟有效的证书，机器只信任 Beacon 的 CA。Agent 手里没有任何值得偷的东西。
    link: /zh/ssh
    linkText: SSH 通道
  - title: 每条命令都有记录
    details: 会话录下来，在浏览器里回放；交互 shell 拆成一条条命令和退出码；Agent 的备注写回告警卡片。Agent 做错了什么你看得见，一个开关能让所有 Agent 立刻停下。
    link: /zh/pages
    linkText: 控制台
---

## 为什么做它 {#why-we-built-it}

运维工具默认键盘前坐着一个人：有面板给人看，密钥放在人的电脑上，有按钮等人来批准。把 Agent 放到第一线，这三样每一样都是漏洞。Agent 不看面板，得有东西叫醒它、把需要的东西一并交给它。不能把密钥交给它，它读到什么就可能泄露什么。凌晨三点它也等不到谁来批准，审核只能事后做，前提是全程有记录。

Beacon 就是为此而做的那一环：把 Agent 当作操作者来设计的基础设施，人负责看着，随时能停。我们自己的服务器就跑在它上面。第一次真实演练，一个停掉的作业从告警触发到验证恢复用了不到四分钟，没有人碰键盘。

## 工作原理 {#how-it-works}

一个服务 `beacon`，站在上报的机器、值班的人和干活的 Agent 之间。指标和规则交给 VictoriaMetrics 和 vmalert；Beacon 不调用任何模型。

<div class="bd-wrap">
<svg class="bd" viewBox="0 0 960 400" role="img" aria-labelledby="bd-title">
  <title id="bd-title">Beacon 的位置：检测点、CI 和 Flux 把数据报给 Beacon，指标和规则放在 VictoriaMetrics 与 vmalert 里；Beacon 把卡片和电话送到值班的人，唤醒 Agent，并让它们经 SSH 通道到达机器。</title>
  <defs>
    <marker id="bd-arrow" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="10" markerHeight="10" markerUnits="userSpaceOnUse" orient="auto">
      <path class="bd-line" d="M2 1.5 L8 5 L2 8.5" />
    </marker>
  </defs>
  <rect class="bd-box" x="1" y="28" width="228" height="84" rx="12" />
  <text class="bd-title" x="115" y="64" text-anchor="middle">检测点</text>
  <text class="bd-text" x="115" y="84" text-anchor="middle">各地的 beacon-probe</text>
  <rect class="bd-box" x="1" y="158" width="228" height="84" rx="12" />
  <text class="bd-title" x="115" y="194" text-anchor="middle">CI 与 Flux</text>
  <text class="bd-text" x="115" y="214" text-anchor="middle">部署、同步</text>
  <rect class="bd-box" x="1" y="288" width="228" height="84" rx="12" />
  <text class="bd-title" x="115" y="324" text-anchor="middle">VictoriaMetrics、vmalert</text>
  <text class="bd-text" x="115" y="344" text-anchor="middle">指标与规则</text>
  <rect class="bd-core" x="350" y="4" width="260" height="392" rx="16" />
  <text class="bd-name" x="480" y="40" text-anchor="middle">Beacon</text>
  <text class="bd-mono" x="480" y="60" text-anchor="middle">beacon serve</text>
  <rect class="bd-part" x="370" y="80" width="220" height="50" rx="10" />
  <text class="bd-title" x="480" y="101" text-anchor="middle">网关</text>
  <text class="bd-text" x="480" y="118" text-anchor="middle">给机器用 · 17006 端口</text>
  <rect class="bd-part" x="370" y="142" width="220" height="50" rx="10" />
  <text class="bd-title" x="480" y="163" text-anchor="middle">路由</text>
  <text class="bd-text" x="480" y="180" text-anchor="middle">分组、抑制、静默</text>
  <rect class="bd-part" x="370" y="204" width="220" height="50" rx="10" />
  <text class="bd-title" x="480" y="225" text-anchor="middle">告警桥</text>
  <text class="bd-text" x="480" y="242" text-anchor="middle">卡片、升级、钩子</text>
  <rect class="bd-part" x="370" y="266" width="220" height="50" rx="10" />
  <text class="bd-title" x="480" y="287" text-anchor="middle">API 与 MCP</text>
  <text class="bd-text" x="480" y="304" text-anchor="middle">给人和 Agent 用 · 17001 端口</text>
  <rect class="bd-part" x="370" y="328" width="220" height="50" rx="10" />
  <text class="bd-title" x="480" y="349" text-anchor="middle">SSH 通道</text>
  <text class="bd-text" x="480" y="366" text-anchor="middle">每次会话都录制 · 17002 端口</text>
  <rect class="bd-box" x="731" y="28" width="228" height="84" rx="12" />
  <text class="bd-title" x="845" y="64" text-anchor="middle">值班的人</text>
  <text class="bd-text" x="845" y="84" text-anchor="middle">告警群里的卡片、手机</text>
  <rect class="bd-box" x="731" y="158" width="228" height="84" rx="12" />
  <text class="bd-title" x="845" y="194" text-anchor="middle">Agent</text>
  <text class="bd-text" x="845" y="214" text-anchor="middle">Claude Code、Codex……</text>
  <rect class="bd-box" x="731" y="288" width="228" height="84" rx="12" />
  <text class="bd-title" x="845" y="324" text-anchor="middle">机器</text>
  <text class="bd-text" x="845" y="344" text-anchor="middle">五分钟有效的证书，NAT 后面也行</text>
  <path class="bd-line" d="M238 62 H342" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="290" y="54" text-anchor="middle">检测结果</text>
  <path class="bd-line" d="M342 78 H238" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="290" y="94" text-anchor="middle">检测目标</text>
  <path class="bd-line" d="M238 200 H342" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="290" y="192" text-anchor="middle">变更事件</text>
  <path class="bd-line" d="M342 322 H238" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="290" y="314" text-anchor="middle">样本</text>
  <path class="bd-line" d="M238 338 H342" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="290" y="354" text-anchor="middle">告警</text>
  <path class="bd-line" d="M618 62 H722" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="670" y="54" text-anchor="middle">卡片、电话</text>
  <path class="bd-line" d="M722 78 H618" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="670" y="94" text-anchor="middle">认领、静默</text>
  <path class="bd-line" d="M618 192 H722" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="670" y="184" text-anchor="middle">钩子唤醒</text>
  <path class="bd-line" d="M722 208 H618" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="670" y="224" text-anchor="middle">REST、MCP、ssh</text>
  <path class="bd-line" d="M618 330 H722" marker-end="url(#bd-arrow)" />
  <text class="bd-text" x="670" y="322" text-anchor="middle">ssh</text>
</svg>
</div>

网关给每台机器各自的令牌，并给它写入的数据打标签，谁也冒充不了谁；检测目标也由网关下发。路由做 Alertmanager 做的事：分组、抑制、静默。桥把告警组变成群里的卡片，没人认领就升级到手机，并用签名的 hook 叫醒 Agent。Agent 经 REST 或 MCP 拿证据、留备注，经 SSH 通道上机器。各部分怎么拼见[架构](/zh/architecture)。

## 安装 {#install}

一台装了 Docker 的 Linux 机器，三条命令起中心：Beacon、VictoriaMetrics、vmalert 和 Caddy。

```bash
git clone https://github.com/openlinkos/beacon.git
sudo cp -r beacon/deploy /opt/beacon && cd /opt/beacon
sudo ./install.sh
```

然后登记一个检测点、一台机器和你的 Agent。[30 分钟装好](/zh/install)一步步带你做完，最后演练一次故障从告警到恢复。

## 接入 Agent {#connect-an-agent}

一个令牌通用 API、MCP 和 SSH 通道，Agent 不需要读任何为 Beacon 写的东西：HTTP 上的 MCP、它自己的 ssh、shell。

```bash
claude mcp add --transport http beacon https://status.example.com/mcp --header "Authorization: Bearer lbt_…"
```

`list_machines` 给它一段 `ssh_config`，之后它自己的 `ssh edge-1`、`scp`、`rsync` 都走通道。被告警叫醒后，它从 `get_evidence` 开始，在机器上处理，用 `probe_now` 验证，给值班的人留一条备注。见[Agent 与告警](/zh/agents)和 [MCP 工具](/zh/reference/mcp)。

## Beacon 不是什么 {#what-beacon-is-not}

- **不是 Agent。**不调用模型。它叫醒 Agent、回答它的问题、记录它做的事；拿主意的是 Agent。
- **没有审批流程。**人看得见每次会话、能回放；暂停开关能立刻停下所有 Agent。
- **不做日志平台，也不写自己的时序数据库。**Agent 用它最熟的方式经 SSH 读日志；VictoriaMetrics 负责存储和查询。

[目标](/zh/goals)写了 Beacon 为什么而做、不做什么、目标冲突时怎么取舍。Beacon 以 Apache License 2.0 开源，在 [GitHub](https://github.com/openlinkos/beacon)。
