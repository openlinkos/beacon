---
sourceHash: sha256-82e536a8
---

# 命令行

Beacon 有三个二进制。`beacon` 是服务本身，以及运行和治理它的命令；`beacon-probe` 是跑在每个检测点上的探针 Agent；`beacon-oncall` 是 Beacon 唤醒 Agent 时所用参考执行器。三个都认 `version`（版本、提交和构建）和 `help`（完整用法）。

## beacon

`serve` 运行服务。其余命令都通过管理套接字与运行中的服务通信，套接字在数据目录里，默认 `/var/lib/beacon`，也可用 `BEACON_DATA_DIR` 指定，因此要在容器里执行：

```bash
docker exec beacon beacon token list
```

| 命令 | 作用 |
| --- | --- |
| `serve` | 运行服务：控制台、API 与 MCP、路由器与桥、网关和 SSH 通道。取 `-config`（默认 `/etc/beacon/config.yaml`），并在 SIGHUP 时重新导入它指名的目录、目标和模块文件；见 [config.yaml](config.md) |
| `check-config` | 校验配置文件、它指名的文件和数据库里的配置，不启动。取 `-config` |
| `config` | 数据库里的配置：[import、export、history、revert](#beacon-config) |
| `setup` | 打印建立第一个成员（所有者）的一次性链接，30 分钟内有效 |
| `user` | 成员：[list、invite、disable、enable、role、reset](#beacon-setup-user-agent) |
| `agent` | 某个成员名下的 Agent：[add、list、remove](#beacon-setup-user-agent) |
| `token` | 凭据：[create、list、revoke、ssh-key、machines、claim](#beacon-token) |
| `machine` | 机器：[add、connector、list、remove、ca](#beacon-machine-ssh) |
| `ssh` | SSH 通道：[pause、resume、status](#beacon-machine-ssh) |
| `enroll` | 机器的网关令牌：[probe、host 或 heartbeat](#beacon-enroll-silence-drill) |
| `silence` | 静默：[create、list、expire](#beacon-enroll-silence-drill) |
| `drill` | 演练一次故障：[fail、off、status](#beacon-enroll-silence-drill) |
| `audit` | 审计日志的链：[verify](#beacon-audit-storage) |
| `storage` | 数据库的健康检查：[check](#beacon-audit-storage)；搬去 PostgreSQL：[migrate](#beacon-audit-storage) |
| `healthcheck` | 服务应答则退出码为 0；镜像的健康检查。取 `-url`（默认 `http://127.0.0.1:17001/livez`，健康的备机也是 200；`/healthz` 是负载均衡用的，备机上是 503） |
| `version` | 版本、提交和构建 |
| `help` | 完整用法，含角色、预设和 scope |

### beacon config

目录、检测目标和模块放在数据库里（[检测一个服务](../guide/monitoring.md)）；这些命令把它们作为一个带版本的变更修改。`import` 读 `DIR/catalog`、`DIR/targets` 和 `DIR/targets/modules.yml`，也就是 `export` 写出的布局。

| 子命令 | 参数 | 作用 |
| --- | --- | --- |
| `import` DIR | `-dry-run`、`-note` | 把 DIR 下的文件作为一次变更设为目录、目标和模块。`-dry-run` 只说会改什么，不改动；`-note` 写明原因，随变更保存并显示在告警群 |
| `export` DIR | | 把配置按同样布局写到该目录，并删除配置里已不存在的文件 |
| `history` | `-limit` | 变更列表：版本、时间、谁、途径、动作、备注。`-limit`（默认 20）定条数 |
| `revert` VERSION | `-note` | 回到 `history` 里的某个版本 |

### beacon setup, user, agent

成员自己的账号和他们的 Agent（[邀请你的团队](../guide/people.md)）。一次性链接和令牌明文只在标准输出打印一次；其余内容都走标准错误输出。`setup` 打印建立第一个成员（所有者）的一次性链接，只可用一次，30 分钟内有效。

**`beacon user`** —— 成员：

| 子命令 | 参数 | 作用 |
| --- | --- | --- |
| `list` | | 成员、角色和 Agent，以及尚未使用的邀请 |
| `invite` | `-role`、`-note` | 一个加入某个角色的一次性链接。`-role` 取 `viewer`、`oncall`、`operator`、`owner` 之一；`-note` 写明给谁 |
| `disable` NAME、`enable` NAME | | 停用或恢复某人的登录 |
| `role` NAME ROLE | | 改角色；其会话和 Agent 随即跟上 |
| `reset` NAME | | 选择新密码的一次性链接；其登录随即全部结束 |

**`beacon agent`** —— 成员名下的 Agent：

| 子命令 | 参数 | 作用 |
| --- | --- | --- |
| `add` NAME | `-owner`、`-preset`、`-scopes`、`-machines`、`-expires`、`-ssh-key` | 建一个成员的 Agent 和它的第一枚令牌，并打印使用它的命令片段。`-owner` 指名所有者；`-preset` 取 `readonly`、`oncall` 或 `operator`，即所有者角色所允许的最强预设；`-scopes` 用自定义 scope 代替预设；`-machines` 限定它能到达的机器；`-expires`（默认 `365d`）；`-ssh-key` 加一把通道用的公钥，可直接给钥匙或 `@FILE` |
| `list` | | 每个 Agent：所有者、预设、可做什么、机器、令牌、OAuth 登录、密钥 |
| `remove` NAME | | 删除一个 Agent，连同它的令牌、OAuth 登录和密钥 |

### beacon token

按名字给 scope 的凭据，给机器和专家用；Agent 的令牌来自 [beacon agent](#beacon-setup-user-agent) 或控制台。新令牌的明文只在标准输出打印一次。

| 子命令 | 参数 | 作用 |
| --- | --- | --- |
| `create` NAME | `-scopes`、`-expires`、`-ssh-key`、`-machines` | 建一枚令牌。`-scopes`（默认 `status:read`），逗号分隔；`-expires`（默认 `365d`）；`-ssh-key` 一把通道用的公钥，可直接给钥匙或 `@FILE`；`-machines` 它能到达的机器，缺省为全部 |
| `list` | | 每枚令牌：scope、到期、寿命、续期时间、谁建的 |
| `revoke` NAME | | 立即作废一枚令牌 |
| `ssh-key` NAME `add` KEY | | 给令牌加一把公钥（`@FILE` 从文件读） |
| `ssh-key` NAME `remove` FINGERPRINT | | 删除令牌的一把密钥 |
| `machines` NAME a,b | | 令牌经通道能到达的机器；`all` 为全部 |
| `claim` NAME | `-owner`、`-agent`、`-preset` | 把一枚旧式令牌归为某个成员的 Agent。`-owner` 指名所有者；`-agent` 指名 Agent（默认用令牌自己的名字）；`-preset` 取 `readonly`、`oncall`、`operator` 或 `custom`（保留原 scope） |

### beacon machine, ssh

机器和 SSH 通道（[给 Agent 一台机器](../guide/machines.md)）。`add` 会打印机器一侧需要做的配置。

**`beacon machine`**：

| 子命令 | 参数 | 作用 |
| --- | --- | --- |
| `add` NAME HOST[:PORT] | `-user`、`-critical`、`-note`、`-replace`、`-key`、`-host-key` | 固定机器的 host key 并按地址到达它。`-user` 是机器上的账号（默认 `beacon`）；`-critical` 标记为关键机器；`-note` 写明它是什么；`-replace` 重新固定已添加机器的 host key |
| `add` NAME connector | 同上 | NAT 后的机器，用 `ssh -R` 向外连到 Beacon；`-key` 是它外连用的公钥，`-host-key` 它的 host key（缺省在首次连接时学习） |
| `connector` NAME | `-key` | 设置或更换连接器机器外连用的密钥 |
| `list` | | 每台机器：地址、账号、host key、连接器是否在线 |
| `remove` NAME | | 删除一台机器 |
| `ca` | | 机器信任的 CA 公钥（`TrustedUserCAKeys`） |

**`beacon ssh`** —— 掣动机器访问的刹车：

| 子命令 | 作用 |
| --- | --- |
| `pause` | 立即停止 Agent 对所有机器的访问；除每台机器自己的访问控制外唯一的刹车 |
| `resume` | 恢复访问 |
| `status` | 通道是否开启、自何时暂停、它的 host key |

### beacon enroll, silence, drill

**`beacon enroll`** —— 机器的网关令牌，单独且只打印一次到标准输出，部署脚本可以直接传给机器：

| 子命令 | 参数 | 作用 |
| --- | --- | --- |
| `probe` NAME | `-rotate` | 检测点的令牌，带推送结果和拉取检测任务的 scope |
| `host` NAME | `-rotate` | 主机的令牌，推送它自己的指标 |
| `heartbeat` NAME | `-rotate` | 任务的令牌，推送心跳 |

`-rotate` 替换已注册机器的令牌。

**`beacon silence`**：

| 子命令 | 参数 | 作用 |
| --- | --- | --- |
| `create` | `-matchers`、`-alert`、`-duration`、`-comment` | 不再通知带指定标签的告警：`-matchers` 写成 `alertname=X,service=Y`，或 `-alert` 给告警组的 id。`-duration`（至多 24h）和 `-comment` 必填 |
| `list` | | 生效和待生效的静默，先结束的在前 |
| `expire` ID | | 立即结束一条静默，它盖住的告警恢复通知 |

### beacon audit、storage {#beacon-audit-storage}

**`beacon audit`** 与 **`beacon storage`** 直接读数据库文件，不需要服务，也不加锁，服务运行时也能用。都接受 `-config`：

| 子命令 | 参数 | 作用 |
| --- | --- | --- |
| `audit verify` | `-from-seq` | 校验审计日志的哈希链——从覆盖 `-from-seq` 的检查点起，或校验现存的全部——链在哪里断就报哪里（退出码 1），否则报告校验了多少条 |
| `storage check` | | 数据库的健康：所用后端自己的检查（SQLite 的一致性检查，或 PostgreSQL 上读全部表）、每个桶各存了多少、审计链的校验 |
| `storage migrate` | `-to` | 把 SQLite 数据库搬进 `storage.dsn_file` 指定的 PostgreSQL，一个事务搬完、提交前核对数量，一条创世记录记下导入。SQLite 文件保留并继续服务，直到配置把驱动改成 postgres；回滚就是把驱动留在 sqlite（搬完后写进 PostgreSQL 的数据不在 SQLite 文件里）。目前 `-to postgres` 是唯一目的地 |

**`beacon drill`** —— 演练一次故障（[演练一次故障](../guide/drills.md)）：

| 子命令 | 作用 |
| --- | --- |
| `fail` 15m | 让 `/drill/healthz` 在这段时间内应答 503，看告警是否如实触发 |
| `off` | 结束演练 |
| `status` | 是否在演练、到何时 |

## beacon-probe

探针 Agent（[检测点位置](../self-hosting/probes.md)）。模块在 `password_file` 里指名的文件只从 `-secrets-dir`（默认 `/etc/beacon-probe/secrets`）按文件名读取；模块写了路径会被拒绝。

| 子命令 | 参数 | 作用 |
| --- | --- | --- |
| `agent` | `-gateway`、`-token-file`、`-state`、`-interval`、`-textfile`、`-proc`、`-listen`、`-secrets-dir` | 推送模式：从网关（`-gateway`）拉取目标和模块，每 `-interval`（默认 `1m`）检测一轮并推送结果，`-state`（默认 `/var/lib/beacon-probe`）里的磁盘队列在网关够不着时排队。`-token-file`（默认 `/etc/beacon-probe/token`）存 `beacon enroll probe` 发的令牌。`-textfile`（默认 `/var/lib/beacon-probe/textfile`）读 `*.prom` 文件即心跳；`-proc`（默认 `/proc`）采集主机自身指标；`-listen`（默认 `127.0.0.1:17009`，留空关闭）上提供 `/healthz` 和 `/metrics` 供手工查看 |
| `serve` | `-listen`、`-modules`、`-textfile`、`-proc`、`-secrets-dir` | 拉取模式，像 Blackbox Exporter：给抓取方应答 `/probe` 和 `/metrics`。`-modules`（默认 `/etc/beacon-probe/modules.yml`）在 SIGHUP 时重载 |
| `check` | `-modules` | 校验模块文件，按中心导入时的同样规则 |
| `run` MODULE TARGET | `-modules`、`-secrets-dir` | 检测一次并打印结果 |
| `version` | | 版本、提交和构建 |

## beacon-oncall

跑在 Agent 机器上的参考执行器（[被 webhook 唤醒的 Agent](../integrations/webhooks.md)）：接下 Beacon 签名的 webhook，立刻应答，然后通过 `/bin/sh -c` 启动 `-run` 指名的程序，把负载放在标准输入、告警放进 `BEACON_*` 环境变量。执行器不做任何决定；程序自己决定。

| 参数 | 默认值 | 作用 |
| --- | --- | --- |
| `-listen` | `:17007` | 接收 Beacon webhook 的地址 |
| `-secret-file` | 必填 | 存 hook 密钥的文件，与 Beacon 一侧的 `secret_file` 相同 |
| `-run` | 必填 | 处理 firing 事件的程序 |
| `-run-resolved` | 无 | 处理 resolved 事件的程序；不设则只确认、不运行 |
| `-concurrency` | `2` | 同时运行的程序数；其余排队。至多 32 |
| `-timeout` | `30m` | 超时后杀死程序的整个进程组 |
| `-log-dir` | `/var/lib/beacon-oncall/logs` | 每次运行的输出，文件名 `<time>-<delivery>.log` |
| `-keep` | `720h` | 超过此时长的日志被清除；`0` 全部保留 |

`beacon-oncall version` 打印版本、提交和构建。Claude Code 与 Codex 的包装脚本和运行它的 unit 在仓库的 `contrib/oncall` 里。
