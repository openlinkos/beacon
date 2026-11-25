---
sourceHash: sha256-82832927
---

# 装在服务器上

这篇指南带你从一台空的 Linux 机器开始，装好 Beacon 里住在服务器上的那些部分。做完之后：

- 中心跑在一台机器上，你是它的所有者；
- 两个检测点在检测你的服务；
- 已预览并发布了要向用户展示的服务；
- 已登记一台机器，你能回放个人 SSH 会话和 Agent 会话；
- 演练告警送达你的测试群，你能打开并认领同一条告警。

这篇指南适合这样的读者：自己管着几台服务器，有一台装了 Docker 的机器的 root 权限，在工作站上用 Agent。想先看看闭环长什么样再拿出服务器，可以先[在本机试一试](../guide/quickstart.md)。

中心上的命令在 `/opt/beacon` 下执行。如果当前用户访问 Docker 套接字需要提权，请在 `docker` 前加 `sudo`。工作站上的命令使用自己的用户执行。

| 步骤 | 时间 |
| --- | --- |
| [0. 需要准备什么](#0-what-you-need) | 准备机器、域名、测试群和 Agent 工作站 |
| [1. 安装中心](#1-install-the-centre) | 3 分钟，其中脚本运行 1 到 2 分钟 |
| [2. 第一个检测点](#2-the-first-probe) | 两个检测点共 5 分钟 |
| [3. 第一台机器](#3-the-first-machine) | 4 分钟 |
| [4. 首个账号、公开状态和 Agent](#4-the-first-agent) | 接入客户端约 5 分钟，首个监控需再等一个检测周期 |
| [5. 测试群、唤醒 Agent 和恢复](#5-the-first-drill) | 预留接收端和钩子配置时间，之后演练约 10 分钟 |

验收 30 分钟接入目标时，记录从开始按本指南操作到测试接收端显示恢复的总耗时，单独记录等待 DNS 生效及应用审批的时间。

## 0. 需要准备什么 {#0-what-you-need}

先说三个词：

- **中心**是运行 Beacon 的那台机器。
- **检测点**是放在用户附近的一台小机器。上面运行 `beacon-probe`，每分钟检测一次你的服务，把结果发给中心。
- **机器**指 Agent 可以通过 Beacon 登录、干活的服务器。

需要准备：

- **中心**：Linux，2 核 CPU、4 GB 内存、40 GB 磁盘；装好 [Docker Engine](https://docs.docker.com/engine/install/) 和 Docker Compose v2，以及 OpenSSH 客户端（`ssh-keygen`）。
- **两个域名**，都解析到中心：
  - 一个给人和 Agent 用：下文的 `status.example.com`。API、MCP、控制台、状态页，以及 17002 端口上的 SSH 通道，都在这个域名上。
  - 一个给机器用：`metrics.example.com`，检测点上报数据的网关。
- **开放的端口**。80 和 443 端口要能从公网访问，申请证书和日常访问都要用。17002 端口要能从 Agent 所在的地方访问。
- **一个邮箱地址**，Let's Encrypt 会往这里发证书相关的通知。必须是真实地址：安装脚本不接受 `example.com` 的邮箱。
- **一台小机器做第一个检测点**：任何带 systemd 的 Linux，x86_64 或 arm64，有 64 MB 空闲内存即可。最好是放在用户所在地区的 VPS。第二个检测点可以直接用中心。
- **一台给 Agent 干活的机器**，中心能连到它的 SSH 端口。用检测点那台也可以。
- **一个 Agent**，装在工作站上，工作站要有浏览器，用来给它登录（[第 4 步](#4-the-first-agent)）。要自动唤醒 Agent，请准备一个中心可访问的接收器，并确保 Agent 的命令行程序能以接收器的 Linux 账户运行。同机示例使用 `curl`、`sha256sum` 和 `openssl`。
- **人工通知的测试目的地**。准备 [Slack 传入 Webhook](../integrations/slack.md)，或用接收端将[带签名的通知 Webhook](../integrations/notification-webhook.md)转发到测试群。使用飞书或 Lark 应用时，先准备将机器人加入群聊，并审批应用版本和回调订阅。

## 1. 安装中心 {#1-install-the-centre}

3 分钟。

**取得参考部署**。在中心上取得参考部署，看一下它指定的版本：

```bash
git clone https://github.com/openlinkos/beacon.git
sudo cp -r beacon/deploy /opt/beacon && cd /opt/beacon
grep BEACON_VERSION .env.example
```

初次安装请保留 `BEACON_VERSION` 指定的版本。这个版本与随附的规则和检测点安装脚本配套。安装脚本首次运行时会把 `.env.example` 复制为 `.env`。之后要更换版本，请按[升级指南](upgrade.md)操作。

**运行安装脚本：**

```bash
sudo ./install.sh
```

脚本会询问两个域名和邮箱地址。想无人值守地安装，就把它们放在环境变量里：`sudo env STATUS_HOST=… METRICS_HOST=… ACME_EMAIL=… ./install.sh`。

剩下的事脚本自己做：

- 把 `.env.example` 复制为 `.env`；
- 生成密钥：规则引擎用的令牌，以及 SSH 通道的主机密钥和 CA 密钥，这些都不会离开这台机器；
- 把你的域名填进 `config.yaml` 和检测目标；
- 用各自的镜像逐个校验文件；
- 拉取 Beacon、VictoriaMetrics、vmalert 和 Caddy 四个镜像并启动；
- 一直等到四个容器都健康、规则引擎的第一条告警已经送到 Beacon。

在网络很好的主机上实测用了 69 秒。2 核机器加普通带宽，按 1 到 2 分钟算，大部分时间花在拉镜像上。

最后应该看到这样的输出：

```
configuration OK: Feishu app false, Pushover false, Bark false; gateway ":17006"; SSH channel :17002; 0 agent hooks
files: 1 probe locations, 0 heartbeats, 0 resources, 3 probe targets in 2 sets, 6 modules, 1 status pages
database: none yet at /var/lib/beacon/beacon.db; the service imports the files when it first starts
Waiting for the containers to be healthy …

Beacon is up (69s); made the secrets: rules-token ssh-host-key ssh-ca-key.
  https://status.example.com          the API (/api/v1), MCP (/mcp) and pages; the first certificate takes about a minute
  https://metrics.example.com         the gateway for machines
  status.example.com:17002            the SSH channel
```

后面还会列出下一步要做的事，也就是这篇指南接下来的几步。

**检查两个域名**，在任意地方都可以：

```bash
curl https://status.example.com/healthz          # ok
curl https://metrics.example.com/api/v1/targets  # {"code":"auth.required",…}：没带令牌就被拒绝，这是对的
```

然后在浏览器里打开 `https://status.example.com/`。看到的是 `catalog/status_pages.yaml` 里的示例状态页，检测点上报数据之前，它显示 **No data yet**。

现在还不会通知任何人。没有配置聊天工具时，告警只记录下来，在 API、MCP 和控制台里能看到。接下来几步用这些就够了。

如果结果不一样：

- **输出 `install.sh: …` 后停下**：提示会写明缺了什么（root 权限、Docker、Compose v2、`ssh-keygen`、真实的邮箱、两个不同的域名）。或者写明哪个文件没通过校验，并给出查看原因的命令。
- **`curl` 报 TLS 错误**：启动后第一张证书最多要等一分钟。如果还是失败，检查两个域名是否都解析到中心、80 和 443 端口是否开放。`docker compose logs caddy` 能看到 Let's Encrypt 的回复。

部署里每个文件放在哪里，见 [deploy/README.md](https://github.com/openlinkos/beacon/blob/main/deploy/README.md)。

## 2. 第一个检测点 {#2-the-first-probe}

两个检测点共 5 分钟。

Beacon 靠多个检测点的一致判断，来区分真正的故障和偶尔的抖动。至少两个检测点检测失败，并且失败的至少占有数据的检测点的一半，才算服务故障。所以第一个检测点带来数据，第二个检测点才带来告警。

**登记第一个检测点**。目录里已经有一个检测点 `edge-1`。目录就是 `catalog/locations.yaml`，在首次启动时导入 Beacon 的数据库。在中心上登记这个检测点，把只打印一次的令牌保存到权限受限的临时文件。

```bash
edge_1_token=$(mktemp)
docker exec beacon beacon enroll probe edge-1 > "$edge_1_token"
```

应该看到 `enrolled probe-edge-1 (probe:write, expires …); the token above is not shown again`。`$edge_1_token` 指向的临时文件里是一行以 `lbt_` 开头的令牌。

**安装检测程序**。通过 SSH 将 `/opt/beacon/probe/` 目录和 `$edge_1_token` 指向的文件复制到检测点机器上只有你能访问的目录。将令牌文件保存为 `edge-1.token`，在该目录下以 root 执行以下命令。

```bash
sh probe/install.sh -gateway https://metrics.example.com -token-file edge-1.token
```

大约 10 秒后，最后输出：

```
edge-1: beacon-probe beacon …; installed: token agent.env beacon-probe beacon-probe.service beacon-heartbeat; 13 MB resident
the gateway at https://metrics.example.com accepted the first push; the location's results arrive every minute
```

确认首次推送成功后，删除中心上的临时文件和传到检测点的副本。检测程序使用已安装在 `/etc/beacon-probe/token` 的令牌。

从此这个检测点每分钟检测一次网关交给它的目标。以后改检测内容，都不用再动检测点。

**添加第二个检测点**。可以使用中心或另一台小机器。systemd 安装脚本在每台主机上安装一个检测程序。在中心运行 `sudoedit /opt/beacon/catalog/locations.yaml`，在 `edge-1` 下添加：

```yaml
  - id: edge-2
    name: Germany - Frankfurt      # 公开显示：写地点和网络，不写厂商
    region: overseas
    network: BGP
```

**导入文件，登记新检测点**。把文件导入 Beacon：先预演一遍看会改什么，再正式导入。然后登记新的检测点：

```bash
docker exec beacon beacon config import /etc/beacon -dry-run
docker exec beacon beacon config import /etc/beacon -note "second probe location"
edge_2_token=$(mktemp)
docker exec beacon beacon enroll probe edge-2 > "$edge_2_token"
```

预演应该在 `would change 1 items (nothing changed):` 下面列出一行 `+ location edge-2`。正式导入则在 `version 2: import of 1 items` 下面列出同样一行。

**安装第二个检测程序**。如果使用另一台机器，将 `probe/` 目录传过去，并把第二个令牌存为 `edge-2.token`。使用前面的 HTTPS 安装命令，令牌参数换成该文件。如果装在中心本机，就在同一个终端用回环地址安装。

```bash
sudo sh probe/install.sh -gateway http://127.0.0.1:17006 -token-file "$edge_2_token"
```

确认第二个检测点推送成功后，删除临时令牌文件和传过去的副本。

导入会用文件替换目录、目标集和检测模块。请先完成这次导入，再添加下文的监控和公开组件。以后导入前，先用 `-dry-run` 检查是否会覆盖文件中没有的控制台、API 或 MCP 修改。

- 两种修改配置的方式，以及怎样让它们保持一致，见[检测一个服务](../guide/monitoring.md)。
- 检测点放在哪里、机器需要什么、安装不顺利时怎么办，见[检测点位置](probes.md)。

**看看数据到了没有**。VictoriaMetrics 自带的界面在中心的 `http://127.0.0.1:8428/vmui`。在工作站上，用 `ssh -N -L 8428:127.0.0.1:8428 centre` 打开。`probe_success` 每个检测点、每个目标各有一条序列，成功为 1。`https://status.example.com/` 上的公开页面现在也显示出各组件的状态了。

## 3. 第一台机器 {#3-the-first-machine}

4 分钟，添加一台给 Agent 干活的机器。

**添加机器**。在中心上执行：

```bash
docker exec beacon beacon machine add edge-1 edge-1.example.net -note "the first probe machine"
```

Beacon 会连接这台机器一次，固定它的主机密钥。以后这台机器出示别的密钥，Beacon 一律拒绝。然后它打印机器上要做的配置：

```
added edge-1 at edge-1.example.net:22 as beacon; its host key is SHA256:…
On the machine, as root, if not done yet:
  echo 'ssh-ed25519 AAAA… beacon-ca' > /etc/ssh/beacon_ca.pub
  echo 'TrustedUserCAKeys /etc/ssh/beacon_ca.pub' > /etc/ssh/sshd_config.d/beacon.conf
  id beacon >/dev/null 2>&1 || useradd -m -s /bin/bash -p '*' beacon
  systemctl reload ssh
(see docs/guide/machines.md for sudo and for PerSourcePenaltyExemptList)
```

**在机器上以 root 身份**执行 `machine add` 打印出来的前三行命令。它们配置为每次连接签发短期证书的 CA，并创建证书里指定的账户。确认 `sshd_config` 包含 `/etc/ssh/sshd_config.d/*.conf`，再运行 `sshd -t`。验证通过后，在 Debian 或 Ubuntu 上重新加载 `ssh`，RHEL 等发行版则重新加载 `sshd`。

如果 `machine add` 报 `the machine cannot be reached`，说明中心连不上这台机器的 SSH 端口。在机器的防火墙里放行。如果机器在 NAT 后面，就按 [NAT 后面的机器](../guide/machines.md#machines-behind-nat)反过来添加。

还有两项配置最好现在就做：

- **`beacon` 账户能做什么，才是真正的边界**。只给它干活需要的权限，比如在 `/etc/sudoers.d/beacon` 里写：`beacon ALL=(root) NOPASSWD: /usr/bin/systemctl restart beacon-probe, /usr/bin/journalctl`。
- **让中心的地址免受 OpenSSH 的连接失败惩罚**，前提是机器支持 `PerSourcePenaltyExemptList`（9.8 引入）。在 `beacon.conf` 中添加 `PerSourcePenaltyExemptList 203.0.113.7`，将示例地址换成中心的来源地址，再验证配置并重新加载 sshd。

其余内容见[给 Agent 一台机器](../guide/machines.md)。

## 4. 第一个 Agent {#4-the-first-agent}

先在中心运行 `docker exec beacon beacon setup`，在工作站上打开它打印的一次性链接，创建所有者账号。如果中心已有所有者，请打开 `https://status.example.com/console`，用该账号登录。

### 选择对用户公开的服务 {#choose-what-users-see}

1. 在**监控**中点**添加 HTTP 监控**。填写服务名和两个检测点均可访问的正常 HTTP 或 HTTPS 端点。选择**所有检测点**和 `http_2xx` 检测模板，再保存。新增监控默认为私有。等一个检测周期后点**刷新结果**，核对两个检测点都有结果。端点和检查选项见[添加 HTTP 监控](../guide/monitoring.md#add-an-http-monitor)。
2. 在**状态页**中打开现有的 `status` 页面，点**添加组件**。填写组件 ID（如 `api`）和公开标题，然后选中刚添加的服务。点**预览变更**，核对公开的服务列表，以及将公开的事件进展或维护公告。预览期间，在未登录的浏览器窗口访问 `https://status.example.com/status`，核对访客看到的页面。
3. 点**发布变更**，刷新未登录的页面，核对新组件。使用**编辑服务**更改组件展示的服务，或用**移除组件**移除示例组件。每次修改或移除都先预览，再确认。至少保留一个组件，别将 `drill` 加入页面。这些操作会更新[公开状态页](../guide/status-pages.md)，检测点仍会继续检测服务。

如果需要新增一个单独的页面，或你的部署还没有状态页，请在包含**创建状态页**功能的版本中按[创建公开状态页](../guide/status-pages.md#create-a-public-page)操作。在本指南的参考部署中，新页面使用自己的专属地址，示例页继续作为默认页。

### 接入个人账号和 Agent {#connect-yourself-and-the-agent}

**从 Beacon 检查机器 SSH。** 在提供[检查机器 SSH](../guide/machines.md#check-machine-ssh)的版本中，打开**机器 → edge-1 → 通过 SSH 连接**并运行检查。就绪表示固定主机密钥匹配，且机器接受了 Beacon 的证书。

**验证个人 SSH。** 按[以自己的身份连接](../guide/machines.md#connect-as-yourself)登记个人公钥，再到 `edge-1` 执行 `hostname`。在**会话**中回放，确认操作人是你的用户名。

**接入 Agent 的 MCP 客户端。** 如果你用 Codex，请在工作站按 [Codex 的 MCP 登录流程](../integrations/codex.md)操作。使用 Claude Code 时，在工作站上添加 Beacon，通过浏览器登录。

```bash
claude mcp add --scope user --transport http beacon https://status.example.com/mcp
claude mcp login beacon
```

也可以在 Claude Code 里通过 `/mcp` 选择 `beacon` 完成登录。在 Beacon 的授权页选择**值班**预设，并选定 `edge-1`。请 Agent 调用 Beacon 的 `whoami`，然后通过 `run_command` 在 `edge-1` 上执行 `hostname`。在**会话**中打开回放，确认操作人显示为 `人 / Agent`。这项检查会用到 Beacon 的 SSH 通道，MCP 客户端无需登记 SSH 密钥。要让 Agent 直接使用 `ssh` 和 `scp`，请[登记独立的 Agent 密钥](../integrations/claude-code.md#connect-with-your-own-ssh-scp-and-rsync)。

- 角色和邀请成员，见[邀请你的团队](../guide/people.md)。
- Codex、其他客户端和用令牌的无头 Agent，见[接入](../integrations/index.md)。

## 5. 第一次演练 {#5-the-first-drill}

**接入一个测试接收端。** 使用本指南新建的实例或独立测试实例。在 `/opt/beacon/config.yaml` 中为 `escalation.critical.channel` 选择要验收的渠道。使用 `slack` 时配置 [Slack 传入 Webhook](../integrations/slack.md)，使用 `notification_webhook` 时[配置接收端](../integrations/notification-webhook.md)转发到测试群，使用 `app` 时[配置飞书或 Lark 应用](../integrations/feishu.md)及测试群的 `feishu.chat_id`。将凭据保存在 `/opt/beacon/secrets/` 下的文件中，确保测试者能访问 `public_url`。完成下一步后，一起应用群聊和唤醒配置。

### 在中心上准备第一次唤醒 {#prepare-a-first-wake-on-the-centre}

[安装与中心匹配的 `beacon-oncall` 版本](../integrations/headless.md#install-the-matching-linux-release)。以下示例使用私有 Docker 网桥和 Claude Code。也可以[使用自己的接收器或 Agent](../guide/on-call.md#wake-an-agent)。

**准备账户和凭据。** 将 `alice` 换成第 4 步创建的 Beacon 用户名。将 Agent 的权限限定在已登记的测试机器 `edge-1`。接收程序和 Agent CLI 将以独立的 Linux 用户 `beacon-oncall` 运行。

```sh
(
  set -eu
  umask 077
  id beacon-oncall >/dev/null 2>&1 || sudo useradd --system --user-group --home-dir /var/lib/beacon-oncall --create-home --shell /usr/sbin/nologin beacon-oncall
  sudo install -d -m 0750 -o beacon-oncall -g beacon-oncall /var/lib/beacon-oncall/logs
  sudo install -d -m 0750 -o root -g beacon-oncall /etc/beacon-oncall
  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT
  openssl rand -hex 32 > "$work/hook-secret"
  docker exec beacon beacon agent add reference-drill -owner alice -preset oncall -machines edge-1 -expires 1h > "$work/beacon-token"
  docker exec beacon beacon token create reference-drill-reporter -scopes alerts:report -expires 1h > "$work/run-report-token"
  sudo install -m 0400 -o 65532 -g 65532 "$work/hook-secret" /opt/beacon/secrets/reference-drill-hook
  sudo install -m 0640 -o root -g beacon-oncall "$work/hook-secret" "$work/beacon-token" "$work/run-report-token" /etc/beacon-oncall/
)
```

**登录你的模型提供方。** 使用上面创建的 Linux 用户设置 Claude Code 或 Codex。集成指南有两种 CLI 的登录与状态命令，并说明如何把 API 密钥传给前台接收程序。[以接收程序的用户登录](../integrations/headless.md#sign-in-as-the-receiver-user)

**检查首次演练的凭据。** 使用带 `beacon-oncall check` 的匹配版本（rc.44 或更新版本）。地址使用 Beacon 的 `public_url`。回报方名称使用钩子配置 `reporter` 中 `token:` 后面的部分。以运行接收程序的 Linux 用户执行。

```sh
sudo -u beacon-oncall -H /usr/local/bin/beacon-oncall check \
  -url https://status.example.com -owner alice -agent reference-drill -machine edge-1 \
  -agent-token-file /etc/beacon-oncall/beacon-token \
  -reporter reference-drill-reporter -report-token-file /etc/beacon-oncall/run-report-token
```

检查会从中心读取当前的 Agent 和回报凭据。它确认 Agent 属于指定的人、具备演练所需权限，并且仅限已登记的测试机器。回报使用独立的令牌，权限仅为 `alerts:report`。命令还会检查 SSH 通道是否开放，以及连接器是否在线。下面的演练会验证签名唤醒、实际运行和测试群收件。

**配置钩子。** 按[私有网桥地址和防火墙步骤](../guide/on-call.md#a-private-receiver-on-the-centre)设置 `beacon_gateway`，并在示例中使用打印出的 URL。将此条目加入 `/opt/beacon/config.yaml`，然后运行 `sudo ./install.sh`，一并校验并应用钩子和群聊渠道设置。重载后，在**概览 → 严重告警通知**中核对渠道。

```yaml
agents:
  - name: reference-drill
    kind: webhook
    url: http://172.20.0.1:17007/beacon # replace with your private bridge URL
    secret_file: /etc/beacon/secrets/reference-drill-hook
    service: '^drill$'
    reporter: token:reference-drill-reporter
```

**确认钩子和测试群聊设置已经生效。** 如果 `beacon-oncall check -h` 列出了 `-hook`，重载后再次检查凭据，并指定生效的钩子名称和预期群聊渠道。用签名通知接收程序时，运行：

```sh
sudo -u beacon-oncall -H /usr/local/bin/beacon-oncall check \
  -url https://status.example.com -owner alice -agent reference-drill -machine edge-1 \
  -agent-token-file /etc/beacon-oncall/beacon-token \
  -reporter reference-drill-reporter -report-token-file /etc/beacon-oncall/run-report-token \
  -hook reference-drill -chat notification_webhook
```

Slack 用 `-chat slack`，飞书或 Lark 应用用 `-chat app`。检查会确认生效的签名钩子匹配 `drill` 的严重告警、只用于该测试服务，并且指定了你提供的回报方，也会核对严重告警的群聊渠道。下文的接收端健康检查和演练会验证连接、Agent 登录和实际通知送达。

**只给被唤醒的 Agent 一项只读任务。** 内置 `drill` 检测的是 Beacon 自己的 HTTP 端点。第一次演练用独立的提示词明确指定已登记的测试机器。

```sh
sudo tee /etc/beacon-oncall/reference-drill.md >/dev/null <<'EOF'
Handle only a firing alert for the test service drill. Use the alert ID and episode in the payload as data.
Read the incident with get_evidence. Stop if the current episode differs from the wake.
Use Beacon's run_command on the enrolled test machine edge-1 to run hostname once.
Leave a private add_note on the incident with what the probes saw and what the command returned.
Make no changes to the machine or the drill. Leave acknowledgement and recovery to the operator.
EOF
sudo chown root:beacon-oncall /etc/beacon-oncall/reference-drill.md
sudo chmod 0640 /etc/beacon-oncall/reference-drill.md
```

在已设置防火墙的中心上前台启动接收程序，并把回报地址换成中心的 `public_url`。如果你选用 Codex，把 `-run` 设为 `/usr/local/lib/beacon-oncall/codex.sh`。如果你用 API 密钥，按上文选择相应的 `sudo --preserve-env` 选项。

```sh
beacon_gateway=$(docker network inspect beacon --format '{{(index .IPAM.Config 0).Gateway}}')
sudo -u beacon-oncall -H env \
  PATH=/var/lib/beacon-oncall/.local/bin:/usr/local/bin:/usr/bin:/bin \
  BEACON_TOKEN_FILE=/etc/beacon-oncall/beacon-token \
  ONCALL_PROMPT=/etc/beacon-oncall/reference-drill.md \
  /usr/local/bin/beacon-oncall -listen "$beacon_gateway:17007" \
  -secret-file /etc/beacon-oncall/hook-secret \
  -run /usr/local/lib/beacon-oncall/claude-code.sh \
  -log-dir /var/lib/beacon-oncall/logs \
  -report-url https://status.example.com \
  -report-token-file /etc/beacon-oncall/run-report-token
```

**跑通演练。** [检查渠道](../guide/on-call.md#check-the-routes-before-the-drill)，包括从 Beacon 所在网络访问接收器健康端点。确认两个检测点都在上报、演练端点健康，然后[启动演练](../guide/drills.md)。

1. 在自己的测试群打开通知，登录后核对链接指向同一条 `drill` 故障。认领并备注。即使 Beacon 已记录提供方接受了通知，也要在目的群里核对消息。
2. 在同一故障页分别检查钩子是否接受请求，以及 Agent 程序的退出结果。打开**会话**，回放 `alice / reference-drill` 在 `edge-1` 上执行的 `hostname`，并核对故障里的 Agent 备注。
3. 结束演练，核对检测结果、已恢复的故障，以及测试群里的恢复消息或飞书卡片更新。随后停止前台接收程序，移除测试钩子，并撤销测试 Agent 和回报令牌。

手机推送和备用通知按[通知渠道专项演练](../guide/drills.md#verify-phone-delivery-and-handover)另行验收。

## 接下来 {#what-next}

- [使用 Beacon](../guide/using-beacon.md)按能力介绍其余运维任务。
- [升级](upgrade.md)、[备份与恢复](backup.md)和[安全模型](security.md)让它一直跑下去。
- 上面某一步没有按预期发生时，见[常见问题](troubleshooting.md)。
- [目标](../about/goals.md)：Beacon 为什么而做，以及它不做什么。
