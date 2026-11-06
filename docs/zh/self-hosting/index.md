---
sourceHash: sha256-b8581780
---

# 部署形态

Beacon 是自托管的：一个**中心**，成员、Agent 和机器都跟它打交道；若干**检测点**，放在用户所在的地方；还有 Agent 工作的**机器**。运行它有三种形态，层层递进：先在本机把整个闭环试一遍，再把同一套东西搬到一台服务器上，然后按需增加检测点和机器。

| 形态 | 用途 | 方式 |
| --- | --- | --- |
| [本机试用](../guide/quickstart.md) | 10 分钟内看完整闭环：服务坏了，告警叫醒 Agent，Agent 通过 SSH 通道修好它，各检测点复测确认 | `deploy/try/`：一个 compose 文件，含 Beacon、VictoriaMetrics、vmalert、一个检测点和一台带 sshd 和小网页服务的演示机器。不需要域名和证书，`http://localhost:17001` |
| [单机生产](install.md) | 正式使用 | `deploy/` 里的参考部署：`install.sh` 在两个域名后面启动 Beacon、VictoriaMetrics、vmalert 和 Caddy，证书由 Let's Encrypt 签发 |
| [检测点与机器](probes.md) | 扩展 | 在用户所在的任何一台小 Linux 上运行 `deploy/probe/install.sh`；机器从中心登记接入，直连中心，或在 NAT 后反向连出 |

本页其余部分说明每种机器需要什么、涉及哪些端口；具体步骤在本区各页。

## 每个部分需要什么 {#what-each-part-needs}

| 部分 | 需要 |
| --- | --- |
| 中心 | Linux，2 核 CPU、4 GB 内存、40 GB 磁盘；Docker Engine 和 Compose v2；OpenSSH 客户端（`ssh-keygen`）。整套系统空闲时约占 200 MB 内存；参考 compose 文件给四个容器的内存上限分别是 256 MB（Beacon）、1 GB（VictoriaMetrics）、256 MB（vmalert）、128 MB（Caddy） |
| 检测点 | 任何带 systemd 的 Linux，x86_64 或 arm64，有 64 MB 空闲内存。检测程序常驻内存约 15 到 20 MB，systemd 单元给它的软上限 40 MB、硬上限 64 MB。它不监听任何端口：只向网关发起 HTTPS 连接，自身状态在 `127.0.0.1:17009` 上查看 |
| Agent 工作的机器 | 任何中心能连到 SSH 端口的服务器。`beacon machine add` 给出的四行命令让它的 sshd 信任 Beacon 的 CA；NAT 后的机器改为向外连接（[NAT 后面的机器](../guide/machines.md#machines-behind-nat)） |
| 试用环境 | 一台装了 Docker 的笔记本电脑；见[在本机试一试](../guide/quickstart.md) |

## 端口 {#ports}

参考部署在中心上开放的端口如下。Beacon 自己的三个监听地址都可以在 `config.yaml` 里改（`listen`、`gateway.listen`、`ssh.listen`，见[配置](configuration.md)），compose 文件则决定其中哪些离开这台机器。

| 端口 | 谁在应答 | 从哪里访问 | 参考部署中的状态 |
| --- | --- | --- | --- |
| 80 和 443，TCP（443 还有 UDP，用于 HTTP/3） | Caddy：申请证书，并为两个域名提供 HTTPS | 公网 | 发布 |
| 17001 | Beacon 本身：公开状态页、`/console` 控制台、`/api/v1`、MCP `/mcp`、`/healthz` | 成员和 Agent，经 Caddy 在人用的域名上 | 只绑定 127.0.0.1 |
| 17002 | SSH 通道：Agent 的 `ssh`、`scp`、`rsync` | Agent 所在的任何地方 | 原样发布，纯 SSH |
| 17006 | 机器的网关：检测点推送结果、拉取目标，心跳上报，规则引擎和 Flux 发送告警与变更事件 | 机器，经 Caddy 在机器用的域名上；装在中心本身的检测点用 `127.0.0.1:17006` | 只绑定 127.0.0.1 |
| 8428 | VictoriaMetrics，自带界面在 `/vmui` | 仅中心 | 只绑定 127.0.0.1 |
| 8880 | vmalert，规则引擎的界面 | 仅中心 | 只绑定 127.0.0.1 |

检测点不开任何端口。Agent 工作的机器需要让中心连到它的 SSH 端口（NAT 后则向外连接）。

## 接下来去哪里 {#where-to-go-from-here}

- [在本机试一试](../guide/quickstart.md)：10 分钟，在一台机器上跑完整闭环。
- [装在服务器上](install.md)：中心、前两个检测点和第一台机器，约 12 分钟。
- [检测点位置](probes.md)：检测点放在哪里、怎样安装。
- [配置](configuration.md)：`config.yaml`、密钥文件，以及改动怎样作用到运行中的 Beacon。
- [升级](upgrade.md)、[跑在 PostgreSQL 上](postgres.md)、[备份与恢复](backup.md)、[安全模型](security.md) 和[常见问题](troubleshooting.md)，各一页。
