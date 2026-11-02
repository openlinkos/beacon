---
sourceHash: sha256-3969969c
---

# 架构

这一页写给运行 Beacon 的人：告警没送到、Agent 上不了机器的时候，知道该去看哪一部分。也写给参与开发的人。内容按事情发生的顺序展开：什么程序跑在哪里，状态存在哪里，一条告警怎样从检测点走到人和 Agent 那里，Agent 怎样拿到证据、怎样上机器，配置放在哪里，每个代码包负责什么，以及所有部分都遵守的规则。Beacon 为什么这样设计，见[目标](goals.md)；改代码的规则在仓库的 `AGENTS.md` 里。

## 全貌 {#the-whole-picture}

```
beacon-probe 检测程序 ──推送（文本格式，gzip）──▶ 网关 ──▶ VictoriaMetrics ◀── vmalert（规则）
  ▲ 检测目标、检测模块、设置 ◀── 网关                                              │ 告警（Alertmanager API v2）
                                                                                  ▼
人 ◀── 飞书卡片、Pushover ◀── 桥 ◀── 路由（分组、抑制、静默）◀── 网关 ◀── CI、Flux（变更事件）
Agent ◀── webhook、workflow 触发 ◀── 桥（新告警叫醒它们）
Agent ──▶ REST /api/v1、MCP /mcp ──▶ 桥（证据、时间线、备注）、存储、SSH 通道
Agent ──▶ ssh、scp、sftp、rsync（17002 端口）──▶ SSH 通道 ──▶ 机器（五分钟有效的证书；每次会话都录像）
```

图里有三股流向。结果往上走：每个检测点推送给网关，网关写进 VictoriaMetrics。告警往回走：vmalert 计算规则，把触发的告警发给网关，路由把它们分组，桥通知人、叫醒 Agent。Agent 从外面进来：通过 REST 和 MCP 取证据、写备注，通过 SSH 通道上机器。

## 什么跑在哪里 {#what-runs-where}

三个程序，都用 Go 写，不依赖 cgo：

| 程序 | 跑在哪里 | 做什么 |
| --- | --- | --- |
| `beacon` | 中心（运行 Beacon 的机器），和 VictoriaMetrics、vmalert 在一起 | 服务本身（`serve`）和它的命令行：`check-config`、`config`、`token`、`enroll`、`machine`、`ssh`、`silence`、`drill`、`healthcheck` |
| `beacon-probe` | 每个检测点：边缘节点，或者用户所在地的小 VPS | `agent` 把结果推给网关；`serve` 像 Blackbox Exporter 一样等别人来抓取；`check` 校验检测模块文件；`run` 检测一次 |
| `beacon-oncall` | Agent 所在的机器 | 参考实现的接收程序：核对钩子的签名，启动你指定的 Agent 程序，把载荷交给它的标准输入 |

参考部署（`deploy/`）在中心上跑四个容器：Beacon（内存上限 256 MB）、VictoriaMetrics（1 GB）、vmalert（256 MB），以及在前面做 TLS 的 Caddy（128 MB）；整套空闲时约占 200 MB 内存。每台检测机器跑一个 `beacon-probe`，常驻内存 15 到 20 MB，systemd 给它的上限是 64 MB。

## 一个进程，三个端口 {#one-process-three-ports}

`beacon serve` 是一个进程，里面有网关、路由、桥、REST API、MCP server、控制台和公开页面，以及 SSH 通道。它监听三个端口：

| 端口 | 给谁 | 谁来回答 |
| --- | --- | --- |
| 17001 | 人和 Agent | REST API（`/api/v1`）、MCP（`/mcp`）、公开页面（`/`、`/status`）、控制台（`/console`，以及卡片链接到的页面：`/console/alerts/{id}`、`/console/sessions/{id}`）、`/healthz`、`/readyz`、`/metrics`（只回答直连）和演练用的检测目标 |
| 17002 | Agent，以及 NAT 后面的机器 | SSH 通道：普通的 SSH，直接对外开放，不走 HTTP 代理 |
| 17006 | 机器 | 网关：检测点、主机、心跳、规则引擎的告警、变更事件、指标查询 |

在参考部署里，Caddy 把 17001 端口放在 `https://status.example.com`，把 17006 端口放在 `https://metrics.example.com`；17002 端口原样对外开放。

## 状态存在哪里 {#where-state-lives}

