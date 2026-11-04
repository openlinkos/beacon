---
sourceHash: sha256-b506902e
---

# Beacon 是什么

Beacon 为管理服务器的 AI Agent 提供自托管的监控和 SSH 接入。装在小机器上的检测程序，从用户访问服务的那些地方检测服务，并说清每次失败的原因：解析、连接、TLS、超时还是状态码。服务出问题时，Beacon 叫醒你的 AI Agent——Claude Code、Codex，或任何会用 MCP 或 HTTP 的 Agent——一次调用把证据交给它。Agent 经 Beacon 用它已经会用 `ssh`、`scp`、`rsync` 登上机器，全程不碰密码和密钥；执行的每条命令都有记录，能在浏览器里回放。值班的人在聊天里收到卡片，没人响应时手机会响；你的用户看到的是公开状态页。

## 为谁 {#who-it-is-for}

用 AI Agent 管理自己的机器和服务的人和小团队：从一个人带几个 Agent 管十几台机器，到一个分摊运维的小团队。Beacon 是自托管的——跑在你自己的硬件上，状态放在一个文件里，不调用任何模型，也不需要云账号。它检测什么、谁可以在上面动手，都由你说了算。

## 它解决的三个问题 {#the-three-problems-it-solves}

**Agent 不知道出事了。**监控工具给人发告警、画面板；Agent 要等有人把告警贴进对话框才知道出了事，还得去猜检测点看到了什么。Beacon 也把告警送给 Agent：签名的 webhook 或 GitHub workflow 叫醒它，一次 `get_evidence` 调用给出每个检测点看到了什么、为什么失败、故障前刚做过的部署，以及已经有谁做过什么。修好之后，`probe_now` 让所有检测点立刻重测，告警按检测的结果恢复，而不是按 Agent 的说法。

**要碰服务器，就得给它你的密钥。**通常的做法是把 SSH 私钥或 root 密码交给 Agent；从此它能从任何地方进这些凭据能开的每一台机器，执行了什么也没有记录。经 Beacon 的 SSH 通道，Agent 照旧用自己的 `ssh`、`scp`、`rsync`，但每次连接由 Beacon 签一张五分钟有效的证书，你的服务器只信任 Beacon。可以吊销一个 Agent，也可以一个开关停下所有 Agent。

**事后说不清发生了什么。**Agent 的命令在一个没人看的终端里滚过去，而它在会话之间什么都不记得。Beacon 录下每次会话，能在浏览器里回放；交互 shell 拆成一条条命令和退出码；Agent 的备注留在告警上。事后复查做了什么，而不是事前逐条审批。

## Beacon 不做什么 {#what-beacon-is-not}

- **Beacon 不是 Agent，也不调用任何模型。**它叫醒你的 Agent、把证据交给它；怎么处理是 Agent 的事。`beacon-oncall` 是那个小的接收程序：钩子触发时启动你指定的 Agent 程序。
- **不做审批流程。**暂停开关和每台机器的访问范围是仅有的刹车；复查在事后，由人看着记录做。
- **不做日志平台、链路追踪、APM，也不画面板。**Agent 要看日志，就通过 SSH 通道读，用 `journalctl` 这些它本来就会的工具。
- **不自己做时序数据库。**检测结果由 VictoriaMetrics 存储和查询，规则由 vmalert 计算。
- **不做远程桌面，也不做部署系统。**Flux 这类工具照常用，Beacon 接收它们的变更事件。
- **不写只有某一处部署才需要的东西。**某处部署特有的需求都用配置解决，放在本仓库之外。

完整的版本和其中的取舍，见[目标](../about/goals.md)。

## 三种运行形态 {#three-ways-to-run-it}

| 形态 | 用途 | 方式 |
| --- | --- | --- |
| [在本机试一试](quickstart.md) | 十分钟看完整闭环 | 一个 compose 文件启动 Beacon、VictoriaMetrics、vmalert、一个检测点和一台可弄坏再修好的演示机器；不要域名、不要证书，地址是 `http://localhost:17001` |
| [装在服务器上](../self-hosting/install.md) | 正式使用 | `deploy/install.sh` 在一台机器上（2 核 4 GB 足够）启动 Beacon、VictoriaMetrics、vmalert 和 Caddy，两个域名指向它 |
| [检测点和机器](../self-hosting/probes.md) | 扩展 | 一个不到 20 MB 的 `beacon-probe` 装在靠近你用户的任何小机器上；机器登记一次，直连到达，在 NAT 后面就走连接器 |

## 接下来 {#where-to-go-next}

- [在本机试一试](quickstart.md)：十分钟走完整个闭环，从服务弄坏到修好。
- [Beacon 如何工作](concepts.md)：一张图、一套术语，以及告警和 Agent 各自走过的路。
- [让 Agent 值班](on-call.md)：正式接上钩子、证据、备注和按需重测。
