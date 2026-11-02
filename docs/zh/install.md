---
sourceHash: sha256-7502a44c
---

# 30 分钟上线

读完这一页，Beacon 就跑在你的一台机器上：第二台机器上的检测点在上报，你已经在用的 Agent（这里以 Claude Code 为例，Codex 等同理）经 Beacon 到达一台机器而不拿任何密钥，你还看着一次演练的故障从告警走到恢复。每一步都写了要花多久；下面的预算说清了哪些算在内、哪些不算。

| 步骤 | 时间 |
| --- | --- |
| [0. 需要准备什么](#0-what-you-need) | 不计：一台机器、两个域名、Agent 的 SSH 公钥 |
| [1. 安装中心](#1-install-the-centre) | 3 分钟，其中脚本跑 1 到 2 分钟 |
| [2. 第一个检测点](#2-the-first-probe) | 两个检测点共 5 分钟 |
| [3. 第一台机器](#3-the-first-machine) | 4 分钟 |
| [4. 第一个 Agent](#4-the-first-agent) | 5 分钟 |
| [5. 第一次演练](#5-the-first-drill) | 10 分钟，大半在等规则 |
| [6. 告警群](#6-the-chat) | 不计：建飞书应用要等管理员审批 |

第 0 步做完后，第 1 到 5 步合计不到 30 分钟。DNS 生效和聊天应用的审批不在你手里，也不在计时里。

## 0. 需要准备什么 {#0-what-you-need}

- **中心**：一台 Linux 机器，2 核 CPU、4 GB 内存、40 GB 磁盘，装好 [Docker Engine](https://docs.docker.com/engine/install/) 和 Docker Compose v2，以及 OpenSSH 客户端（`ssh-keygen`）。整套安装空闲时约占 200 MB 内存。
- **两个域名**指向它：一个给人和 Agent（下文的 `status.example.com`：API、MCP、页面，以及 17002 端口的 SSH 通道），一个给机器（`metrics.example.com`：网关）。80 和 443 端口要能从公网访问，签证书和走流量都靠它们；17002 端口要能从 Agent 所在的地方访问。
- **一个邮箱地址**，Let's Encrypt 用来发证书相关的通知。
- **一台小机器**做第一个检测点：任何带 systemd 的 Linux，x86_64 或 arm64，有 64 MB 空余内存即可。放在用户所在地的 VPS 最好；第二个检测点可以用中心自己。
- **一台给 Agent 干活的机器**，中心能 SSH 到它；用检测点那台机器也行。
- **你的 Agent 程序**，带一把 SSH 公钥（`~/.ssh/id_ed25519.pub`）；用 MCP 的话，Claude Code 或任何能走 HTTP MCP 的程序都可以。

## 1. 安装中心 {#1-install-the-centre}

在中心上，3 分钟：

```bash
git clone https://github.com/openlinkos/beacon.git
sudo cp -r beacon/deploy /opt/beacon && cd /opt/beacon
sudo ./install.sh
```

脚本会询问两个域名和邮箱地址（也可以从环境变量读：`STATUS_HOST=… METRICS_HOST=… ACME_EMAIL=… sudo ./install.sh`），然后把 `.env.example` 复制成 `.env`，生成密钥（规则引擎用的令牌，以及 SSH 通道的主机密钥和 CA 密钥，都不离开这台机器），把你的域名填进 `config.yaml` 和检测目标，用各自的镜像校验每个文件，拉取四个镜像并启动，等四个容器都健康、规则引擎的第一条告警送到网关，再打印下一步该做什么。在一台网络很好的主机上实测 69 秒，含拉镜像；2 核机器加普通带宽按 1 到 2 分钟算，大半花在拉镜像上。

检查：`curl https://status.example.com/healthz` 返回 `ok`（启动后第一张证书最多要等一分钟），`curl https://metrics.example.com/api/v1/targets` 返回 401，不带令牌本该如此。此时还不会通知任何人：没配置聊天工具时，告警只记录下来、在 API 和 MCP 里能看到，这对下面几步已经够用。

其余操作都经 `docker exec beacon beacon …` 完成；[deploy/README.md](https://github.com/openlinkos/beacon/blob/main/deploy/README.md) 说明了每个文件在哪里。

## 2. 第一个检测点 {#2-the-first-probe}

Beacon 靠多地点的一致判断来区分故障和抖动：至少两个检测点、并且至少一半有数据的检测点失败，才算服务故障。所以第一个检测点给你数据，第二个才给你告警。两个共 5 分钟。

目录（`catalog/locations.yaml`，首次启动时导入）里有一个检测点 `edge-1`。在中心上登记它：

```bash
docker exec beacon beacon enroll probe edge-1 > edge-1.token     # 只显示这一次
```

把 `/opt/beacon/probe/` 和令牌复制到检测点机器上，在那里以 root 执行：

```bash
sh probe/install.sh -gateway https://metrics.example.com -token-file edge-1.token
```

它会创建 `beacon-probe` 用户，按机器架构下载 `beacon-probe` 的发布版本并用发布的 `SHA256SUMS` 核对（也可以 `-binary FILE` 给它一个），把令牌装成只有该用户可读，装好 systemd 单元 `beacon-probe` 和 `beacon-heartbeat` 小工具，启动，然后等网关收下第一次推送：约 10 秒，常驻内存 13 MB。从此这个检测点每分钟检测网关发给它的目标，改检测目标再也不用碰检测点。

第二个检测点可以就是中心自己（几乎不花钱），也可以是另一台小机器。先用一个能改配置的 API 令牌把它加进目录，再像上面一样登记、安装：

```bash
docker exec beacon beacon token create setup -scopes config:write -expires 1d > setup.token
curl -X PUT -H "Authorization: Bearer $(cat setup.token)" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/config/locations/centre \
  -d '{"name": "Germany - Frankfurt", "region": "overseas", "network": "BGP"}'
docker exec beacon beacon enroll probe centre > centre.token
sudo sh probe/install.sh -gateway http://127.0.0.1:17006 -token-file centre.token   # 在中心上：直接用网关自己的端口
docker exec beacon beacon token revoke setup
```

检测点的名字按地点和网络写，不写厂商：这个名字是公开的。看看数据到了没有：VictoriaMetrics 自带的界面在中心的 `http://127.0.0.1:8428/vmui`（从工作站经 `ssh -N -L 8428:127.0.0.1:8428 centre` 打开）；`probe_success` 每个检测点、每个目标各一条序列。随附的两个检测目标是 Beacon 自己的状态域名和 `https://example.com/`；把后者换成你自己的服务：改 `targets/all-web.yaml` 再 `beacon config import /etc/beacon`，或者按[目录、检测目标和检测模块](catalog.md)用 `PUT /api/v1/config/targets/all-web`。

## 3. 第一台机器 {#3-the-first-machine}

一台给 Agent 干活的机器，4 分钟。在中心上：

```bash
docker exec beacon beacon machine add edge-1 edge-1.example.net -note "the first probe machine"
```

Beacon 连接一次，记下这台机器的主机密钥，并打印出机器需要做的事：它的 sshd 要信任 Beacon 的 CA，证书里指定的账户要存在。在机器上以 root 执行它打印的四行：

```bash
echo 'ssh-ed25519 AAAA… beacon-ca' > /etc/ssh/beacon_ca.pub
echo 'TrustedUserCAKeys /etc/ssh/beacon_ca.pub' > /etc/ssh/sshd_config.d/beacon.conf
useradd -m -s /bin/bash -p '*' beacon
systemctl reload ssh
```

还有两行值得现在就加上：`beacon` 账户能做什么才是真正的边界，只给它工作所需的权限（`/etc/sudoers.d/beacon`：`beacon ALL=(root) NOPASSWD: /usr/bin/systemctl restart beacon-probe, /usr/bin/journalctl`）；并把中心的地址排除在 OpenSSH 对连接失败的惩罚之外（`beacon.conf` 里加 `PerSourcePenaltyExemptList 203.0.113.7`），因为所有 Agent 都从那里来。其余见 [SSH 通道](ssh.md)。

## 4. 第一个 Agent {#4-the-first-agent}

5 分钟。一个令牌同时用于 API、MCP 和 SSH 通道；把 Agent 的公钥给它，它自己的 `ssh` 也就能用了：

```bash
docker exec beacon beacon token create claude-code \
  -scopes status:read,alerts:write,machines:ssh,sessions:read -expires 90d \
  -ssh-key "$(cat ~/.ssh/id_ed25519.pub)"
```

令牌只显示一次。在跑 Claude Code 的工作站上：

```bash
claude mcp add --transport http beacon https://status.example.com/mcp --header "Authorization: Bearer lbt_…"
```

然后在 Claude Code 里让它列出机器，并在 `edge-1` 上执行 `uptime`。它会调用 `list_machines`，再调用 `run_command`；结果以退出码、stdout 和 stderr 返回，没有终端控制码。同一个 `list_machines` 的结果里有一段 `ssh_config` 和一行 `known_hosts`：放进 `~/.ssh/config` 和 `~/.ssh/known_hosts`，`ssh edge-1 uptime`、`scp`、`rsync -e ssh` 和 `ssh -L` 就照常可用，用户名就是机器名。这次连接 Beacon 签了一张五分钟有效的证书；Agent 手里没有这台机器的任何密钥。

每次会话都有记录：`docker exec beacon beacon token list` 看到令牌，`list_sessions`（或 `GET /api/v1/sessions`）看到会话，每条命令和它的退出码都在。`beacon ssh pause` 关闭全部会话，直到 `ssh resume`。

## 5. 第一次演练 {#5-the-first-drill}

10 分钟，大半在等。Beacon 内置了一个用来演练的服务：`/drill/healthz` 平时返回 200，直到你让它失败；每个检测点都检测它（`targets/all-drill.yaml`，服务名 `drill`，级别严重）。

```bash
docker exec beacon beacon drill fail 15m
```

有两个检测点时会看到：

- 1 分钟内两个检测点都看到 503，各自的 `probe_success{service="drill"}` 变成 0；
- 连续失败 3 分钟后，规则把每个检测点算作失败；两个都失败既是「至少两个」也是「至少一半」，所以再过 1 分钟 `ServiceDown` 触发：距命令约 4 到 5 分钟；
- 告警出现在 `GET /api/v1/alerts` 和 MCP 的 `list_alerts` 里。问你的 Agent 出了什么事：`get_evidence` 返回每个检测点的结果和原因（`http_status`）、同一故障域里的告警、最近的变更和已经做了什么；`ack_alert` 带备注，以及 `add_note`，把它的工作记到告警的时间线上；`silence_alert` 让它安静。在中心上，`beacon silence list` 能看到这条静默，`beacon silence create -matchers 'alertname=ServiceDown,service=drill' -duration 1h -comment 'drill'` 则从命令行创建一条，审计里会记下来源是命令行；
- `docker exec beacon beacon drill off`；检测点下一轮就成功，规则引擎停止发送后，告警在 `resolve_timeout`（5 分钟）内恢复。

只有一个检测点时什么都不会触发，这是故意的：一个检测点的话永远不算故障。配置了告警群（第 6 步）后，卡片会在命令后约 4 分钟出现在群里，按钮可以认领和静默，恢复后变绿；配置了 Pushover，没人认领的严重告警 15 分钟后会打到手机上。

## 6. 告警群 {#6-the-chat}

不算在 30 分钟里：飞书（或 Lark）自建应用要等管理员审批，多久取决于你的组织。[配置飞书应用](feishu.md)一步步讲了应用、权限、长连接、群和自定义机器人。然后在中心上：

1. 把每个密钥放进 `/opt/beacon/secrets/` 下对应的文件（`feishu-app-id`、`feishu-app-secret`、`feishu-webhook-url`；手机用 `pushover-user-key` 和 `pushover-app-token`；看门狗用 `healthchecks-bridge-url`）。
2. 在 `config.yaml` 里取消 `feishu:` 段（以及 `pushover:`、`healthchecks:`）的注释，填上你的 `chat_id` 和值班的人。
3. 再跑一次 `sudo ./install.sh`：它会修正文件的属主和权限，校验并重启。

然后再做一次演练，看卡片。要让新告警主动叫醒 Agent、而不是等人去问它，按 [Agent 与告警](agents.md)加一个 `agents:` 钩子。

## 接下来读什么 {#what-to-read-next}

- [Agent 与告警](agents.md)：唤醒 Agent、证据、备注和时间线。
- [SSH 通道](ssh.md)：SSH 通道的全貌：账户、录制、暂停、它不做什么。
- [目录、检测目标和检测模块](catalog.md)：目录、检测目标和模块，以及怎样经 Beacon 修改它们。
- [deploy/README.md](https://github.com/openlinkos/beacon/blob/main/deploy/README.md) 和 [deploy/runbooks/](https://github.com/openlinkos/beacon/tree/main/deploy/runbooks)：各个文件，以及每条告警一页的处理手册。
- [目标](goals.md)：Beacon 为什么而做，以及它不做什么。