- **一个 bbolt 文件**：数据目录（`data_dir`，即 `/var/lib/beacon`）里的 `status.db`。里面有告警组及其备注和唤醒 Agent 的记录、静默、令牌（只存 SHA-256 哈希）、机器、SSH 会话的索引、变更事件、配置及其历史、控制台的登录，以及审计日志。
- **SSH 会话的录像**：放在它旁边的 `recordings/YYYY/MM/DD/` 下，保留 `ssh.keep` 这么久（默认 180 天）。
- **管理套接字**：同一目录下的 `ctl.sock`，权限 0600。命令行通过它连到运行中的服务，所以 `token`、`enroll`、`machine`、`ssh`、`silence`、`config` 和 `drill` 既不需要网络，也不需要令牌；只有服务自己的用户和 root 能连上。令牌就在这里创建。
- **指标和规则不归 Beacon 管**：检测结果由 VictoriaMetrics 存储和查询（参考部署保留 400 天），规则由 vmalert 计算。

## 一条告警走过的路 {#an-alerts-path}

从服务出故障，到人和 Agent 知道这件事。下面按参考规则和默认设置来讲：

1. **检测点推送。**`beacon-probe agent` 每分钟检测一遍网关下发的检测目标，把样本按 Prometheus 文本格式推上来；断网时先存进磁盘队列。它的令牌写明了它是哪个检测点。网关按目录给它写入的所有数据打上检测点、地区、网络和主机标签，所以一个检测点冒充不了另一个；机器已经到期的检测点根本写不进来（`location.inactive`）。
2. **规则判断。**vmalert 每 30 秒计算一次规则。某个检测点对一个服务连续失败 3 分钟，才算这个检测点失败；至少两个检测点失败，并且不少于有数据的检测点的一半，服务才算故障；这个状态持续 1 分钟后，`ServiceDown` 触发。vmalert 用规则令牌，按 Alertmanager 的格式把它发到网关的 `POST /api/v2/alerts`。从第一次检测失败算起，大约 4 到 5 分钟。
3. **路由分组。**Alertmanager 的活由 Beacon 自己做：告警按 `alertname` 和 `service` 分组（路由规则另有规定的除外）；新的告警组先等 `group_wait`（30 秒），看还有没有别的告警进来，再发出去；之后最多每 `group_interval`（2 分钟）更新一次；抑制和静默会压住它们匹配的告警。一条告警一直触发到规则引擎给它的 `endsAt` 为止，告警恢复时 vmalert 会把 `endsAt` 设成过去的时间；没带 `endsAt` 的告警，在最后一次收到它 `resolve_timeout`（5 分钟）之后恢复。告警组在重启后依然保留。名为 `Watchdog` 的告警一直在触发，用来证明整条链路正常，从不展示。
4. **桥通知人。**新的告警组通过飞书应用变成告警群里的一张卡片。严重告警没人认领时，10 分钟后在飞书应用里加急，15 分钟后发 Pushover 紧急通知；认领之后，升级暂停 `ack_ttl` 这么久（4 小时）。`info` 级别的告警交给群里的自定义机器人发，不占 API 调用额度；应用的月度额度用掉 70% 以后，只有严重告警还走应用。只要 Watchdog 一直送达、告警发得出去，桥就每分钟 ping 一次 healthchecks.io；中心或链路上任何一环停了，ping 也就停了，由 healthchecks.io 从外面报警。
5. **桥叫醒 Agent。**配置里的钩子（带签名的 webhook，或者触发一次 GitHub workflow）在告警组第一次发出通知时调用一次；钩子要求的话（`resolved`），告警恢复时再调用一次，默认在恢复 1 分钟后。这些调用和通知各走各的，通知从不等它们；失败时分别在 5 秒、20 秒、1 分钟和 3 分钟后重试。

## Agent 走过的路 {#an-agents-path}

Agent 不管是被钩子叫醒，还是被人叫来，进每一扇门都用同一个令牌：

