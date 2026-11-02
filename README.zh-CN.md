<h1>
  <img alt="Beacon · 烽火" src="https://openlinkos.com/brand/beacon/github/readme.svg" height="56">
</h1>

**为管理服务器的 AI Agent 打造的自托管监控与 SSH 接入。**

[![CI](https://github.com/openlinkos/beacon/actions/workflows/ci.yml/badge.svg)](https://github.com/openlinkos/beacon/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/openlinkos/beacon?include_prereleases&sort=semver)](https://github.com/openlinkos/beacon/releases)
[![Licence: Apache-2.0](https://img.shields.io/badge/licence-Apache--2.0-blue.svg)](LICENSE)

[English](README.md) | 简体中文 · 文档：[beacon.openlinkos.com/zh/](https://beacon.openlinkos.com/zh/)

Beacon 从多个地方同时检测你的网站和服务，走的是用户访问时的那条路，并说清每次检测失败的原因：解析、连接、TLS、超时还是状态码。服务出问题时，它叫醒你的 AI Agent（Claude Code、Codex，或任何会用 MCP 或 HTTP 的 Agent），一次调用就把证据交给它：每个检测点看到了什么、故障前刚改过什么、已经有谁做过什么。Agent 经 Beacon 用自己的 `ssh` 登上服务器，或者用 Beacon 提供的执行命令、改文件的工具，把能修的修好，让各检测点立刻重测，再在告警上留一条备注。整个过程它不接触任何密码和私钥，执行的每一条命令都有记录，能在浏览器里回放。用户看状态页；值班的人在飞书里收到卡片，没人响应时手机会响。

> **状态：早期（0.3，预发布版）。** 检测、告警路由、飞书卡片和手机升级、带录像的 SSH 通道、唤醒 Agent、证据、按需重测、控制台和公开状态页都已打通，做它的人自己的服务器就由它管着。1.0 之前，小版本之间配置可能变化，[更新日志](CHANGELOG.md)会写清楚怎么改。

## 为什么

AI 编程 Agent 已经很会做运维活：读日志、找出配置里错的那一行、重启该重启的服务。让人不敢放手的，是活儿以外的那些事：

- **Agent 不知道出事了。**监控工具给人发告警、画面板。Agent 要等有人把告警贴进对话框才知道出了事，还得去猜检测点看到了什么。
- **要碰服务器，就得给它你的密钥。**通常的做法是把 SSH 私钥或 root 密码交给 Agent。从此它能从任何地方进这些密钥能开的每一台机器，执行了什么没有任何记录。
- **事后说不清发生了什么。**Agent 的命令在一个没人看的终端里滚过去了。想复盘只能问 Agent，而它在会话之间什么都不记得。

Beacon 的设计是让 Agent 干活，让人看得见全部：

- **从用户所在的地方检测。**一个不到 20 MB 的检测程序，装在任何小机器上，检测 HTTP、TLS、TCP、镜像仓库、Git 和域名到期，并记下每次失败的原因。只有多个检测点都说坏了才算故障，单个检测点网络不好不会吵醒任何人。
- **叫醒 Agent 时证据一并给齐。**签名的 webhook 或 GitHub workflow 叫醒你指定的 Agent。一次 `get_evidence` 调用返回每个检测点的结果、同一故障域里的其他告警、故障前刚做的部署，以及人和 Agent 已经做过的事。
- **上服务器不用交出密钥。**Agent 经 Beacon 用自己的 `ssh`、`scp`、`rsync`，或者用 MCP 工具。每次连接 Beacon 签一张五分钟有效的证书，服务器只信任 Beacon。可以单独吊销一个 Agent，也可以一个开关停下所有 Agent。
- **每条命令都有记录。**会话录下来能在浏览器里回放，交互 shell 拆成一条条命令和退出码，Agent 的备注写在告警上。事后复查做了什么，而不是事前逐条审批。
- **修好要再测一遍。**修完 Agent 让所有检测点立刻重测（`probe_now`），一分钟内看到各自的结果。
- **人一直在场。**给用户看的公开状态页，给团队用的控制台，能在飞书卡片上直接认领和静默的告警，没人响应时经 Pushover 升级到手机。
- **小，而且是你自己的。**一个 Go 服务、一个检测程序，跑在 VictoriaMetrics 上；2 核 4 GB 的机器绰绰有余。Apache-2.0 许可。Beacon 不调用任何模型，也不需要任何云账号。

## 功能

- **MCP 服务器**（`/mcp`），38 个工具：告警（`list_alerts`、`get_alert`、`ack_alert`、`silence_alert` 等）、处理告警（`get_evidence`、`get_timeline`、`add_note`、`list_changes`、`probe_now`）、机器（`list_machines`、`run_command`、`read_file`、`write_file`、`edit_file`、`list_sessions`、`get_session` 等）、维护和状态页、配置，以及 Agent 自己的令牌（`whoami`、`renew_token`）。每个工具在 `/api/v1` 下都有对应的 REST 接口。
- **SSH 通道**（17002 端口）：`ssh`、`scp`、`sftp`、`rsync` 和 `ssh -L` 到任何登记过的机器，用户名就是机器名；NAT 后面的机器用普通的 `ssh -R` 自己连出来。
- **检测程序**（`beacon-probe`）：和 Blackbox Exporter 兼容的检测并给出失败原因，主机指标和心跳，断网时先存进磁盘队列；检测目标和模块由中心下发，改检测内容不用重新部署。
- **告警路由**，不需要 Alertmanager：分组、抑制、静默和死信开关都在服务里；vmalert 像发给 Alertmanager 一样把告警发给 Beacon。
- **飞书卡片**原地更新：什么坏了、从什么时候、哪些检测点失败及原因、Agent 的备注；卡片上的按钮可以认领和静默；没人响应时升级到飞书加急和 Pushover 紧急通知。
- **唤醒 Agent**：新告警组触发签名 webhook 或 GitHub workflow；还有 `beacon-oncall`，一个小的接收程序，验证 webhook 后启动你指定的 Agent 程序（附 Claude Code 和 Codex 的包装脚本）。
- **公开状态页**由 Beacon 自己提供：组件来自检测目标，90 天历史，事件来自告警，维护窗口，JSON、Atom 订阅和徽章。
- **控制台**（`/console`；站点根路径是公开状态页）：告警及其证据和时间线、机器、每次会话及回放、Agent 的令牌、配置及历史、审计日志。
- **在 Beacon 里配置**：检测点、检测目标、模块和状态页存在它的数据库里，经 API、MCP 或命令行修改，每次修改都是一个能撤销的版本；文件只用于导入导出，所以仍然可以放在 git 里。

## 支持哪些 Agent

| Agent | 接入方式 | Beacon 能否叫醒它 |
|---|---|---|
| Claude Code | HTTP 上的 MCP，它自己的 `ssh` | ✅ 用 `beacon-oncall`（无界面模式）或 GitHub workflow |
| Codex CLI | HTTP 上的 MCP，它自己的 `ssh` | ✅ 用 `beacon-oncall` |
| 任何 MCP 客户端 | HTTP 上的 MCP | ✅ 用 webhook |
| Bot 和脚本 | REST 和 `ssh` | ✅ 用 webhook |

Beacon 不调用模型：告警该怎么处理由 Agent 决定，哪个 Agent 都可以用。见[让 Agent 值班](https://beacon.openlinkos.com/zh/agents)。

## 快速开始

**需要：**一台 Linux 机器（2 核、4 GB 内存），装好 Docker 和 Compose v2，两个指向它的域名。

```bash
git clone https://github.com/openlinkos/beacon.git
sudo cp -r beacon/deploy /opt/beacon && cd /opt/beacon
sudo ./install.sh        # 会问两个域名和一个用于申请证书的邮箱
```

`install.sh` 生成密钥、校验所有文件、启动 Beacon、VictoriaMetrics、vmalert 和 Caddy，最后告诉你下一步做什么。[30 分钟装好](https://beacon.openlinkos.com/zh/install)带你走完剩下的：第一个检测点、第一台机器、第一个 Agent，以及一次从告警到恢复的故障演练。

### 接入 Claude Code

```bash
docker exec beacon beacon token create claude-code -scopes status:read,alerts:write,machines:ssh -expires 90d
claude mcp add --scope user --transport http beacon https://status.example.com/mcp \
  --header "Authorization: Bearer lbt_…"
```

然后在任意会话里问：*「现在有什么在报错吗？看一下证据，上机器查一查，告诉我你打算怎么处理。」* Agent 会根据 `get_evidence` 和机器上的实际情况回答，它执行过的一切都在控制台的「会话」里。

## 文档

文档在 [`docs/`](docs/)，发布在 [beacon.openlinkos.com/zh/](https://beacon.openlinkos.com/zh/)：

- [30 分钟装好](docs/zh/install.md)：从一台空机器到第一个检测点、第一台机器、第一个 Agent 和第一次演练
- [让 Agent 值班](docs/zh/agents.md)：叫醒 Agent、证据、备注、时间线、按需重测、`beacon-oncall`
- [SSH 通道](docs/zh/ssh.md)：Agent 不拿密钥怎么上机器、录像、NAT 后面的机器
- [目录、检测目标和模块](docs/zh/catalog.md)：检测什么、从哪里检测、怎么配置
- [状态页](docs/zh/status.md)：公开页、事件、维护、徽章
- [控制台](docs/zh/pages.md)：人看到什么，以及卡片打开的链接
- [飞书](docs/zh/feishu.md)：应用、卡片、群机器人
- [API](docs/zh/reference/api.md) 和 [MCP 工具](docs/zh/reference/mcp.md)：每个接口和工具，以及作用域和错误码
- [架构](docs/zh/architecture.md)和[目标](docs/zh/goals.md)：各部分怎么拼在一起，为什么做、不做什么
- [更新日志](CHANGELOG.md)

## 工作原理

```
beacon-probe（各检测点）──结果──▶ 网关 ──▶ VictoriaMetrics ◀── vmalert（规则）
CI、Flux ──变更事件──▶ 网关                                        │ 告警
                                                                   ▼
人 ◀── 飞书卡片、手机、状态页 ◀── beacon（路由、桥）──▶ Agent 的 webhook
Agent ──▶ MCP 和 REST（证据、备注、probe_now）──▶ beacon
Agent ──▶ ssh、scp、rsync（17002 端口）──▶ SSH 通道 ──▶ 你的服务器（五分钟证书，每次会话都录像）
```

一个服务 `beacon` 包括给机器用的网关、告警路由、通往人和 Agent 的桥、API 和 MCP、控制台和状态页，以及 SSH 通道；状态存在一个 bbolt 文件里，会话录像放在旁边。指标和规则交给 VictoriaMetrics 和 vmalert。各部分见[架构](docs/zh/architecture.md)。

## 安全

Agent 从不接触密码和私钥：SSH 通道每次连接签一张短期证书，机器只信任它的 CA。令牌只存 SHA-256 哈希，有作用域、会过期；每个检测点和主机只能以自己的身份写入。Agent 做的每件事都记入审计并回显到告警群。密钥只从文件读取，从不写进日志。漏洞报告方式见 [SECURITY.md](SECURITY.md)。

## 参与贡献

欢迎提 issue 和 pull request；[CONTRIBUTING.md](CONTRIBUTING.md) 说明怎么构建、测试和提交改动，[AGENTS.md](AGENTS.md) 是给参与开发的 AI Agent 看的说明。

## 许可

[Apache License 2.0](LICENSE)。LinkOS 和 OpenLinkOS 的名称和标志不在此许可范围内。

Beacon 的名字来自长城的烽火台：边关哨所发现敌情就点起烽火，信号一台接一台传到都城，烽火的数量表示敌情的轻重。
