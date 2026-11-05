---
sourceHash: sha256-c6634a80
---

# 备份与恢复

一个 Beacon 中心的备份要装下什么、每样东西放在哪里、用什么命令拷出来再倒回去。示例以[装在服务器上](install.md)的参考部署为准，在中心的 `/opt/beacon` 下以 root 执行。在一台新机器上恢复就是：装好、解开部署目录、把卷里的数据倒回去。

配置了 `backup:` 目的地时，Beacon 会自己做加密备份（`beacon backup now | ls | restore | drill | keygen`；见[命令行](../reference/cli.md)、[config.yaml](../reference/config.md)）。这些档案用 age 加密，清单有签名；恢复到正在用的数据目录需要 `-confirm`。单次 S3 PUT 最大 5 GiB。参考部署里可选的 Litestream 服务会持续运送 WAL，但**那份副本是明文**——把它当作热副本、靠存储自己的访问控制，而不是当作必须保密的异地备份。Litestream 的数据卷以可写方式挂载，以便保存副本状态；它不加密，所以那份卷上的恢复材料对该容器可见。

## 每样东西在哪里 {#what-lives-where}

| 东西 | 位置 | 丢了会怎样 |
| --- | --- | --- |
| Beacon 的状态 | `beacon-data` 卷，`beacon` 容器内的 `/var/lib/beacon`：`status.db`（一个 bbolt 文件）和 `recordings/`（会话的录像 asciicast v3 与文字稿，按天一个目录） | 账号、Agent 和令牌；目录、检测目标和模块（在数据库里，不只是那几个文件）；告警组和备注；审计日志；会话索引；录像 |
| 指标 | `victoriametrics-data` 卷，`beacon-victoriametrics` 容器内的 `/storage`；保留 400 天 | 状态页 90 天历史条背后的数据，证据里的检测窗口 |
| 证书 | `caddy-data` 卷 | 不要紧：Caddy 会再向 Let's Encrypt 申请 |
| 部署 | `/opt/beacon`：`.env`、`config.yaml`、`secrets/`、`catalog/`、`targets/`、`rules/` | 你的配置，SSH 通道的密钥 |

关于密钥说两句：`install.sh` 在机器上生成它们，它们永不离开这台机器——备份既然装下了它们，备份自己就得保护好：只许 root 读，或者离开机器前加密。丢了 `ssh-ca-key`，机器就不再接受 Agent，直到每台机器换上新 CA 的公钥：重新运行 `beacon machine add NAME HOST`，把它打印的四行在机器上执行一遍（见[装在服务器上](install.md#3-the-first-machine)）。

## 备份 {#back-up}

Beacon 自己的镜像是 distroless——没有 shell，也没有 tar——所以用一个临时容器读卷，从持有卷的那个停掉的容器挂过去。Beacon 运行期间一直打开并锁着 `status.db`，在它底下拷出来的文件不保证完整，所以要停服务。停的代价是几秒钟：网关停着的时间里，每个检测点把结果暂存在磁盘上，恢复了就补发；开着的 SSH 会话会结束（做过的事已有记录）。

1. **Beacon 的状态和录像**——几秒钟：

   ```bash
   sudo install -d -m 0700 /opt/beacon-backups
   cd /opt/beacon
   docker compose stop beacon
   sudo docker run --rm --volumes-from beacon -v /opt/beacon-backups:/backup alpine \
     tar -C /var/lib/beacon -czf /backup/beacon-$(date +%F).tgz status.db recordings
   ```

2. **指标**，同样的做法，拷多久取决于数据有多大（这是大头）：

   ```bash
   docker compose stop victoriametrics
   sudo docker run --rm --volumes-from beacon-victoriametrics -v /opt/beacon-backups:/backup alpine \
     tar -C /storage -czf /backup/victoriametrics-$(date +%F).tgz .
   ```

3. **部署目录**，连同密钥：

   ```bash
   sudo tar -C /opt -czf /opt/beacon-backups/deployment-$(date +%F).tgz beacon
   ```

4. **恢复运行：**

   ```bash
   docker compose up -d
   docker compose ps        # beacon、beacon-victoriametrics、beacon-vmalert、beacon-caddy：healthy
   ```

多久备份一次：状态每天晚上一次——这四条命令就是 cron 里的一段脚本；部署目录改了就备；指标看你在乎多少历史。如果决定指标可以不要，单靠状态也能把中心救回来，只是状态页的历史从头算起，显示为灰色。

## 恢复 {#restore}

在卷被清空的同一台机器上，或在一台新机器上。

1. **装中心**，走到[装在服务器上](install.md)的第 1 步为止（克隆、拷到 `/opt/beacon`），然后把部署目录的备份解开盖上去，再运行安装脚本：

   ```bash
   sudo tar -C /opt -xzf /opt/beacon-backups/deployment-YYYY-MM-DD.tgz
   cd /opt/beacon && sudo ./install.sh
   ```

   `install.sh` 只生成缺失的密钥，所以恢复出来的 `ssh-host-key` 和 `ssh-ca-key` 会留下来，Agent 对机器的访问也就扛过了重建。它会在空卷上启动一切；下一步把状态倒回去。
2. **状态和录像**：

   ```bash
   docker compose stop beacon
   sudo docker run --rm --volumes-from beacon -v /opt/beacon-backups:/backup alpine \
     tar -C /var/lib/beacon -xzf /backup/beacon-YYYY-MM-DD.tgz
   docker compose up -d
   ```

   对着运行中的 Beacon 做这一步是不可能的：服务运行期间数据库是锁着的。同目录下的 `ctl.sock` 是服务的管理套接字，恢复时不必带上它。
3. **指标**，同样做法，用 `--volumes-from beacon-victoriametrics` 和 `tar -C /storage -xzf /backup/victoriametrics-YYYY-MM-DD.tgz`。
4. **检查。**

   - 用原来的账号登录控制台；告警、静默、钩子都还是原样。
   - `docker exec beacon beacon config history` 列出你记得的那些版本。
   - 之前的一次会话，在控制台的**会话**页里还能打开回放。
   - 检测点什么都不用做：它们的令牌没变，中心停着的这段时间它们把结果暂存在磁盘上；一分钟内总览里就能看到它们重新上报。

## 目录，以文件形式 {#the-catalogue-as-files}

目录在 `status.db` 里，所以每次状态的备份都装着它。要把生效的配置也留成文件——放进 git，或者在两个中心之间搬——用：

```bash
docker exec beacon beacon config export /var/lib/beacon/export
sudo docker cp beacon:/var/lib/beacon/export /opt/beacon/export-$(date +%F)
```

容器里的 `/etc/beacon` 是只读挂载，所以导出到数据目录再拷出来。导入用 `beacon config import DIR`，先 `-dry-run`；整套说明见[配置](configuration.md#the-catalogue-files-at-start-and-on-sighup)。

## 接下来 {#what-next}

- [升级](upgrade.md)，它从一次备份开始。
- [安全模型](security.md)：备份装下的不只是数据，还有通向机器的钥匙。