- **REST 和 MCP** 调用同样的桥方法。`get_evidence` 一次给全：每个检测点的结果和失败原因（来自 VictoriaMetrics）、同一故障域里的其他告警、故障前刚发生的变更事件，以及人和 Agent 已经做过的事；时间窗口从告警开始前 30 分钟算起。`add_note`、`ack_alert` 和 `silence_alert` 是写操作；每次写操作都记入审计日志，并回显到告警群。
- **按需重测。**`probe_now` 往内存里的一块请求板上放一个请求，每个检测点都通过网关长轮询这块板（每次最长 55 秒）。检测这个目标的检测点立刻检测，像平时一样推送样本，再交回结果；这次调用在等待时间内（默认 45 秒，最长 90 秒）把结果返回。
- **机器**通过 SSH 通道到达，见下一节。
- **它自己的令牌**在到期前自己续期（`renew_token`，每天最多一次），所以没人需要给 Agent 发长期令牌。

来自 Beacon 之外的文本（标签、被检测服务的应答、变更事件、机器打印出来的内容）会去掉控制字符和不可见字符，太长的截断；每个带有这类文本的应答都用一个 `untrusted` 对象列出这些字段。

## 怎样上一台机器 {#reaching-a-machine}

Agent 用令牌上登记的密钥登录 SSH 通道，登录用的用户名就是它要去的机器。通道先核对令牌有没有 `machines:ssh`、能不能到达这台机器，再用固定下来的主机密钥连上机器，出示一张专为这次连接签发的证书：五分钟有效，账户是机器上的账户（默认 `beacon`），证书 ID 写明这次连接、令牌和进来的途径（`beacon 20261101-091724-aa8bba for claude-code via ssh`），设了 `ssh.source_addresses` 时只能从这些地址使用。机器只信任 Beacon 的 CA。Agent 的 `ssh`、`scp`、`sftp`、`rsync` 和 `ssh -L` 原样可用，MCP 工具（`run_command`、`read_file`、`write_file`、`edit_file`）走的也是这条路。

每次会话都录成 asciicast v3，可以回放；交互 shell 借助 shell 集成标记拆成一条条命令和退出码，告警群也会收到执行了什么。`beacon ssh pause` 立刻关掉所有会话，在 `ssh resume` 之前拒绝新会话。Beacon 连不到的机器，用普通的 `ssh -R` 和为它登记的密钥自己连出来，Beacon 再顺着这条连接回头连到它的 sshd。

## 配置 {#configuration}

- **配置文件**（`/etc/beacon/config.yaml`）写明其他一切：端口、VictoriaMetrics、告警群、钩子、SSH 通道。密钥在里面只以 `*_file` 路径出现。这个文件在服务启动时读取。
- **Beacon 监控什么**存在数据库里：目录（检测点、心跳、到期资源、状态页）、检测目标集和检测模块。它们通过 API、MCP 和命令行修改；每次修改都像以前校验文件一样校验，立即生效，并成为一个版本，记下谁改的、改前改后的值，可以撤销。保留最近 10,000 次修改。
- **文件只用来导入和导出。**数据库里还没有配置时，第一次启动导入这些文件；收到 `SIGHUP` 时，只有上次导入之后没人通过 Beacon 改过，才会再导入；`beacon config import DIR` 随时可以导入一个目录，`config export DIR` 把配置写回成文件。

## 代码包 {#packages}

