---
sourceHash: sha256-55c29493
---

# 升级

把一个运行中的中心从一个版本挪到下一个，再挪回来，就是 `.env` 里的一行加一次 `install.sh`：脚本拉取新镜像，用它校验所有文件，重建 Beacon 容器，然后等它健康。状态、密钥和配置都留在原地。示例以[装在服务器上](install.md)的参考部署为准，在中心的 `/opt/beacon` 下执行。

## 升级改什么，留什么 {#what-an-upgrade-touches-what-it-keeps}

| 改变 | 不变 |
| --- | --- |
| `beacon` 容器，按 `.env` 里 `BEACON_VERSION` 指名的镜像（`ghcr.io/openlinkos/beacon:vX.Y.Z`）重建 | `beacon-data` 卷里的 `beacon.db` 和 `recordings/`：账号、Agent 和令牌，目录、检测目标和模块（它们就在那个数据库里），告警组，审计日志 |
| VictoriaMetrics、vmalert 和 Caddy，只在你换用新的 `compose.yaml` 时：它们的镜像按摘要钉住，跟仓库一起走 | `/opt/beacon`：`config.yaml`、`secrets/`、`catalog/`、`targets/` 是你的；升级不写它们 |
| | `victoriametrics-data` 和 `caddy-data` 两个卷 |

Beacon 没有要你手工执行的迁移：它打开同一个 `beacon.db`，把缺的部分补上。**从还没有 SQL 数据库的版本升级时**（0.3.0-rc.16 及更早的版本把状态存在 bbolt 文件 `status.db` 里），首次启动会在一个事务里把这个文件迁进 `beacon.db`，提交前核对每个桶的条数，并把旧文件改名为 `status.db.pre-sql`；参考部署那样的状态，用时以毫秒计。要退回：停掉 Beacon，把 `status.db.pre-sql` 改回 `status.db`，删掉 `beacon.db` 及其 `-wal`、`-shm` 文件，再启动旧版本：它有升级前的一切，没有升级之后的。中断只是容器重建，几秒钟，开着的 SSH 会话会结束（到这里为止做过的事都有记录；见[通道保证什么，不保证什么](../guide/machines.md#what-the-channel-guarantees-and-what-it-does-not)）。网关停着的时间里，每个检测点把结果暂存在磁盘上——最多 64 MB——恢复了就补发，所以不丢检测结果。告警组在重启后仍在，触发中的告警之后还在 `list_alerts` 里。

## 选一个版本 {#pick-a-version}

- [更新日志](../changelog.md)写明每个版本改了什么；[发布页](https://github.com/openlinkos/beacon/releases)列出各个标签。预发布版带 `-rc.N`。
- 只有最新的小版本及其候选版本有修复（[安全政策](https://github.com/openlinkos/beacon/blob/main/SECURITY.md)这么定）；请挪到最新的标签，而不是更旧的小版本。

## 升级中心 {#upgrade-the-centre}

1. **先备份状态**：[备份与恢复](backup.md#back-up)里的三条命令，一分钟的活。升级之所以是个安全的时刻，正因为你退得回去。

2. **取该版本的部署文件**（推荐：钉住的镜像摘要、规则和操作手册都跟仓库一起走）。留一份 `/opt/beacon` 作退路，然后在当初安装用的克隆里：

   ```bash
   sudo cp -a /opt/beacon /opt/beacon.bak
   cd beacon
   git fetch --tags && git checkout vX.Y.Z        # 或者 git pull，跟着 main 走
   sudo cp -a install.sh compose.yaml Caddyfile .env.example /opt/beacon/
   sudo cp -a metrics rules runbooks probe /opt/beacon/
   ```

   `config.yaml`、`catalog/` 和 `targets/` 是你的，不会被覆盖。如果这个版本改了 `config.yaml` 本身，`git diff vOLD..vX.Y.Z -- deploy/config.yaml` 能看出改了什么；新的键请手工并进你的那份。新版本需要而你的配置里没有的键，安装脚本的校验会在重启任何东西之前停下来，指名道姓。

3. **在 `.env` 里抬高版本**：

   ```bash
   cd /opt/beacon
   sudo sed -i 's/^BEACON_VERSION=.*/BEACON_VERSION=vX.Y.Z/' .env
   ```

4. **运行安装脚本**，任何时候运行都安全：

   ```bash
   sudo ./install.sh
   ```

   带着新标签，它拉取新镜像，用新镜像校验 compose 文件、Caddyfile、规则和 Beacon 自己的配置——哪个文件校验不过，升级就在碰运行中的服务之前停下——然后 `docker compose up -d` 重建 Beacon 容器，并等全部四个容器健康、规则引擎的第一条告警到达网关。结尾应该和初次安装一样，看到 `Beacon is up (…s)`。

5. **检查。** 版本、容器和状态：

   ```bash
   docker exec beacon beacon version   # beacon vX.Y.Z (…)
   docker compose ps                   # 四个容器，healthy
   docker exec beacon beacon config history
   ```

   `config history` 列出你记得的那些版本，这正是数据库在说话。想完整走一遍链路，可以来一次演练（`docker exec beacon beacon drill fail 15m`，见[演练一次故障](../guide/drills.md)）。

## 先中心，后检测点 {#centre-first-then-the-probes}

检测协议在两个方向上都被各版本共享：旧的 `beacon-probe` 对新的中心照常工作，新的检测点对旧的中心也能少用点功能照常跑。所以先升级中心，检测点什么时候方便什么时候升：命令在[检测点位置](probes.md)（`sh probe/install.sh -gateway … -version vX.Y.Z`，可重复运行，只在二进制变了时重启服务）。

## 回退 {#roll-back}

1. 把上一个标签放回去，再跑一次安装脚本：

   ```bash
   sudo sed -i 's/^BEACON_VERSION=.*/BEACON_VERSION=vOLD/' .env
   sudo ./install.sh
   ```

2. 数据库不是障碍：Beacon 没有迁移，新版本写下的记录带着旧版本不读的字段，旧版本只是不读它们。回去的路上什么都不改写。
3. 如果第 2 步还换了部署文件，把当时留的那份拷回去：`sudo cp -a /opt/beacon.bak/. /opt/beacon/`。
4. 再往后，就[从备份恢复](backup.md#restore)。

## 接下来 {#what-next}

- [备份与恢复](backup.md)：拷什么、多久拷一次、怎么倒回去。
- 升级没有以 `Beacon is up` 收尾时，见[常见问题](troubleshooting.md)。
