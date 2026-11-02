---
sourceHash: sha256-255c4e76
---

# 30 分钟装好

这篇指南带你从一台空的 Linux 机器开始，搭起一套能值班的系统。做完之后：Beacon 跑在一台机器上，你是它的所有者；两个检测点在检测你的服务；你正在用的 AI Agent（这里以 Claude Code 为例，Codex 等其他 Agent 做法相同）作为你的一个 Agent 登录，不拿任何密钥就能在你的一台服务器上执行命令；你还亲眼看着一次演练的故障从告警走到恢复。适合这样的读者：自己管着几台服务器，有一台装了 Docker 的机器的 root 权限，在工作站上用 Agent。

每一步都写了要花多久、执行什么、应该看到什么，以及看到别的结果时怎么办。

| 步骤 | 时间 |
| --- | --- |
| [0. 需要准备什么](#0-what-you-need) | 不计时：一台机器、两个域名、Agent 的 SSH 公钥 |
| [1. 安装中心](#1-install-the-centre) | 3 分钟，其中脚本运行 1 到 2 分钟 |
| [2. 第一个检测点](#2-the-first-probe) | 两个检测点共 5 分钟 |
| [3. 第一台机器](#3-the-first-machine) | 4 分钟 |
| [4. 第一个 Agent](#4-the-first-agent) | 5 分钟，包括建你自己的账号 |
| [5. 第一次演练](#5-the-first-drill) | 10 分钟，大部分时间在等规则触发 |
| [6. 告警群](#6-the-chat) | 不计时：创建飞书应用要等管理员审批 |

第 0 步准备好之后，第 1 到 5 步合计不到 30 分钟。DNS 生效和聊天应用的审批不由你决定，也不算在内。

## 0. 需要准备什么 {#0-what-you-need}

先说三个词。**中心**是运行 Beacon 的那台机器。**检测点**是放在用户附近的一台小机器，上面运行 `beacon-probe`，每分钟检测一次你的服务，把结果发给中心。**机器**指 Agent 可以通过 Beacon 登录、干活的服务器。

- **中心**：Linux，2 核 CPU、4 GB 内存、40 GB 磁盘；装好 [Docker Engine](https://docs.docker.com/engine/install/) 和 Docker Compose v2，以及 OpenSSH 客户端（`ssh-keygen`）。整套系统空闲时约占 200 MB 内存。
- **两个域名**，都解析到中心：一个给人和 Agent 用（下文的 `status.example.com`：API、MCP、控制台、状态页，以及 17002 端口上的 SSH 通道），一个给机器用（`metrics.example.com`：检测点上报数据的网关）。80 和 443 端口要能从公网访问，申请证书和日常访问都要用；17002 端口要能从 Agent 所在的地方访问。
- **一个邮箱地址**，Let's Encrypt 会往这里发证书相关的通知。必须是真实地址：安装脚本不接受 `example.com` 的邮箱。
- **一台小机器做第一个检测点**：任何带 systemd 的 Linux，x86_64 或 arm64，有 64 MB 空闲内存即可。最好是放在用户所在地区的 VPS。第二个检测点可以直接用中心。
- **一台给 Agent 干活的机器**，中心能连到它的 SSH 端口。用检测点那台也可以。
- **你的 Agent**：工作站上的 Claude Code，或任何支持 HTTP MCP 的程序，工作站上要有浏览器，用来给它登录；如果想让 Agent 也用它自己的 `ssh`，还要准备它的 SSH 公钥（工作站上的 `~/.ssh/id_ed25519.pub`）。

## 1. 安装中心 {#1-install-the-centre}

3 分钟。在中心上取得参考部署，看一下它指定的版本：

```bash
git clone https://github.com/openlinkos/beacon.git
sudo cp -r beacon/deploy /opt/beacon && cd /opt/beacon
grep BEACON_VERSION .env.example
```

`BEACON_VERSION` 就是 Beacon 要运行的版本；安装脚本第一次运行时，会把 `.env.example` 复制为 `.env`。如果它不是[最新发布的版本](https://github.com/openlinkos/beacon/releases)，现在就在 `.env.example` 里改掉。以后要升级，改 `.env` 里的版本，再运行一次安装脚本；脚本随时可以重复运行。然后运行它：

```bash
sudo ./install.sh
```

脚本会询问两个域名和邮箱地址。想无人值守地安装，就把它们放在环境变量里：`STATUS_HOST=… METRICS_HOST=… ACME_EMAIL=… sudo ./install.sh`。接下来脚本把 `.env.example` 复制为 `.env`，生成密钥（规则引擎用的令牌，以及 SSH 通道的主机密钥和 CA 密钥，这些都不会离开这台机器），把你的域名填进 `config.yaml` 和检测目标，用各自的镜像逐个校验文件，拉取 Beacon、VictoriaMetrics、vmalert 和 Caddy 四个镜像并启动，一直等到四个容器都健康、规则引擎的第一条告警已经送到 Beacon。在网络很好的主机上实测用了 69 秒；2 核机器加普通带宽，按 1 到 2 分钟算，大部分时间花在拉镜像上。

最后应该看到这样的输出：

```
configuration OK: Feishu app false, Pushover false, Bark false; gateway ":17006"; SSH channel :17002; 0 agent hooks
files: 1 probe locations, 0 heartbeats, 0 resources, 3 probe targets in 2 sets, 5 modules, 1 status pages
database: none yet at /var/lib/beacon/status.db; the service imports the files when it first starts
Waiting for the containers to be healthy …

Beacon is up (69s); made the secrets: rules-token ssh-host-key ssh-ca-key.
  https://status.example.com          the API (/api/v1), MCP (/mcp) and pages; the first certificate takes about a minute
  https://metrics.example.com         the gateway for machines
  status.example.com:17002            the SSH channel
```

后面还会列出下一步要做的事，也就是这篇指南接下来的几步。然后在任意地方检查两个域名：

```bash
curl https://status.example.com/healthz          # ok
curl https://metrics.example.com/api/v1/targets  # {"code":"auth.required",…}：没带令牌就被拒绝，这是对的
```

在浏览器里打开 `https://status.example.com/`，看到的是 `catalog/status_pages.yaml` 里的示例状态页；检测点上报数据之前，它显示 **No data yet**。现在还不会通知任何人：没有配置聊天工具时，告警只记录下来，在 API、MCP 和控制台里能看到，接下来几步用这些就够了。

如果结果不一样：

- **输出 `install.sh: …` 后停下**：提示会写明缺了什么（root 权限、Docker、Compose v2、`ssh-keygen`、真实的邮箱、两个不同的域名），或者哪个文件没通过校验，并给出查看原因的命令。
- **`curl` 报 TLS 错误**：启动后第一张证书最多要等一分钟。如果还是失败，检查两个域名是否都解析到中心、80 和 443 端口是否开放；`docker compose logs caddy` 能看到 Let's Encrypt 的回复。

这篇指南后面的操作都在 `/opt/beacon` 下通过 `docker exec beacon beacon …` 执行。每个文件放在哪里，见 [deploy/README.md](https://github.com/openlinkos/beacon/blob/main/deploy/README.md)。

## 2. 第一个检测点 {#2-the-first-probe}

两个检测点共 5 分钟。Beacon 靠多个检测点的一致判断来区分真正的故障和偶尔的抖动：至少两个检测点检测失败，并且失败的至少占有数据的检测点的一半，才算服务故障。所以第一个检测点带来数据，第二个检测点才带来告警。

**第一个检测点。** 目录文件 `catalog/locations.yaml` 在首次启动时导入 Beacon 的数据库，里面已经有一个检测点 `edge-1`。在中心上登记它，令牌只打印这一次：

```bash
docker exec beacon beacon enroll probe edge-1 > edge-1.token
```

应该看到 `enrolled probe-edge-1 (probe:write, expires …); the token above is not shown again`，`edge-1.token` 里是一行以 `lbt_` 开头的令牌。把 `/opt/beacon/probe/` 目录和 `edge-1.token` 复制到检测点机器上，以 root 执行：

```bash
sh probe/install.sh -gateway https://metrics.example.com -token-file edge-1.token
```

脚本安装与这份参考部署相配的 `beacon-probe` 版本（检测程序和服务一起发布；用 `-version` 可以指定别的版本）。它会创建 `beacon-probe` 用户，按机器架构下载这个版本的 `beacon-probe`，用发布页的 `SHA256SUMS` 核对（访问不了 GitHub 的机器，可以用 `-binary FILE` 安装你自己下载的文件），把令牌装成只有这个用户能读，装好 systemd 单元 `beacon-probe` 和 `beacon-heartbeat` 小工具，启动检测程序，然后等网关收下第一次推送。大约 10 秒，最后输出：

```
edge-1: beacon-probe beacon 0.3.0-rc.13 …; installed: token agent.env beacon-probe beacon-probe.service beacon-heartbeat; 13 MB resident
the gateway at https://metrics.example.com accepted the first push; the location's results arrive every minute
```

从此这个检测点每分钟检测一次网关交给它的目标，以后改检测内容，都不用再动检测点。如果输出 `no push accepted by … within 150 seconds`，在检测点机器上运行 `journalctl -u beacon-probe -n 50`：`401` 说明令牌文件不是 `enroll` 打印的那个；`403` 加 `location.inactive` 说明目录里没有这个检测点；连接错误说明这台机器连不上 `metrics.example.com` 的 443 端口。时钟未同步的警告也要处理：检测结果带时间戳，请安装 chrony 或打开 systemd-timesyncd。

**第二个检测点。** 可以就用中心（几乎不花钱），也可以是另一台小机器。在中心上编辑目录文件 `/opt/beacon/catalog/locations.yaml`，在 `edge-1` 下面加上：

```yaml
  - id: edge-2
    name: Germany - Frankfurt      # 公开显示：写地点和网络，不写厂商
    region: overseas
    network: BGP
```

把文件导入 Beacon：先预演一遍看会改什么，再正式导入，然后登记新的检测点：

```bash
docker exec beacon beacon config import /etc/beacon -dry-run
docker exec beacon beacon config import /etc/beacon -note "second probe location"
docker exec beacon beacon enroll probe edge-2 > edge-2.token
```

预演应该在 `would change 1 items (nothing changed):` 下面列出一行 `+ locations edge-2`，正式导入则在 `version 2: import of 1 items` 下面列出同样一行。然后照上面的步骤在第二台机器上安装检测程序。如果装在中心上，就让它直接连网关自己的端口，参考部署把这个端口开放在 `127.0.0.1` 上：

```bash
sudo sh probe/install.sh -gateway http://127.0.0.1:17006 -token-file edge-2.token
```

导入会让这些文件成为全部配置：通过 API 或 MCP 改过、而文件里没有的内容会被删掉，所以要先预演。两种修改配置的方式以及怎样让它们保持一致，见[检测什么，从哪里检测](catalog.md)。

**看看数据到了没有。** VictoriaMetrics 自带的界面在中心的 `http://127.0.0.1:8428/vmui`（在工作站上用 `ssh -N -L 8428:127.0.0.1:8428 centre` 打开）。`probe_success` 每个检测点、每个目标各有一条序列，成功为 1。`https://status.example.com/` 上的公开页面现在也显示出各组件的状态了。随附的检测目标有三个：Beacon 自己的状态域名、演练服务（第 5 步）和 `https://example.com/`；把最后一个换成你自己的服务，改的是 `/opt/beacon/targets/all-web.yaml`，改完再导入一次。

## 3. 第一台机器 {#3-the-first-machine}

4 分钟。添加一台给 Agent 干活的机器。在中心上执行：

```bash
docker exec beacon beacon machine add edge-1 edge-1.example.net -note "the first probe machine"
```

Beacon 会连接这台机器一次，固定它的主机密钥（以后这台机器出示别的密钥，Beacon 一律拒绝），然后打印机器上要做的配置：

```
added edge-1 at edge-1.example.net:22 as beacon; its host key is SHA256:…
On the machine, as root, if not done yet:
  echo 'ssh-ed25519 AAAA… beacon-ca' > /etc/ssh/beacon_ca.pub
  echo 'TrustedUserCAKeys /etc/ssh/beacon_ca.pub' > /etc/ssh/sshd_config.d/beacon.conf
  id beacon >/dev/null 2>&1 || useradd -m -s /bin/bash -p '*' beacon
  systemctl reload ssh
(see docs/ssh.md for sudo and for PerSourcePenaltyExemptList)
```

在机器上以 root 执行中间这四行。它们让 sshd 信任 Beacon 的 CA（为每次连接签发短期证书的那把密钥），并创建证书里指定的账户。如果 `machine add` 报 `the machine cannot be reached`，说明中心连不上这台机器的 SSH 端口：在机器的防火墙里放行；如果机器在 NAT 后面，就按 [NAT 后面的机器](ssh.md#machines-behind-nat)反过来添加。

还有两项配置最好现在就做：

- **`beacon` 账户能做什么，才是真正的边界。** 只给它干活需要的权限，比如在 `/etc/sudoers.d/beacon` 里写：`beacon ALL=(root) NOPASSWD: /usr/bin/systemctl restart beacon-probe, /usr/bin/journalctl`。
- **别让 OpenSSH 惩罚中心的地址。** 所有 Agent 都从中心的地址连过来，连接失败多了会被 OpenSSH 按来源地址限制，所以在 `beacon.conf` 里加上 `PerSourcePenaltyExemptList 203.0.113.7`（中心的地址）。

其余内容见 [SSH 通道](ssh.md)。

## 4. 第一个 Agent {#4-the-first-agent}

5 分钟。先在 Beacon 上建你自己的账号，再把 Claude Code 接成你的一个 Agent：它通过浏览器登录，不用复制任何令牌，能做的事也不会超出你的角色。

**你的账号。** Beacon 现在还没有账号。它启动时已经往日志里写了一个一次性的初始化链接；在中心上重新打印一个：

```bash
docker exec beacon beacon setup
```

```
Open this link within 30 minutes (until …) to make the first account, an owner. It works once:
https://status.example.com/console/setup?code=…
```

在浏览器里打开链接，设置用户名（下文用 `alice`）、显示名和至少 10 个字符的密码。你就是第一位所有者，已经登录了 `https://status.example.com/console` 上的控制台。这个链接只能用一次；其他人都通过邀请加入（[成员和 Agent](accounts.md#invite-people)）。

**Claude Code。** 在工作站上添加 Beacon 的 MCP 服务器，不带令牌：

```bash
claude mcp add --scope user --transport http beacon https://status.example.com/mcp
```

启动 Claude Code，执行 `/mcp`，选中 `beacon` 并完成认证。浏览器会打开 Beacon 的授权页面「Claude Code 想以你的身份使用 Beacon」。Agent 名称保留 `claude-code`，**可以做什么**选**值班**（能处理告警、能上机器），机器选全部，有效期选 30 天，然后点**允许**。回到 Claude Code，`/mcp` 显示 `beacon` 已连接；`claude mcp list` 显示 `beacon: … Connected`。现在 Claude Code 手里有一个一小时有效的访问令牌，并在你允许的 30 天里自己刷新。在审计日志和告警群里，它是 `alice / claude-code`。

**它自己的密钥。** 对 Claude Code 说：*「把我的 SSH 公钥 ~/.ssh/id_ed25519.pub 登记到 Beacon，列出 Beacon 能连的机器，在 edge-1 上执行 `uptime`。」* 它会依次调用 `add_ssh_key`、`list_machines` 和 `run_command`，拿回退出码、stdout 和 stderr，都是纯文本，不带终端控制码。这次连接 Beacon 签了一张五分钟有效的证书；Agent 手里没有这台机器的任何密钥。

同一个 `list_machines` 的结果里还有一段 `ssh_config` 和一行 `known_hosts`，给 Agent 自己的 ssh 用：

```
Host edge-1
  HostName status.example.com
  Port 17002
  User edge-1
  HostKeyAlias beacon
```

把这段配置放进工作站的 `~/.ssh/config`，把那一行放进 `~/.ssh/known_hosts`，`ssh edge-1 uptime`、`scp`、`rsync -e ssh` 和 `ssh -L` 就和平常一样能用：用户名就是机器名，会话在机器上以 `beacon` 账户运行。如果 ssh 提示 `Permission denied (publickey)`，说明它出示的密钥 Beacon 不认识：让 Claude Code 用 `add_ssh_key` 登记它，或者在控制台上依次打开 **Agent**、`claude-code`、**添加 SSH 密钥**加上去。

每次会话都有记录。控制台的 **Agent** 页面上能看到 `claude-code`、它的登录和它的密钥；`list_sessions`（或 `GET /api/v1/sessions`）列出所有会话，每条命令和它的退出码都在里面；`docker exec beacon beacon ssh pause` 会关闭全部会话并拒绝新的会话，直到执行 `ssh resume`。

**没有浏览器的 Agent**，比如 CI 任务或被 Hook 唤醒的 Agent，改用令牌：在控制台的 **Agent** 页面上创建，或在中心上执行 `docker exec beacon beacon agent add oncall-bot -owner alice -preset oncall`，令牌只打印这一次，同时给出使用它的命令（[无界面的 Agent 和 CI](accounts.md#headless-agents-and-ci)）。

## 5. 第一次演练 {#5-the-first-drill}

10 分钟，大部分时间在等。Beacon 自带一个用来演练的服务：`/drill/healthz` 平时返回 200，直到你让它失败；每个检测点都把它当作严重级别的服务 `drill` 来检测（`targets/all-drill.yaml`）。公开状态页上不显示它。

```bash
docker exec beacon beacon drill fail 15m    # drill until …: /drill/healthz answers 503
```

有两个检测点时，接下来会这样：

1. **1 分钟内**两个检测点都看到 503，各自的 `probe_success{service="drill"}` 变为 0。
2. **连续失败 3 分钟后**，规则把每个检测点都算作失败。两个检测点都失败，既满足「至少两个」，也满足「至少一半」，所以**再过 1 分钟 `ServiceDown` 触发**，距执行命令大约 4 到 5 分钟。它会出现在 `GET /api/v1/alerts` 和 MCP 的 `list_alerts` 里。
3. **问你的 Agent 出了什么事。** 它会调用 `get_evidence`，一次拿到每个检测点的结果和失败原因（`http_status`，503）、同一故障域里的其他告警、最近的变更，以及已经做过的事。它用带备注的 `ack_alert` 或 `add_note` 把自己的工作记到告警的时间线上；`silence_alert` 让告警安静下来。
4. **结束演练**：`docker exec beacon beacon drill off`。检测点下一轮就会成功，最多等一分钟；规则引擎每 30 秒评估一次，随后报告告警已恢复，Beacon 立即把它标为恢复。

只有一个检测点时什么都不会触发，这是有意设计的：单个检测点说坏了，永远不算故障。

**等待的时候，在控制台里看着它**：打开 `https://status.example.com/console`，你在第 4 步已经登录过（一次登录有效 12 小时）。告警页面上能看到每个检测点看到了什么、时间线、备注，以及告警涉及的机器上的每次会话和回放。详见[控制台](pages.md)。

在中心上，`beacon silence list` 能看到 Agent 设的静默；`beacon silence create -matchers 'alertname=ServiceDown,service=drill' -duration 1h -comment 'drill'` 则从命令行创建一条静默，审计日志里会记下它来自命令行。

配好告警群（第 6 步）之后，告警触发约 30 秒后（`group_wait`）卡片会出现在群里，卡片上的按钮可以认领和静默，恢复后卡片变绿；配了 Pushover 的话，没人认领的严重告警 15 分钟后会打到手机上。

如果 6 分钟后仍没有告警，检查两个检测点是否都在上报（在 vmui 里 `probe_success{service="drill"}` 应该有两条序列），以及 `curl -i https://status.example.com/drill/healthz` 是否返回 `503` 和 `drill`。更多排查方法见[常见问题](troubleshooting.md#an-alert-never-arrives)。

## 6. 告警群 {#6-the-chat}

不算在 30 分钟里：飞书（或 Lark）的自建应用要等管理员审批，多久取决于你的组织。[配置飞书应用](feishu.md)一步步讲了应用、权限、长连接、群和自定义机器人。准备好之后，在中心上：

1. 把每个密钥放进 `/opt/beacon/secrets/` 下对应的文件：`feishu-app-id`、`feishu-app-secret` 和 `feishu-webhook-url`；手机通知用 `pushover-user-key` 和 `pushover-app-token`；`healthchecks-bridge-url` 用于失联报警：告警能送达时，Beacon 每分钟 ping 一次 healthchecks.io，中心自己失联时，由 healthchecks.io 通知你。
2. 在 `/opt/beacon/config.yaml` 里取消 `feishu:` 段（以及 `pushover:`、`healthchecks:`）的注释，填上你的 `chat_id` 和值班的人。
3. 再运行一次 `sudo ./install.sh`：它会把密钥文件的属主和权限改成 Beacon 需要的样子，并校验所有文件。然后重启 Beacon，让它读到新配置：`docker compose restart beacon`。之后日志里会出现 `feishu long connection ready`（`docker compose logs beacon`）。

再做一次演练，看卡片怎么出现。如果想让新告警自动叫醒 Agent，而不是等人去问它，按[让 Agent 值班](agents.md)加一个 `agents:` 钩子。

## 接下来读什么 {#what-to-read-next}

- [成员和 Agent](accounts.md)：邀请团队成员、角色、Codex、无界面的 Agent 和它们的令牌，以及从令牌迁移过来。
- [让 Agent 值班](agents.md)：叫醒 Agent、证据、备注、时间线，以及修完之后立刻重测。
- [SSH 通道](ssh.md)：机器上的账户、录像、暂停、NAT 后面的机器，以及通道不做什么。
- [检测什么，从哪里检测](catalog.md)：检测点、检测目标和检测模块，以及怎样通过 Beacon 修改它们。
- [常见问题](troubleshooting.md)：上面某一步没有按预期发生时怎么办。
- [deploy/README.md](https://github.com/openlinkos/beacon/blob/main/deploy/README.md) 和 [deploy/runbooks/](https://github.com/openlinkos/beacon/tree/main/deploy/runbooks)：各个文件，以及每种告警一页的处理手册。
- [目标](goals.md)：Beacon 为什么而做，以及它不做什么。
