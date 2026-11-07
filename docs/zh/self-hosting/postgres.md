---
sourceHash: sha256-8d8b4b6b
---

# 跑在 PostgreSQL 上

Beacon 默认把状态放在 SQLite 里：一个文件，不用另跑服务，对哪个团队都够用。本页把状态搬到 PostgreSQL，必要时再搬回来。

以下情况再搬到 PostgreSQL：

- 已经在跑 PostgreSQL，想把 Beacon 的状态和其他数据放在一起；
- 想用托管数据库，或按时间点恢复；
- 要[主备两台](#a-primary-and-a-standby)。

性能不是搬的理由。两边的状态一样，表也一样：账号、Agent 和令牌，目录，告警组，审计日志和它的哈希链。

示例以[装在服务器上](install.md)的参考部署为准，在 `/opt/beacon` 下以 root 执行。

## 准备数据库 {#prepare-the-database}

Beacon 要 PostgreSQL 14 或更新的版本，以及一个归它自己的数据库和角色。表由它第一次启动时自己建。

```sql
CREATE ROLE beacon LOGIN PASSWORD 'a long random password';
CREATE DATABASE beacon OWNER beacon;
```

**还没有 PostgreSQL 的话**，在同一个 compose 文件里和 Beacon 并排起一个就行。端口只在 compose 网络里，数据有自己的卷：

```yaml
services:
  postgres:
    container_name: beacon-postgres
    image: postgres:17-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: beacon
      POSTGRES_DB: beacon
      POSTGRES_PASSWORD_FILE: /run/secrets/postgres-password
    volumes:
      - postgres-data:/var/lib/postgresql/data
      - ./secrets/postgres-password:/run/secrets/postgres-password:ro
    healthcheck:
      test: [CMD, pg_isready, -U, beacon, -d, beacon]
      interval: 10s
    mem_limit: 512m

volumes:
  postgres-data:
```

**把连接串写进文件**。Beacon 从文件读连接串，不从 YAML 读，也从不把它写进日志或错误信息。把它放进密钥目录，和其他密钥一样只让 Beacon 的用户（uid 65532）能读：

```bash
cd /opt/beacon
printf 'postgres://beacon:%s@postgres:5432/beacon?sslmode=disable\n' "$PASSWORD" > secrets/postgres-dsn
chown 65532:65532 secrets/postgres-dsn && chmod 0400 secrets/postgres-dsn
```

`sslmode=disable` 适合同一个 compose 网络里的数据库，流量不出这台机器。在别处的数据库用 `sslmode=verify-full`。

## 搬状态 {#move-the-state}

搬迁在一个事务里，把 SQLite 数据库复制进空的 PostgreSQL 数据库。提交前它核对条数，并在审计链上加一条记录注明这次导入。SQLite 文件原样留着。

搬的时候要停掉 Beacon：复制之后服务写的东西只会留在 SQLite 里。停机期间各检测点把结果排在本地磁盘上，网关恢复后再补发。

1. **在 `config.yaml` 里写上数据库**，驱动暂时还是 `sqlite`：

   ```yaml
   storage:
     driver: sqlite
     dsn_file: /etc/beacon/secrets/postgres-dsn
   ```

2. **停掉 Beacon，开始搬：**

   ```bash
   docker compose stop beacon
   docker compose run --rm --no-deps beacon storage migrate -to postgres -config /etc/beacon/config.yaml
   ```

   它回答 `moved /var/lib/beacon/beacon.db into postgres postgres:5432/beacon: N entries across the buckets, the audit log newest …`。PostgreSQL 数据库里已经有东西时会拒绝，所以再跑一次什么也不会复制。

3. **把 `config.yaml` 里的驱动改成 `postgres`**，启动 Beacon：

   ```bash
   docker compose up -d beacon
   ```

## 检查 {#check-it}

```bash
docker exec beacon beacon storage check -config /etc/beacon/config.yaml
docker exec beacon beacon audit verify -config /etc/beacon/config.yaml
```

前一条命令读每张表，并校验审计链。后一条从头走一遍链。两条都应该报告完整，最新一条是导入的记录。

然后登录控制台。账号、告警和配置的历史都和原来一样。

## 回退 {#roll-back}

把 `storage.driver` 改回 `sqlite`，重启 Beacon。它用的是搬迁那一刻的 SQLite 文件。之后写进 PostgreSQL 的东西不在里面。

## 备份 {#back-up}

状态现在在 PostgreSQL 里，所以[备份与恢复](backup.md)里每晚的备份改成导出数据库，不再拷 SQLite 文件。`pg_dump` 在 Beacon 运行时读一个一致的快照，什么都不用停：

```bash
docker compose exec -T postgres pg_dump -U beacon -Fc beacon > /opt/beacon-backups/beacon-$(date +%F).dump
```

`beacon-data` 卷里还有会话录像，除非录像放在对象存储里（`ssh.recordings: s3`）。照旧备份它。

恢复时：

1. 起一个空的 PostgreSQL 给 Beacon 用。
2. 用 `pg_restore -U beacon -d beacon` 把导出倒进去。
3. 启动 Beacon。

## 主备两台 {#a-primary-and-a-standby}

用 PostgreSQL 时，两台机器上的两个 Beacon 实例可以共用状态。在数据库里持有租约的是主。主停止续约、租约过期后（约 15 秒），另一台接手。

两个实例都要有：

- 同样的配置；
- 同样的密钥；
- 放在同一个对象存储桶里的录像。

`ha` 和 `ssh.recordings` 的字段见[配置参考](../reference/config.md)。前面的负载均衡把流量发给 `/healthz` 返回 200 的那台。

## 接下来 {#what-next}

- [备份与恢复](backup.md)：备份里还要有什么。
- [升级](upgrade.md)：换新版本，两种数据库都一样。
