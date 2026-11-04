---
sourceHash: sha256-593651cd
---

# 升级

把一个运行中的中心从一个版本挪到下一个，再退回来：改 `.env` 里的一行，跑一�?`install.sh`——脚本拉取新镜像、用它校验所有文件、重�?Beacon 容器、等待健康。状态、密钥和配置都留在原地。示例以[装在服务器上](install.md)的参考部署为准，在中心的 `/opt/beacon` 下执行�?
## 升级改什么，留什�?{#what-an-upgrade-touches-what-it-keeps}

| 改变�?| 不变�?|
| --- | --- |
| `beacon` 容器，按 `.env` �?`BEACON_VERSION` 指名的镜像（`ghcr.io/openlinkos/beacon:vX.Y.Z`）重�?| `beacon-data` 卷里�?`status.db` �?`recordings/`：账号、Agent 和令牌，目录、检测目标和模块（都在这个数据库里）、告警组、审计日�?|
| VictoriaMetrics、vmalert �?Caddy，只在你换用新的 `compose.yaml` 时：它们的镜像按摘要固定，随仓库一起变�?| `/opt/beacon`：`config.yaml`、`secrets/`、`catalog/`、`targets/` 是你的，升级不写它们 |
| | `victoriametrics-data` �?`caddy-data` �?|

Beacon 没有要跑的数据库迁移：它打开同一�?`status.db`，缺什么补什么。中断就是容器重建的那几秒，会结束还开着�?SSH 会话（它们做过的事记录到那一刻为止；见[通道保证什么，不保证什么](../guide/machines.md#what-the-channel-guarantees-and-what-it-does-not)）。网关停着的这段时间，每个检测点把结果暂存在磁盘上（最�?64 MB），网关一恢复就补发，检测结果一条不丢。告警组能扛过重启，恢复�?`list_alerts` 里触发中的告警还在�?
## 挑一个版�?{#pick-a-version}

- [更新日志](../changelog.md)说明每个版本改了什么；[发布页](https://github.com/openlinkos/beacon/releases)列出所�?tag。候选版本带 `-rc.N`�?- 只有最新的次版本及其候选版本会收到修复（[安全政策](https://github.com/openlinkos/beacon/blob/main/SECURITY.md)写明）；请升到最新的 tag，不要停在旧的次版本上�?
## 升级中心 {#upgrade-the-centre}

1. **先备份状�?*：[备份与恢复](backup.md#back-up)里的三条命令，一分钟的事。升级之所以是个安心的动作，正因为你随时可以退回去�?2. **取这个版本的部署文件**（建议做：固定的镜像摘要、规则和处理手册都随仓库变动）。先留一�?`/opt/beacon` 以便退回，然后在安装时的克隆里�?
   ```bash
   sudo cp -a /opt/beacon /opt/beacon.bak
   cd beacon
   git fetch --tags && git checkout vX.Y.Z        # 或者：git pull，跟着 main �?   sudo cp -a install.sh compose.yaml Caddyfile .env.example /opt/beacon/
   sudo cp -a metrics rules runbooks probe /opt/beacon/
   ```

   `config.yaml`、`catalog/` �?`targets/` 是你的，不会被覆盖。如果这个版本改�?`config.yaml` 本身，`git diff vOLD..vX.Y.Z -- deploy/config.yaml` 会显示改了什么；缺的新键自己补进去。新版本要的键你的文件里没有时，安装脚本的校验会先停下来说明，运行中的服务不会被碰�?3. **提高版本�?*，改 `.env`�?
   ```bash
   cd /opt/beacon
   sudo sed -i 's/^BEACON_VERSION=.*/BEACON_VERSION=vX.Y.Z/' .env
   ```

4. **运行安装脚本**，它随时可以重复运行�?
   ```bash
   sudo ./install.sh
   ```

   换了�?tag，它会拉取新镜像，用新镜像校�?compose 文件、Caddyfile、规则和 Beacon 自己的配置——哪个文件不通过，升级就停在那里，运行中的服务毫发无损——然�?`docker compose up -d` 重建 Beacon 容器，再等四个容器全部健康、规则引擎的第一条告警到达网关。结尾你会看�?`Beacon is up (…s)`，和第一次安装一样�?
5. **检查�?* 版本、容器和状态：

   ```bash
   docker exec beacon beacon version   # beacon vX.Y.Z (�?
   docker compose ps                   # 四个容器，healthy
   docker exec beacon beacon config history
   ```

   `config history` 列出你熟悉的那些版本号，这说明数据库还在。想完整看一遍链路，可以跑一次演练（`docker exec beacon beacon drill fail 15m`，见[演练一次故障](../guide/drills.md)）�?
## 先中心，后检测点 {#centre-first-then-the-probes}

探针协议在两个方向上都跨版本：旧�?`beacon-probe` 对新的中心照常工作，新的探针对旧的中心只是用不上它没听说过的功能。所以先升级中心，检测点什么时候方便什么时候升：[检测点位置](probes.md)有那条命令（`sh probe/install.sh -gateway �?-version vX.Y.Z`，可重复运行，只有二进制变了才重启服务）�?
## 回滚 {#roll-back}

1. 把版本号改回去，再运行一次安装脚本：

   ```bash
   sudo sed -i 's/^BEACON_VERSION=.*/BEACON_VERSION=vOLD/' .env
   sudo ./install.sh
   ```

2. 数据库不是障碍：Beacon 没有迁移，新版本写下的记录里，旧版本只是读不到自己不认识的字段。退回去时什么都不会被改写�?3. 如果�?2 步还换了新的部署文件，把当时留下的那份拷回去：`sudo cp -a /opt/beacon.bak/. /opt/beacon/`�?4. 再不行，[从备份恢复](backup.md#restore)�?
## 接下�?{#what-next}

- [备份与恢复](backup.md)：备份什么、多久一次、怎么倒回去�?- 升级没有�?`Beacon is up` 结束时，看[常见问题](troubleshooting.md)�?