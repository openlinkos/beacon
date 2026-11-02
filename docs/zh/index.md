---
sourceHash: sha256-3e772f60
layout: home

hero:
  name: Beacon
  text: 给做运维的 AI Agent 用的基础设施
  tagline: 看得见服务对用户是否正常，够得着每一台机器而不把密钥交给 Agent，把值班的人和他们的 Agent 连在同一个平台上；一切按 Agent 已有的习惯来用。
  image:
    light: /logo.svg
    dark: /logo-dark.svg
    alt: Beacon
  actions:
    - theme: brand
      text: 30 分钟上线
      link: /zh/install
    - theme: alt
      text: 目标
      link: /zh/goals
    - theme: alt
      text: GitHub
      link: https://github.com/openlinkos/beacon

features:
  - title: 看得见
    details: 从用户所在的地方检测你的服务，并说清每次失败的原因。几个检测点都说坏了才算故障，一个点的抖动不会叫醒任何人；一次调用就把证据交到 Agent 手里。
    link: /zh/catalog
    linkText: 监控什么
  - title: 够得着
    details: Agent 用自己的 ssh、scp、rsync 和端口转发，或者 MCP 工具，到达每一台登记过的机器，NAT 后面的也行。Beacon 为每次连接签一张五分钟有效的证书，每次会话都有录制；没有一个 Agent 拿着密钥。
    link: /zh/ssh
    linkText: SSH 通道
  - title: 连得上
    details: 一个令牌通用 SSH、REST 和 MCP。新告警会唤醒 Agent，一条时间线串起告警、会话、命令和备注；人在告警群里看到这一切，能回放，也能一键暂停 Agent。
    link: /zh/agents
    linkText: Agent 与告警
---

## 工作原理 {#how-it-works}

一个服务 `linkos-beacon`，站在上报数据的机器、值班的人和干活的 Agent 之间。指标和规则仍在 VictoriaMetrics 和 vmalert 里；Beacon 不调用任何模型。

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
  <text class="bd-text" x="115" y="84" text-anchor="middle">各地的 linkos-probe</text>
  <rect class="bd-box" x="1" y="158" width="228" height="84" rx="12" />
  <text class="bd-title" x="115" y="194" text-anchor="middle">CI 与 Flux</text>
  <text class="bd-text" x="115" y="214" text-anchor="middle">部署、同步</text>
  <rect class="bd-box" x="1" y="288" width="228" height="84" rx="12" />
  <text class="bd-title" x="115" y="324" text-anchor="middle">VictoriaMetrics、vmalert</text>
  <text class="bd-text" x="115" y="344" text-anchor="middle">指标与规则</text>
  <rect class="bd-core" x="350" y="4" width="260" height="392" rx="16" />
  <text class="bd-name" x="480" y="40" text-anchor="middle">Beacon</text>
  <text class="bd-mono" x="480" y="60" text-anchor="middle">linkos-beacon serve</text>
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

网关给每台机器发各自的令牌，并给它写入的数据打标签，谁也冒充不了别人；它还给每个检测点下发检测目标。路由像 Alertmanager 一样给告警分组、抑制、静默。告警桥把告警组变成告警群里的一张卡片，没人认领就升级到手机，并用带签名的钩子唤醒 Agent。Agent 经 REST 或 MCP 取证据、留备注，经 SSH 通道到达机器。各部分怎样拼在一起，见[架构](/zh/architecture)。

## 安装 {#install}

在一台装了 Docker 的 Linux 机器上，三条命令就能启动中心：Beacon、VictoriaMetrics、vmalert 和 Caddy。

```bash
git clone https://github.com/openlinkos/beacon.git
sudo cp -r beacon/deploy /opt/beacon && cd /opt/beacon
sudo ./install.sh
```

然后登记一个检测点、一台机器和你的 Agent。[30 分钟上线](/zh/install)一步步讲了怎么做，最后演练一次故障，从告警一直走到恢复。

## 接入 Agent {#connect-an-agent}

一个令牌通用 API、MCP 和 SSH 通道，Agent 也不需要任何专门为 Beacon 写的东西：HTTP 上的 MCP、它自己的 ssh、shell。

```bash
claude mcp add --transport http linkos-beacon https://status.example.com/mcp --header "Authorization: Bearer lbt_…"
```

`list_machines` 会给它一段 `ssh_config`，之后它自己的 `ssh edge-1`、`scp` 和 `rsync` 都经通道走。被告警唤醒后，它先调 `get_evidence`，再到机器上处理，用 `probe_now` 验证，最后给值班的人留一条备注。见 [Agent 与告警](/zh/agents)和 [MCP 工具](/zh/reference/mcp)。

## Beacon 不是什么 {#what-beacon-is-not}

- **不是 Agent。**它不调用任何模型。它唤醒 Agent、回答它们、记录它们做了什么；拿主意是 Agent 的事。
- **没有审批流程。**每次会话人都看得到，也能回放；暂停开关可以一下子停住所有 Agent。
- **不做日志平台，也没有自己的时序数据库。**Agent 按它最熟悉的方式经 SSH 读日志；存储和查询交给 VictoriaMetrics。

[目标](/zh/goals)说明了 Beacon 为什么而做、不做什么，以及目标冲突时怎么取舍。Beacon 以 Apache License 2.0 开源，代码在 [GitHub](https://github.com/openlinkos/beacon)。