| 包 | 是什么 |
| --- | --- |
| `cmd/beacon` | 服务和它的命令行；把各部分接起来，在第一次启动和收到 `SIGHUP` 时导入配置文件 |
| `cmd/beacon-probe` | 检测程序：`agent`（推送模式）、`serve`（拉取模式，像 Blackbox Exporter）、`check`、`run` |
| `cmd/beacon-oncall`、`internal/oncall` | Agent 机器上的参考接收程序：核对钩子的签名，启动命令行里指定的程序，把载荷交给它的标准输入；`contrib/oncall` 里有 Claude Code 和 Codex 的封装脚本、提示词和一个 systemd 单元 |
| `internal/server` | 17001 端口上的路由：存活和就绪检查、只回答直连的 `/metrics`、飞书的 HTTP 回调、演练用的检测目标、REST API、MCP 和各个页面 |
| `internal/gateway` | 机器的入口：令牌作用域、按目录打标签、到 VictoriaMetrics 的写入和导入代理、给检测点的检测目标、检测模块和设置、规则引擎的告警、CI 和 Flux 的变更事件、给 `metrics:read` 的查询代理 |
| `internal/probenow` | 按需重测：网关的长轮询从中取请求、`probe_now` 在上面等结果的那块请求板 |
| `internal/router` | Alertmanager 的活：分组、`group_wait` 和 `group_interval`、`resolve_timeout`、抑制、静默和 Watchdog；告警组在重启后保留 |
| `internal/bridge` | 告警组到人（卡片、认领、静默、升级、飞书额度、就绪状态、healthchecks.io 的 ping），以及到 Agent（叫醒它们的钩子、证据、时间线、备注、变更事件、清理不可信文本） |
| `internal/feishu`、`internal/notify` | 飞书应用（卡片、长连接、加急）；Pushover、Bark 和群里的自定义机器人 |
| `internal/api`、`internal/mcpserver` | REST（`openapi.yaml`）和 MCP，调用同样的桥方法和 SSH 通道方法 |
| `internal/status` | 公开状态页：组件、组件现在的状态和根据检测点一致判断算出的 90 天历史、来自告警组的事件、维护窗口 |
| `internal/web`、`internal/web/link` | 控制台和公开状态页，基于 `@openlinkos/ui` 的工作台框架（内置在 `static/ol` 下，用 `scripts/update-ui.sh` 更新；asciinema-player 在 `static/` 下），以及卡片打开这些页面用的签名链接 |
| `internal/sshgate` | SSH 通道：Agent 凭令牌上的密钥登录、给机器的证书、转接会话和转发的端口、录像（asciicast v3）和文字记录、工具（执行、读、写、改）、NAT 后面机器的连接器、暂停、回显到告警群 |
| `internal/store` | bbolt 数据库：告警组（带备注和唤醒记录）、静默、令牌（只存哈希，带作用域、主体、有效期、SSH 密钥和机器）、机器、SSH 会话的索引、变更事件、控制台登录、审计日志、演练 |
| `internal/ctl` | 数据目录里的管理套接字，命令行的 `token`、`enroll`、`machine`、`ssh`、`silence`、`config` 和 `drill` 都通过它 |
| `internal/catalog`、`internal/targets` | 检测点、心跳、到期资源和状态页；检测目标文件和检测协议的类型；每次修改都要通过的校验 |
| `internal/configdb` | 数据库里的目录、检测目标集和检测模块文件：带版本的修改、历史和撤销、导入和导出、网关读取的当前配置 |
| `internal/probe`、`internal/hostmetrics`、`internal/agent` | 各种检测器（兼容 Blackbox，另有镜像仓库、HTTP 上的 Git 和 RDAP），带失败原因；从 `/proc` 读取的主机指标；检测程序的周期和磁盘队列 |
| `internal/inventory` | 对照集群的 DNS 轮转核对 CDN 节点 |
| `internal/config`、`internal/i18n`、`internal/metrics`、`internal/vm`、`internal/clientip`、`internal/version` | 配置和密钥；翻译；`/metrics` 输出；VictoriaMetrics 查询；客户端地址；构建信息 |
| `internal/alertmanager` | 只有 Alertmanager 的数据格式：规则引擎发的是这种格式，路由也认这种格式 |
| `internal/testutil` | 一个接了假依赖的桥，给上面那些包的测试用 |

## 所有部分都遵守的规则 {#rules-every-part-keeps}

- **密钥只从文件读取**，文件由配置写明；密钥从不写进日志，也不写进错误信息。
- **令牌只存 SHA-256 哈希**，带作用域和有效期。同一个客户端地址 10 分钟内失败 20 次，这 10 分钟剩下的时间里就会被挡住（API、控制台登录和网关共用一个计数；SSH 通道单独计数，上限相同）。检测点或主机的令牌只能以它自己的身份写入。
- **每个操作既是 REST 接口，也是 MCP 工具**，走同一个方法，都有作用域检查、审计记录和到告警群的回显。错误是 `{"error", "code"}`，错误码保持稳定（[REST API](reference/api.md)）。
- **检测协议只增不改**：只加字段和接口，从不修改或删除，所以不同版本的检测点和网关始终能配合。
- **贴近 Agent 的习惯**：用 SSH、shell、HTTP 加 JSON、Prometheus 查询和 MCP，不自创协议或配置语言。
- **没有审批流程，也不调用模型**：Beacon 记录并展示 Agent 做了什么；拿主意的是 Agent，暂停开关和每台机器的权限是仅有的刹车。
- **给人看的内容都有翻译**：卡片、提示和群消息有英文和中文；日志、命令行输出和 API 错误保持英文。
