---
sourceHash: sha256-3a0a8f6e
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
| `storage` | 数据库的健康检查：[check、migrate](#beacon-audit-storage) |
| `settings` | 保管库的配置：[get、set、reset、history、revert](#beacon-settings) |
| `secret` | 密钥保管库：[ls、collection、show、set、reveal、tag、level、archive、rm、restore、rollback、generate、import、perms、tags](#beacon-secret) |
| `run` | 把环境变量和 `.env` 里的 `beacon://` 引用注入子进程（`-agent`、`-reason`、`-env-file`、`-wait`）；值从不打印。见 [beacon run](#beacon-run) |
| `key` | 保管库的密钥：[status、init、unseal、seal、rotate](#beacon-key) |
| `backup` | Beacon 自己的备份：[now、ls、restore、drill、keygen](#beacon-audit-storage) |
| `healthcheck` | 服务应答则退出码为 0；镜像的健康检查。取 `-url`（默认 `http://127.0.0.1:17001/livez`，健康的备机也是 200；`/healthz` 是负载均衡的检查，备机上是 503） |
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

### beacon settings {#beacon-settings}

`settings` 命令读写密钥工作所依据的系统配置，每项都有登记过的默认值，每次改动都是一个版本，记着谁改的、为什么，可以回滚（[设置](settings.md)）。值按存的样子写 JSON：`50`、`"30d"`、`["rules","agents"]`、`{"keep":"365d","min_versions":10}`。

| 子命令 | 标志 | 做什么 |
| --- | --- | --- |
| `get` [KEY] | | 每一项的值、默认值、在哪一级被覆盖、谁改的，以及其它范围的覆盖；给 KEY 时只看那一项 |
| `set` KEY VALUE | `-scope`、`-target`、`-reason` | 改一项。`-reason` 说明为什么，随改动保存并回显到告警群；`-scope`（默认 `system`）和 `-target` 把值放在某个库、标签、资格或申请上 |
| `reset` KEY | `-scope`、`-target`、`-reason` | 去掉覆盖，让上一级（或默认值）重新生效 |
| `history` | `-limit` | 改动：版本、时间、谁、途径、动作、配置项、备注 |
| `revert` VERSION | `-note` | 回到 `history` 里的某个版本 |

### beacon secret {#beacon-secret}

保管库，给人在命令行上手：和控制台的密钥页、[REST 路由](api.md)是同一套权限和审计。每次改动都记在某个账号上（`-user`，默认是你自己），看值需要那个账号的密码，值和审计、日志里从不出现。条目用 `beacon://` 引用或 `COLLECTION/TITLE` 指名。

这些命令走数据目录里的管理 socket，所以要在容器里（或持有 socket 的那台主机上）跑。导入也在那里解析：导出文件的明文不走 REST 或 MCP，那两面也没有导入入口。

| 子命令 | 标志 | 做什么 |
| --- | --- | --- |
| `ls` [COLLECTION] | `-state` | 库的级别和条目数；给库名则列条目（`-state`：`normal`、`archived` 或 `trash`）：标题、版本、状态、级别、标签 |
| `collection` NAME | `-level`、`-description` | 建一个库；`-level`（默认 `review`）是条目继承的审批 |
| `show` REF | | 一条：版本、级别、状态、标签、`beacon://` 引用、字段（名、类型、用途）和有多少版本——从不带值 |
| `set` COLLECTION/TITLE [FIELD=VALUE …] | `-tags`、`-level`、`-category`、`-url`、`-set-note`、`-reason`、`-value-file`、`-value-from-stdin` | 添加或写下一条的下一版本。字段是 `name=value` 或 `section/name=value`；`username` 是明文，其余都是隐藏字段。`FIELD=-` 配 `-value-file` 或 `-value-from-stdin` 从文件或 stdin 读值，不必出现在命令行。`-level` `-` 清掉条目自己的级别；`-reason` 说明为什么，随版本保存 |
| `reveal` REF [FIELD …] | `-user`、`-password-file`、`-password-from-stdin` | 看值：账号密码来自文件（`-password-file`）或 stdin（`-password-from-stdin`；输入时会显示）。`otp` 字段给当前验证码和剩余秒数。打印前先审计 |
| `tag` REF TAGS | `-user`、`-reason` | 改标签（逗号分隔）——这是策略改动，需要该库的 `manage` |
| `level` REF LEVEL | `-user`、`-reason` | 设条目自己的审批级别（`auto`、`review`、`person`、`never`，或 `-` 继承库的）——同样要 `manage` |
| `archive` REF | `-user` | 归档：还在，不挡路 |
| `rm` REF | `-user` | 进回收站，`retention.trash`（30 天）还管着 |
| `restore` REF | `-user` | 从归档或回收站拿回来 |
| `rollback` REF VERSION | `-note` | 把旧版本再做成当前；`-note` 说明为什么，随新版本保存 |
| `generate` REF FIELD | `-length`、`-charset`、`-user`、`-reason` | 让 Beacon 生成新值（`-length` 默认 24，`-charset` 为 `letters`、`alnum` 或 `all`），存成新版本。值不显示；要看就 reveal |
| `import` SOURCE | `-file`、`-collection`、`-on-conflict`、`-level`、`-map`、`-vault`、`-dry-run`、`-user`、`-reason` | 导入导出：`1pux`（1Password 未加密导出，推荐）、`1password-csv`、`op`（已登录的 `op` 命令行；大约 300 条以上用 1PUX）、`chrome`（一个库；同一凭据跨站点合成一条带多个 URL）、`bitwarden`（未加密 JSON）或 `csv`（任意 CSV，列名用 `-map title=HEADER,…`）。命令走数据目录里的管理 socket，文件在跑 Beacon 的那台主机上解析。没有 REST 或 MCP 的导入。`-dry-run` 只给报告、不写入。`-on-conflict` 默认 `skip`（`new_version`、`skip`、`keep_both`——没有 `ask`）。输出从不带值；做完删掉导出文件 |
| `perms` COLLECTION [user:NAME=PERM] | `-user`、`-reason` | 谁在这个库能做什么：没有第二段就列出来；`user:alice=read` 给人 `describe`、`read`、`edit` 或 `manage`（角色用 `role:oncall=read`），`user:alice=-` 收回 |
| `tags` [NAME [LEVEL]] | `-description`、`-user` | 标签，或设一个：标签的级别是策略（戴它的每条至少那么严），只有 owner 的账号能改 |

### beacon run {#beacon-run}

`beacon run` 填上进程环境和 `.env` 里的 `beacon://` 引用，再带着值启动子命令。值从不打印：只活在那个进程里，和 `op run` 一样。引用走点名的 Agent 的 use 授权；`-wait` 在没有覆盖的授权时申请并等待那么久。

```bash
beacon run -agent claude-code -reason "fix the dns outage" --env-file .env -- printenv TOKEN
```

| 标志 | 做什么 |
| --- | --- |
| `-agent` NAME | 引用所走的 Agent |
| `-reason` TEXT | 为什么；每次取用都记 |
| `-env-file` FILE | 一份 `.env`，里面的 `beacon://` 会被填上 |
| `-wait` DURATION | 没有覆盖的授权时等多久（Go 时长，例如 `30s`） |

`--` 后面是命令。`-env-file` 的值盖过进程环境；解析后的 `beacon://` 再盖过两者。

REST `POST /api/v1/secrets/use` 是给 CLI 的同一条路：它返回值，好让 `beacon run` 注入。不能看见值的 Agent 用 `run_command` 的 `secrets` 或 `http_request`；没有会返回值的 MCP 工具。

### beacon key {#beacon-key}

保管库的密钥：根密钥从哪来见 [config.yaml](config.md) 的 `vault` 段。`file`、`aws-kms`、`gcp-kms`、`transit` 模式启动时密钥就在；`passphrase` 模式重启后封存，直到 owner 第一次 `key init`，再 `unseal`。封存是密钥这边的总闸：取用和揭示等待，监控和 SSH 通道继续。

| 子命令 | 标志 | 做什么 |
| --- | --- | --- |
| `status` | | `sealed` 或 `unsealed`、模式，以及每条签名失败的策略行（当不存在处理） |
| `init` | `-passphrase-file`、`-passphrase-from-stdin` | 给口令模式设第一句口令并启封。之后的启封用这句口令，不再收新的。口令来自文件（`-passphrase-file`，secrets 目录那种）或 stdin（`-passphrase-from-stdin`；输入时会显示）——从不作为 shell 参数 |
| `unseal` | `-passphrase-file`、`-passphrase-from-stdin` | 重启后启封口令模式。口令来自文件或 stdin——从不作为 shell 参数 |
| `seal` | | 再封上：密钥相关的功能等口令；监控和 SSH 通道继续 |
| `rotate` [COLLECTION] | `-passphrase-file`、`-passphrase-from-stdin` | 不给名字就是新的根密钥：每个库密钥再包一次，几秒内完成（口令模式用 `-passphrase-file` 指名新口令文件）。给名字就是那个库的密钥：条目在后台重新加密。删成员不必轮换——成员从不持有密钥 |

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
| `storage check` | | 数据库的健康：后端自己的检查（SQLite 的一致性检查，或 PostgreSQL 上每一张表）、每个桶各存了多少、审计链的校验 |
| `storage migrate` | `-to` | 把 SQLite 数据库迁入 `storage.dsn_file` 所指的 PostgreSQL，一个事务里完成并在提交前核对条数；创世记录记下这次导入。SQLite 文件保留，配置改成 PostgreSQL 驱动之前仍由它服务；回滚就是把驱动留在 sqlite（迁过去之后写进 PostgreSQL 的不在 SQLite 文件里）。今天的目的地是 `-to postgres` |

**`beacon backup`** —— Beacon 自己的备份；每个子命令都接受 `-config`。单次 S3 PUT 最大 5 GiB。

| 子命令 | 标志 | 做什么 |
| --- | --- | --- |
| `backup now` | `-full` | 立刻做一次（`-full` 带完整录像集）：服务在就走管理 socket，否则自己做 |
| `backup ls` | | 每个目的地里有什么，新的在前 |
| `backup restore` FILE... | `-identity`、`-into`、`-dry-run`、`-confirm`、`-verify-key` | 解密、核对清单签名和每个成员、检查数据库完整性和审计链，再把数据库、录像、配置和 secrets 放回去；`-dry-run` 在临时目录里证明、不动真东西。恢复到正在用的 `data_dir` 需要 `-confirm`（当前文件先旁路复制）。先给完整档案，再给增量。`-identity` 里没有校验密钥时，`-verify-key` 是恢复材料里的 `sign1:…` 行 |
| `backup drill` FILE... | `-identity`、`-keep`、`-verify-key` | 恢复到临时目录、核对，并续上内置的 `recovery-drill` 提醒；`-keep` 打印它留在哪 |
| `backup keygen` | | 一对 age 密钥：私钥只在 stdout 打印一次；公钥给 `backup.recipients` |

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
