---
sourceHash: sha256-ef9b9440
---

# Beacon 是什么

Beacon 是给 AI Agent 用的自托管基础设施，由 Agent 和管理它们的人共用。Agent 经它拿到凭据、登上机器、看到监控，人也在这里做同样的事。双方做的每件事都有记录。

- **团队密码管理器**。人在这里保存、共享密码、密钥和令牌。Agent 凭授权使用，还可以只用不看。
- **Agent 的可信访问**。每个 Agent 都是一个身份，代表某个人行事，不超出这个人的角色。Claude Code 和 Codex 在浏览器里登录。
- **SSH 跳板机**。Agent 用自己本来就会的 `ssh`、`scp`、`rsync` 登上机器，不碰密码，也不碰密钥。
- **从用户所在的地方检测**。检测程序从多个地方检测每个服务，并说清失败的原因。告警叫醒你的 Agent，一次调用拿到证据。
- **也管 Agent 开发的服务**。Agent 经同一套 API，自己把开发的服务加进检测。
- **每个动作都有记录**。Agent 执行的每条命令都录下来，能在浏览器里回放。每次修改都追溯到是谁做的、代表谁、为什么。
- **人也在同一个地方**。值班的人在聊天里收到卡片，没人响应时手机会响。你的用户看到公开状态页。

密码管理器，以及服务自身指标的视图，正在开发中。Beacon 往哪里去，见[目标](../about/goals.md)。

## 为谁 {#who-it-is-for}

用 AI Agent 管理自己的机器和服务的人和团队：从一个人带几个 Agent 管十几台机器，到一个共用密码、分担工作的团队。

Beacon 跑在你自己的硬件上。它默认把状态放在一个 SQLite 文件里，不调用任何模型，也不需要云账号。它保管什么、检测什么、谁可以动手，都由你说了算。

## Agent 和人共用 {#shared-by-agents-and-people}

Agent 自己闭不了环。它能用什么由人决定，同样的活人自己也要做。所以 Beacon 的每一部分都同时为两边而做：对 Agent，按它已有的方式来用；对人，至少和人今天做这件事所用的工具一样好。

## 它解决的问题 {#the-problems-it-solves}

### 要调用服务，就得给它你的密码 {#to-call-a-service-it-needs-your-passwords}

要调一个 API、连一个数据库，Agent 需要令牌或密码。通常是有人把它贴进对话框。从那里它可能流进对话记录、提示词或代码仓库，事后也没人说得清是哪个 Agent 用了它、为什么用。

Beacon 像密码管理器那样保管团队的凭据。Agent 申请一次，拿到对某个密钥的授权，此后在授权范围内随用随取，每次使用都连同理由记录下来。给它授权的人，自己的密码也放在同一个地方。

### 要碰服务器，就得给它你的密钥 {#to-touch-a-server-it-needs-your-keys}

通常的做法是把 SSH 私钥或 root 密码交给 Agent。从此它能从任何地方进这些凭据能开的每一台机器，执行了什么也没有记录。

经 Beacon 的 SSH 通道，Agent 照旧用自己的 `ssh`、`scp`、`rsync`。每次连接由 Beacon 签一张五分钟有效的证书，你的服务器只信任 Beacon。可以吊销一个 Agent，也可以一个开关停下所有 Agent。

### Agent 不知道出事了 {#the-agent-does-not-know-that-something-broke}

监控工具给人发告警、画面板。Agent 要等有人把告警贴进对话框才知道出了事，还得去猜检测点看到了什么。

Beacon 也把告警送给 Agent：签名的 webhook 或 GitHub workflow 叫醒它。一次 `get_evidence` 调用，给出每个检测点看到了什么、为什么失败、故障前刚做过的部署，以及已经有谁做过什么。修好之后，`probe_now` 让所有检测点立刻重测。告警按检测结果恢复，而不是按 Agent 的说法。

### 事后说不清发生了什么 {#afterwards-nobody-can-tell-what-happened}

Agent 的命令在一个没人看的终端里滚过去，而它在会话之间什么都不记得。

Beacon 录下每次会话，能在浏览器里回放。交互 shell 拆成一条条命令和退出码，Agent 的备注留在告警上。事后复查做了什么，而不是事前逐条审批。

配置的每次修改也在同一份记录里：谁改的、代表谁、为什么。

## Beacon 不做什么 {#what-beacon-is-not}

- **不是 Agent，也不调用任何模型**。它叫醒你的 Agent、把证据交给它；怎么处理是 Agent 的事。`beacon-oncall` 是那个小的接收程序：钩子触发时启动你指定的 Agent 程序。
- **不审批 Agent 执行什么**。复查在事后，由人看着记录做。唯一要批的是凭据，每次授权批一次。机器上的刹车是暂停开关和每台机器的访问范围。
- **不做日志平台、链路追踪和 APM**。Agent 要看日志，就通过 SSH 通道读，用 `journalctl` 这些它本来就会的工具。
- **不自己做时序数据库**。检测结果由 VictoriaMetrics 存储和查询，规则由 vmalert 计算。
- **不做远程桌面，也不做部署系统**。Flux 这类工具照常用，Beacon 接收它们的变更事件。
- **不写只有某一处部署才需要的东西**。某处部署特有的需求都用配置解决，放在本仓库之外。

完整的版本和其中的取舍，见[目标](../about/goals.md)。

## 三种运行形态 {#three-ways-to-run-it}

| 在哪里 | 得到什么 | 从这里开始 |
| --- | --- | --- |
| 你的笔记本 | 十分钟走完整个闭环，不要域名、不要证书 | [在本机试一试](quickstart.md) |
| 一台服务器 | 正式使用：两个域名指向它；2 核 4 GB 足够 | [装在服务器上](../self-hosting/install.md) |
| 检测点 | 从靠近用户的地方检测，再加上 Agent 要干活的机器 | [检测点位置](../self-hosting/probes.md) |

三种形态的比较见[部署形态](../self-hosting/index.md)。

## 接下来 {#where-to-go-next}

按顺序读这两页：

1. [在本机试一试](quickstart.md)：十分钟走完整个闭环，从服务弄坏到修好。
2. [Beacon 如何工作](concepts.md)：一张图、一套术语，以及告警和 Agent 各自走过的路。

之后按你要做的事去找：

- [使用 Beacon](using-beacon.md)：每件事一页。
- [接入](../integrations/index.md)：各种 Agent 客户端，以及告警前后的钩子、变更事件和聊天。
- [自托管](../self-hosting/index.md)：把 Beacon 装到服务器上，并让它一直跑下去。
- [参考](../reference/index.md)：每个接口、工具、命令、配置项和作用域。
- [关于](../about/index.md)：目标、架构和更新日志。
