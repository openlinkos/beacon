---
sourceHash: sha256-bd233833
---

# 装在服务器上

这篇指南带你从一台空的 Linux 机器开始，装好 Beacon 里住在服务器上的那些部分：做完之后，中心跑在一台机器上，你是它的所有者；两个检测点在检测你的服务；一台服务器登记为 Agent 可以通过 Beacon 登录的机器。接 Agent 和演练故障各有单独的页面，在第 4、5 步链过去。适合这样的读者：自己管着几台服务器，有一台装了 Docker 的机器的 root 权限，在工作站上用 Agent。想先看看闭环长什么样再拿出服务器，可以先[在本机试一试](../guide/quickstart.md)。

每一步都写了要花多久、执行什么、应该看到什么，以及看到别的结果时怎么办。中心上的命令都在 `/opt/beacon` 下通过 `docker exec beacon beacon …` 执行。

| 步骤 | 时间 |
| --- | --- |
| [0. 需要准备什么](#0-what-you-need) | 不计时：一台机器、两个域名 |
| [1. 安装中心](#1-install-the-centre) | 3 分钟，其中脚本运行 1 到 2 分钟 |
| [2. 第一个检测点](#2-the-first-probe) | 两个检测点共 5 分钟 |
| [3. 第一台机器](#3-the-first-machine) | 4 分钟 |
| [4. 第一个 Agent](#4-the-first-agent) | 5 分钟，见[在本机试一试](../guide/quickstart.md) |
| [5. 第一次演练](#5-the-first-drill) | 10 分钟，见[演练一次故障](../guide/drills.md) |

第 0 步准备好之后，第 1 到 3 步合计约 12 分钟。DNS 生效不由你决定，也不算在内。

## 0. 需要准备什么 {#0-what-you-need}

先说三个词。**中心**是运行 Beacon 的那台机器。**检测点**是放在用户附近的一台小机器，上面运行 `beacon-probe`，每分钟检测一次你的服务，把结果发给中心。**机器**指 Agent 可以通过 Beacon 登录、干活的服务器。

- **中心**：Linux，2 核 CPU、4 GB 内存、40 GB 磁盘；装好 [Docker Engine](https://docs.docker.com/engine/install/) 和 Docker Compose v2，以及 OpenSSH 客户端（`ssh-keygen`）。
- **两个域名**，都解析到中心：一个给人和 Agent 用（下文的 `status.example.com`：API、MCP、控制台、状态页，以及 17002 端口上的 SSH 通道），一个给机器用（`metrics.example.com`：检测点上报数据的网关）。80 和 443 端口要能从公网访问，申请证书和日常访问都要用；17002 端口要能从 Agent 所在的地方访问。
- **一个邮箱地址**，Let's Encrypt 会往这里发证书相关的通知。必须是真实地址：安装脚本不接受 `example.com` 的邮箱。
- **一台小机器做第一个检测点**：任何带 systemd 的 Linux，x86_64 或 arm64，有 64 MB 空闲内存即可。最好是放在用户所在地区的 VPS。第二个检测点可以直接用中心。
- **一台给 Agent 干活的机器**，中心能连到它的 SSH 端口。用检测点那台也可以。
- **一个 Agent**，装在工作站上，工作站要有浏览器，用来给它登录（[第 4 步](#4-the-first-agent)）。

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
database: none yet at /var/lib/beacon/beacon.db; the service imports the files when it first starts
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

部署里每个文件放在哪里，见 [deploy/README.md](https://github.com/openlinkos/beacon/blob/main/deploy/README.md)。

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

大约 10 秒后，最后输出：

```
edge-1: beacon-probe beacon 0.3.0-rc.18 …; installed: token agent.env beacon-probe beacon-probe.service beacon-heartbeat; 13 MB resident
the gateway at https://metrics.example.com accepted the first push; the location's results arrive every minute
```

从此这个检测点每分钟检测一次网关交给它的目标，以后改检测内容，都不用再动检测点。

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

导入会让这些文件成为全部配置：通过 API 或 MCP 改过、而文件里没有的内容会被删掉，所以要先预演。两种修改配置的方式以及怎样让它们保持一致，见[检测什么，从哪里检测](../guide/monitoring.md)；检测点放在哪里、机器需要什么、安装不顺利时怎么办，见[检测点位置](probes.md)。

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
(see docs/guide/machines.md for sudo and for PerSourcePenaltyExemptList)
```

在机器上以 root 执行中间这四行。它们让 sshd 信任 Beacon 的 CA（为每次连接签发短期证书的那把密钥），并创建证书里指定的账户。如果 `machine add` 报 `the machine cannot be reached`，说明中心连不上这台机器的 SSH 端口：在机器的防火墙里放行；如果机器在 NAT 后面，就按 [NAT 后面的机器](../guide/machines.md#machines-behind-nat)反过来添加。

还有两项配置最好现在就做：

- **`beacon` 账户能做什么，才是真正的边界。** 只给它干活需要的权限，比如在 `/etc/sudoers.d/beacon` 里写：`beacon ALL=(root) NOPASSWD: /usr/bin/systemctl restart beacon-probe, /usr/bin/journalctl`。
- **别让 OpenSSH 惩罚中心的地址。** 所有 Agent 都从中心的地址连过来，连接失败多了会被 OpenSSH 按来源地址限制，所以在 `beacon.conf` 里加上 `PerSourcePenaltyExemptList 203.0.113.7`（中心的地址）。

其余内容见[给 Agent 一台机器](../guide/machines.md)。

## 4. 第一个 Agent {#4-the-first-agent}

5 分钟，每一步都写在[在本机试一试](../guide/quickstart.md)里：对着你的服务器执行同样的命令。简言之：用服务打印的一次性链接建第一个账号，然后通过 MCP 接上你的 Agent，在浏览器里给它授权：

```bash
docker exec beacon beacon setup
claude mcp add --scope user --transport http beacon https://status.example.com/mcp
```

Agent 能做的事不会超出你的角色，它在机器上执行的每条命令都有记录、可以回放。角色、Codex、邀请成员和无界面 Agent 的令牌，见[成员和 Agent](../guide/people.md)。

## 5. 第一次演练 {#5-the-first-drill}

10 分钟，大部分时间在等，完整步骤见[演练一次故障](../guide/drills.md)。简言之：Beacon 自带一个用来演练的服务；有两个检测点时，`docker exec beacon beacon drill fail 15m` 让所有检测点看到失败，约 4 到 5 分钟后 `ServiceDown` 触发；`beacon drill off` 结束演练，告警随即恢复。

## 接下来 {#what-next}

- [配置飞书应用](../integrations/feishu.md)，让告警以卡片送达成员。每个密钥放进 `/opt/beacon/secrets/` 下的一个文件，`/opt/beacon/config.yaml` 里的 `feishu:` 段取消注释，`sudo ./install.sh` 加 `docker compose restart beacon` 让它生效（[配置](configuration.md)）。
- [让 Agent 值班](../guide/on-call.md)，新告警自动叫醒 Agent，不用等人去问。
- [升级](upgrade.md)、[备份与恢复](backup.md)和[安全模型](security.md)让它一直跑下去。
- 上面某一步没有按预期发生时，见[常见问题](troubleshooting.md)。
- [目标](../about/goals.md)：Beacon 为什么而做，以及它不做什么。
