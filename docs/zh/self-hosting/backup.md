---
sourceHash: sha256-9bb17de3
---

# 备份与恢复

本页说明一个 Beacon 中心的备份要装下什么、每样东西放在哪里。还给出备份的命令，以及把备份倒回去的命令。

在一台新机器上恢复就是：装好、解开部署目录、把卷里的数据倒回去。

示例以[装在服务器上](install.md)的参考部署为准，在中心的 `/opt/beacon` 下以 root 执行。

## 每样东西在哪里 {#what-lives-where}

| 东西 | 位置 | 丢了会怎样 |
| --- | --- | --- |
| Beacon 的状态 | `beacon-data` 卷，`beacon` 容器内的 `/var/lib/beacon`：`beacon.db`（一个 SQLite 数据库，运行时旁边有 `-wal` 和 `-shm` 文件）和 `recordings/`（会话的录像 asciicast v3 与文字稿，按天一个目录） | 账号、Agent 和令牌；目录、检测目标和模块（在数据库里，不只是那几个文件）；告警组和备注；审计日志；会话索引；录像 |
| 指标 | `victoriametrics-data` 卷，`beacon-victoriametrics` 容器内的 `/storage`；保留 400 天 | 状态页 90 天历史条背后的数据，证据里的检测窗口 |
| 证书 | `caddy-data` 卷 | 不要紧：Caddy 会再向 Let's Encrypt 申请 |
| 部署 | `/opt/beacon`：`.env`、`config.yaml`、`secrets/`、`catalog/`、`targets/`、`rules/` | 你的配置，SSH 通道的密钥 |
| 团队凭据策略 | 状态数据库中的加密记录，以及 Beacon 数据目录的 `team-credentials/epochs` | 验证当前策略所需的防回滚记录。将最新版本另存一份受保护的独立副本 |

当 `ssh.recordings` 设为 `s3` 时，备份共享存储桶和每个中心的本地状态。等待上传的已封存证据位于原中心的 `recordings/` 目录。重启后，Beacon 会继续上传已封存的文件。旧版遗留且没有封存标记的文件，在会话保留期内仍可在原中心读取。

关于密钥说两句。`install.sh` 在机器上生成它们，它们永不离开这台机器：

