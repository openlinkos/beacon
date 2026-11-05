---
sourceHash: sha256-6310790e
---

# 检测点位置

检测点是运行 `beacon-probe` 的一台小机器，放在用户所在的地方。它每分钟向中心询问要检测什么，检测完把结果推送回去。本页说明检测点放在哪里值得、机器需要什么、怎样安装和更新检测程序。检测点检测什么、怎么检测，是另一个问题：见[检测什么，从哪里检测](../guide/monitoring.md)。

示例以[装在服务器上](install.md)里的参考部署为准，中心上的命令都通过 `docker exec beacon beacon …` 执行。

## 为什么是两个，为什么要分开 {#why-two-and-why-apart}

Beacon 靠多个检测点的一致判断来区分真正的故障和偶尔的抖动：按参考规则，一个检测点连续 3 分钟失败才算它判定服务故障；至少两个检测点失败，并且至少占上报检测点的一半，才算服务故障。单个检测点说坏了，永远不算故障。所以第一个检测点带来数据，第二个才带来告警；此后每加一个，结论更难被质疑，覆盖也更宽。

把检测点放在用户所在的地方，用他们使用的网络。同一网络上的两个检测点会共享这个网络的故障，也就会一起看错；把检测点分散到不同网络上，如果用户分布在多个国家，也分散到国家。目录给每个检测点一个 `region`（`overseas` 海外或 `mainland` 大陆），规则会比较两者：只有大陆访问不通的服务是路径问题，不是故障，告警也按路径问题发。

中心自己就可以做一个检测点，几乎不花钱：参考部署把网关开放在回环地址上，`probe/install.sh -gateway http://127.0.0.1:17006` 就能把检测程序装在服务旁边。它是个合格的第二个检测点，却不是合格的第一个：中心挂了，这个检测点也一起挂，所以至少要保持两个不在中心上的检测点。

检测点按地点和网络命名，永远不要用厂商名字：名字是公开的，显示在状态页和卡片上，比如 `Germany - Frankfurt`、网络 `BGP`。

## 机器需要什么 {#what-the-machine-needs}

任何带 systemd 的 Linux，x86_64 或 arm64，有 64 MB 空闲内存。检测程序常驻内存约 15 到 20 MB；systemd 单元给它的软上限 40 MB、硬上限 64 MB，并告诉内核内存紧张时先牺牲它，因为检测点常常和它所服务的程序挤在同一台小机器上。它不监听端口：只向网关发起 HTTPS 连接；自身状态在 `127.0.0.1:17009/metrics` 上，供类似 `journalctl` 的排查用。

时钟必须同步（chrony 或 systemd-timesyncd）：检测结果带时间戳，安装脚本发现 NTP 没开时会警告。

## 安装一个检测点 {#install-a-location}

检测点必须先在目录里，然后才能登记：登记一个目录里没有的 id 会失败，提示 `location edge-2 is not in the catalogue`。目录自带一个检测点 `edge-1`；增加更多，要么改 `catalog/locations.yaml` 再导入，要么走 API 和 MCP，两种做法都在[检测什么，从哪里检测](../guide/monitoring.md#locations)里。

在中心上登记检测点。令牌只打印这一次，之后只有网关收到的内容算数：

```bash
docker exec beacon beacon enroll probe edge-1 > edge-1.token
```

应该看到 `enrolled probe-edge-1 (probe:write, expires …); the token above is not shown again`，`edge-1.token` 里是一行以 `lbt_` 开头的令牌。把 `/opt/beacon/probe/` 目录和 `edge-1.token` 复制到检测点机器上，以 root 执行：

```bash
sh probe/install.sh -gateway https://metrics.example.com -token-file edge-1.token
```

脚本安装与这份参考部署相配的 `beacon-probe` 版本（检测程序和服务一起发布；用 `-version` 可以指定别的版本，访问不了 GitHub 的机器可以用 `-binary FILE` 安装你自己下载的文件）。它会创建 `beacon-probe` 用户，按机器架构下载检测程序，用发布页的 `SHA256SUMS` 核对，把令牌装成只有这个用户能读，装好 systemd 单元 `beacon-probe` 和 `beacon-heartbeat` 小工具，启动检测程序，然后等网关收下第一次推送。大约 10 秒，最后输出：

```
edge-1: beacon-probe beacon 0.3.0-rc.16 …; installed: token agent.env beacon-probe beacon-probe.service beacon-heartbeat; 13 MB resident
the gateway at https://metrics.example.com accepted the first push; the location's results arrive every minute
```

### 结果不是这样的时候 {#when-it-does-not-end-like-that}

如果脚本以 `no push accepted by … within 150 seconds` 结束，在检测点机器上运行 `journalctl -u beacon-probe -n 50`：

- `401` 说明令牌文件不是 `enroll` 打印的那个；重新登记一次，复制新文件过去。
- `403` 加 `location.inactive` 说明目录里没有这个检测点（或者它对应的 `vps` 资源已过期）；补上目录并导入。
- 连接错误说明这台机器连不上网关域名的 443 端口。

关于时钟的警告也要处理，原因如上。

## 装好之后 {#after-the-install}

- **检测目标来自中心。** 检测程序每分钟拉取网关交给它的目标，所以改检测内容或方式，都不用再动检测点。
- **更新检测程序**：复制一份新的 `probe/` 目录，再执行一遍同样的命令，`-version vX.Y.Z` 指定版本；脚本是幂等的，只替换有变化的部分。检测程序和服务一起发布，新取的 `deploy/` 里的版本与中心相配。
- **慢路径或易断的连接**是检测点自己的属性：目录里的 `probe.timeout` 只为这一个检测点放宽所有模块的超时，`probe.retries` 让没收到应答的检测立刻重试一次（[检测点](../guide/monitoring.md#locations)）。
- **机器上的定时任务**，比如备份，结束时执行 `beacon-heartbeat <name>`（安装脚本已经把它装好）；其余见[心跳与到期资源](../guide/heartbeats.md)。
- **检测程序自己的状态**在检测点机器的 `127.0.0.1:17009/metrics` 上：最近一次推送、失败、网关连不上时暂存结果的磁盘队列。
