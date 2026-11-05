---
sourceHash: sha256-aceee746
---

# PostgreSQL 与高可用

Beacon 的状态默认放在 SQLite，对多数部署这就是对的答案：一个文件、不用多运维一个服务、`VACUUM INTO` 在线备份，上面还有 Beacon 自己的[备份](backup.md)。本页讲另一种情形——什么时候 PostgreSQL 更合适，以及它换来什么：一个能接管的备机、按时间点恢复和托管数据库。

性能不是换的理由。Beacon 的写入每秒最多几十次，远低于 SQLite 的能力；在我们这个规模上，PostgreSQL 带来的是运维手段，不是速度。

## 什么时候用 PostgreSQL {#when-postgresql}

| 你的情况 | 选择 |
| --- | --- |
| 一台机器、一个小团队 | SQLite，加上 Beacon 内置备份；想要秒级恢复点再加 Litestream（见下） |
| 已经在运维 PostgreSQL，或想用托管数据库、按时间点恢复、加一台备机 | PostgreSQL |
| 指望写入更快 | 都不选：时间不花在这里 |

## 迁移状态 {#moving-the-state}

驱动和数据源名写进 `config.yaml`——`dsn_file` 指到 `secrets/` 下的文件，DSN 本身不写进配置（见 [config.yaml](../reference/config.md)）：

```yaml
storage:
  driver: postgres
  dsn_file: secrets/postgres-dsn
```

PostgreSQL 14 及以上；连接池和语句超时是同一配置块里的键。启动时如果 SQLite 数据库还在、驱动却选了 PostgreSQL，一次迁移完成：`beacon storage migrate -to postgres` 把每张表和事件日志在一个事务里拷过去，核对数量，SQLite 文件留在原地。反方向同理。`beacon storage check` 报告在用数据库的健康——一致性检查、每个桶各存多少、审计链的校验。

Beacon 存的一切都在两种数据库上测过；同一套键值和事件接口承载状态，哪种都一样（见[架构](../about/architecture.md#where-state-lives)）。

## 主机旁边的备机 {#a-standby-beside-the-primary}

适用时的高可用是一主一备——不是两个同时活跃的中心。SSH 监听、聊天的长连接、告警路由都只能有一个头，所以同一时刻只有一个实例活跃，由数据库决定是哪个：

- 数据库里的一张**租约**表。真正跑的是 [config.yaml](../reference/config.md) 里的 `ha.renew`（5 秒）和 `ha.expire`（15 秒）。设置里也登记了 `storage.lease.renew` 和 `storage.lease.expires`，数字相同；集成时只会留一套。
- 备机拿不到租约，`/healthz` 返回 `503`，于是前面的负载均衡或 VIP 把所有请求都转给主机。健康的备机 `/livez` 仍是 `200`，Docker 的健康检查就不会把它重启；断了网的实例 `/livez` 失败。
- **录像**放在两个实例都能读写的 S3 兼容存储上（见[配置](configuration.md)）；告警、告警组和路由状态本来就在数据库里。
- 切换耗时约等于租约过期时间：主机一停，备机在这 15 秒内开始应答。

## 持续复制 {#continuous-replication}

SQLite 上想要秒级恢复点，用 [Litestream](https://litestream.io/) 把预写日志持续流到 S3 兼容存储；参考部署的 compose 文件把它列为可选服务。恢复就是把数据库从流里回到备机的磁盘，再在上面启动 Beacon；v0.5 起按时间点恢复走同一条流。PostgreSQL 下这件事归数据库：pgBackRest 或 WAL-G 做物理备份和 PITR，Beacon 自己的备份就只装录像、配置和 secrets 目录（见[备份](backup.md)）。

指标历史——VictoriaMetrics——不在两种数据库的备份里；它的工具是 `vmbackup`，参考部署的说明里有。

## 接下来 {#what-next}

- [备份与恢复](backup.md)：两种模式下各备份什么、恢复怎么演练。
- [配置](configuration.md)：`storage:` 配置块和录像的 S3 设置。
- [升级](upgrade.md)：新版本服务之前先把数据库迁过去。
