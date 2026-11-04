---
sourceHash: sha256-cb3931a8
---

# Beacon 如何工作

这一页给出它的形状：一张图、一套指南其余部分都在用的术语，以及两条要紧的路——告警的路，从服务出故障到人和 Agent 知道这件事；Agent 的路，从被唤醒到在告警上留下备注。要查得更细，见[架构](../about/architecture.md)，那里讲得全。

## 全貌 {#the-whole-picture}

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
  <text class="bd-title" x="480" y="225" text-anchor="middle">桥</text>
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

图上有三股流向。结果往上走：每个检测点检测中心下发的目标，经网关推进 VictoriaMetrics。告警往回走：vmalert 计算规则，把触发的告警发给网关，路由把它们分组，桥通知人、叫醒 Agent。Agent 从外面进来：经 REST 和 MCP 取证据、写备注，经 SSH 通道上机器。

## 术语 {#the-words}

| 术语 | 含义 |
| --- | --- |
| 中心 | `beacon` 服务和运行它的机器。检测点把数据推给它，人和 Agent 登录它，VictoriaMetrics 和 vmalert 在它旁边。 |
| 检测点 | 一台跑着 `beacon-probe` 的小机器，离你的用户很近；检测中心下发的目标，把结果推回来。 |
| 机器 | 在 Beacon 登记过的服务器，Agent 经 SSH 通道到达：直连，或在 NAT 后面时经连接器。 |
| 成员 | 在这个 Beacon 上有自己的账号的人，有一个角色。 |
| 所有者 | Agent 所代表的那个人；Agent 能做的事绝不超出这个人的角色。 |
| 角色 | 一个人可以做什么。角色一级盖一级：查看者只看，值班还能处理告警、上机器，运维还能管理机器和维护，所有者还能改配置、管团队。 |
| Agent | 代表某个人行事的程序：Claude Code、Codex、CI 任务。在 SSH、REST 和 MCP 上是同一个身份；用 MCP 的 OAuth 或令牌登录。 |
| 预设 | 一个新 Agent 可以做什么，创建时选定——只读、值班或运维——始终在所有者角色的范围之内。 |
| SSH 通道 | Beacon 的 SSH 入口，17002 端口。Agent 自己的 `ssh`、`scp`、`rsync` 都从这里走；每次连接一张五分钟有效的证书；每次会话都录像。 |
| 证据 | `get_evidence` 一次调用返回的东西：每个检测点的结果和失败原因、同一故障域里的其他告警、故障前刚发生的变更，以及人和 Agent 已经做过的事。 |
| 会话 | 经通道到一台机器的一次访问：一个 shell、一条命令、一次文件传输、一个转发的端口。从头到尾都有记录。 |
| 回放 | 在浏览器里重放一次会话的录像，每条命令带着退出码。 |
| 控制台 | 成员登录的页面：告警及其证据和时间线、机器、会话及回放、成员和 Agent、配置及历史、审计日志。 |
| 状态页 | Beacon 自己提供的公开页面：组件、它们的状态和历史、事件、维护窗口。 |
| 目录 | Beacon 看什么，记在这里：检测点、心跳、到期资源、状态页。存在数据库里，每次修改都是一个版本。 |
| 心跳 | 一项必须按时报告成功的任务——备份、定时任务。迟了或一直没有报告，告警就触发。 |
| 静默 | 把匹配的告警压住一段时间：从卡片或命令行设置，写明原因和结束时间。 |
| 维护 | 对一些服务的计划内工作，提前在状态页公布；进行期间它覆盖的组件显示为维护中。 |
| 告警 | 规则发现问题时发出的东西，由 Beacon 分组、路由：变成聊天里的卡片，没人响应时打到手机，也变成钩子去叫醒 Agent。 |
| 演练 | 一次排练的故障：`beacon drill fail` 让 Beacon 自己的演练服务故意出错，你看整个闭环跑一遍——在里面的可以是你，也可以是 Agent。 |

## 一条告警走过的路 {#an-alerts-path}

每分钟，每个检测点把中心下发的目标检测一遍，连同每次失败的原因一起推送；VictoriaMetrics 存下这些结果。vmalert 每 30 秒计算一次规则——至少几个检测点都说坏才算服务故障，所以单个检测点网络不好不会吵醒任何人——把触发的告警发给 Beacon；Alertmanager 的活由 Beacon 自己干：告警分组（新的告警组先等半分钟，看还有没有同伴进来），抑制和静默压住它们匹配的。新的告警组变成聊天里的卡片；严重的没人认领，10 分钟后加急，15 分钟后打到手机。同一个告警组也会触发叫醒 Agent 的钩子，两者各走各的，通知从不等钩子。检测点重新看到服务正常时，规则引擎发现，告警恢复。

## Agent 走过的路 {#an-agents-path}

Agent 代表一个人行事，这个人是它的所有者；它能做的事绝不超出所有者的角色，每次请求都重新读取。被钩子叫醒，或被人叫来，它只登录一次——经浏览器用 MCP 的 OAuth，或用令牌——这个身份进得了每一扇门：REST 和 MCP 是同样的调用，SSH 到的是同样的机器。`get_evidence` 一次给全整件事；`probe_now` 让所有检测点立刻重测；`add_note`、`ack_alert`、`silence_alert` 是写操作，每一次都记入审计、回显到告警群。机器上的活经 SSH 通道：用它自己的 `ssh` 和 `scp`，或用 Beacon 的工具（`run_command`、`read_file`、`write_file`、`edit_file`）；每次连接一张五分钟有效的证书，每次会话一份录像。Agent 的访问一结束——凭据被吊销、所有者被降级——它开着的一切立即关闭；`beacon ssh pause` 则把所有人的一切都关掉。

## 再往深处读 {#where-to-read-more}

- [架构](../about/architecture.md)：每个部分讲透——什么跑在哪里、状态放在哪里、每个部分遵守的规则。
- [让 Agent 值班](on-call.md)和[给 Agent 一台机器](machines.md)：上面两条路，变成你亲手做的事。