- **备份既然装下了密钥，备份自己就得保护好**：只许 root 读，或者离开机器前加密。
- **丢了 `ssh-ca-key`，机器就不再接受 Agent**，直到每台机器换上新 CA 的公钥。重新运行 `beacon machine add NAME HOST -replace`，把它打印的四行在机器上执行一遍（见[装在服务器上](install.md#3-the-first-machine)）。

## 备份 {#back-up}

Beacon 自己的镜像是 distroless，没有 shell，也没有 tar。所以用一个临时容器读卷，从持有卷的那个停掉的容器挂过去。

Beacon 运行期间一直打开并锁着 `beacon.db`。在它底下拷出来的文件不保证完整，所以要停服务。正常停下以后，所有内容都在 `beacon.db` 本身里。

停的代价是几秒钟。网关停着的时间里，每个检测点把结果暂存在磁盘上，恢复了就补发。开着的 SSH 会话会结束，做过的事已有记录。

1. **备份 Beacon 的状态和录像**。几秒钟：

   ```bash
   sudo install -d -m 0700 /opt/beacon-backups
   cd /opt/beacon
   docker compose stop beacon &&
   sudo docker run --rm --volumes-from beacon -v /opt/beacon-backups:/backup alpine \
     sh -c 'cd /var/lib/beacon || exit 1
       set -- beacon.db*
       if [ -d recordings ]; then set -- "$@" recordings; fi
       if [ -f team-credentials/epochs ]; then set -- "$@" team-credentials/epochs; fi
       tar -czf /backup/beacon-$(date +%F).tgz "$@"'
   ```

   启用[团队凭据库](../guide/team-credentials.md)后，在受保护的独立存储中保留最新且已验证的 `team-credentials/epochs`。每次凭据策略变更经确认后都更新这份独立副本，撤销授权也一样，无需等到下次数据库备份。另行备份 `credentials.key_file` 所指的密钥文件。

2. **备份指标**，同样的做法。拷多久取决于数据有多大：这是大头。

   ```bash
   docker compose stop victoriametrics
   sudo docker run --rm --volumes-from beacon-victoriametrics -v /opt/beacon-backups:/backup alpine \
     tar -C /storage -czf /backup/victoriametrics-$(date +%F).tgz .
   ```

3. **备份部署目录**，连同密钥：

   ```bash
   sudo tar -C /opt -czf /opt/beacon-backups/deployment-$(date +%F).tgz beacon
   ```

4. **恢复运行：**

   ```bash
   docker compose up -d
   docker compose ps        # beacon、beacon-victoriametrics、beacon-vmalert、beacon-caddy：healthy
   ```

多久备份一次：

- **状态**：每天晚上一次。这四条命令就是 cron 里的一段脚本。
- **部署目录**：改了就备。
- **指标**：看你在乎多少历史。

如果决定指标可以不要，单靠状态也能把中心救回来。只是状态页的历史从头算起，显示为灰色。

## 恢复 {#restore}

在卷被清空的同一台机器上，或在一台新机器上。

1. **装中心**，走到[装在服务器上](install.md)的第 1 步为止：克隆、拷到 `/opt/beacon`。然后把部署目录的备份解开盖上去，再运行安装脚本：

   ```bash
   sudo tar -C /opt -xzf /opt/beacon-backups/deployment-YYYY-MM-DD.tgz
   cd /opt/beacon && sudo ./install.sh
   ```

   `install.sh` 只生成缺失的密钥。所以恢复出来的 `ssh-host-key` 和 `ssh-ca-key` 会留下来，Agent 对机器的访问也就扛过了重建。它会在空卷上启动一切；下一步把状态倒回去。

2. **恢复状态和录像。** 如果原来启用了团队凭据库，从独立存储中取出最新且可靠的防回滚记录，选用与该记录匹配或更新的数据库备份。存档可能早于后续的授权撤销。解包时跳过存档附带的防回滚记录。

   ```bash
   docker compose stop beacon &&
   sudo docker run --rm --volumes-from beacon -v /opt/beacon-backups:/backup:ro alpine \
     tar -C /var/lib/beacon --exclude=team-credentials/epochs -xzf /backup/beacon-YYYY-MM-DD.tgz
   ```

   让 Beacon 保持停止。服务运行期间数据库会被锁住。恢复时无需带上服务的管理套接字 `ctl.sock`。

   **如果原来启用了团队凭据库**，将独立保存的防回滚记录放到 `/opt/beacon-backups/team-credential-epochs`，只允许 root 访问。启动 Beacon 之前，将这份记录恢复到数据卷，确保容器内的服务账户可以读取和更新它。

   ```bash
   sudo docker run --rm --volumes-from beacon -v /opt/beacon-backups:/backup:ro alpine \
     sh -ec 'test -s /backup/team-credential-epochs
       install -d -m 0700 -o 65532 -g 65532 /var/lib/beacon/team-credentials
       install -m 0600 -o 65532 -g 65532 /backup/team-credential-epochs /var/lib/beacon/team-credentials/epochs
       sync'
   ```

   如果找不到最新且可靠的防回滚记录，或没有匹配的数据库，在启动 Beacon 前先在 `config.yaml` 的 `credentials` 下设置 `enabled: false`。监控和 SSH 可以继续运行。恢复上游服务的访问令牌后再重新授权。使用 PostgreSQL 的单中心部署除了数据库备份，也需要独立留存的防回滚记录和外部密钥。

   **启动 Beacon。** 先恢复独立的防回滚记录及其匹配的状态，或确保团队凭据库已停用。

   ```bash
   docker compose up -d
   ```

3. **倒回指标**，同样做法，用 `--volumes-from beacon-victoriametrics` 和 `tar -C /storage -xzf /backup/victoriametrics-YYYY-MM-DD.tgz`。

4. **检查。**

   - 用原来的账号登录控制台。告警、静默、钩子都还是原样。
   - `docker exec beacon beacon config history` 列出你记得的那些版本。
   - 之前的一次会话，在控制台的**会话**页里还能打开回放。
   - 如果恢复了团队凭据库，以所有者身份打开**团队凭据**，向已批准的服务发出测试请求。核对授权列表，并确认被撤销授权的 Agent 仍无法使用该凭据。
   - 检测点什么都不用做。它们的令牌没变，中心停着的这段时间它们把结果暂存在磁盘上。一分钟内总览里就能看到它们重新上报。

Linux 参考部署的烟测会将已停止的 SQLite 服务恢复到新卷。测试覆盖所有者重新登录、告警备注和 SSH 回放，以及恢复独立防回滚记录后，被撤销授权的 Agent 仍无法使用凭据。

## 目录，以文件形式 {#the-catalogue-as-files}

目录在 `beacon.db` 里，所以每次状态的备份都装着它。要把生效的配置也留成文件，放进 git 或者在两个中心之间搬，用：

```bash
docker exec beacon beacon config export /var/lib/beacon/export
sudo docker cp beacon:/var/lib/beacon/export /opt/beacon/export-$(date +%F)
```

容器里的 `/etc/beacon` 是只读挂载。所以导出到数据目录，再从那里拷出来。

导入用 `beacon config import DIR`，先 `-dry-run`。整套说明见[配置](configuration.md#the-catalogue-files-at-start-and-on-sighup)。

## 接下来 {#what-next}

- [升级](upgrade.md)，它从一次备份开始。
- [安全模型](security.md)：备份装下的不只是数据，还有通向机器的钥匙。
