---
sourceHash: sha256-f93d1a8d
---

# 架构

Beacon 是怎样搭起来的：有哪些进程，状态存在哪里，一条告警走过的路，Agent 怎样被唤醒、怎样到达机器，以及每个部分都守的规矩。这一页写给运行 Beacon、想知道事情在哪里发生的人，也写给贡献者；为什么这样做见[目标](goals.md)，改代码的规矩在仓库的 `AGENTS.md` 里。

## 全貌 {#the-whole-picture}

```
beacon-probe 检测程序 ──推送（文本格式，gzip）──▶ 网关 ──▶ VictoriaMetrics ◀── vmalert（规则）
  ▲ 检测目标、检测模块、设置 ◀── 网关                                              │ 告警（Alertmanager API v2）
                                                                                  ▼
人 ◀── 飞书卡片、Pushover ◀── 告警桥 ◀── 路由（分组、抑制、静默）◀── 网关 ◀── CI、Flux（变更事件）
Agent ◀── webhook、workflow 触发 ◀── 告警桥（新告警唤醒它们）
Agent ──▶ REST /api/v1、MCP /mcp ──▶ 告警桥（证据、时间线、备注）、存储、SSH 通道
Agent ──▶ ssh、scp、sftp、rsync（17002 端口）──▶ SSH 通道 ──▶ 机器（五分钟有效的证书；每次会话都录制）
```

三个程序，都用 Go 写，不用 cgo：

| 程序 | 跑在哪里 | 做什么 |
| --- | --- | --- |
| `beacon` | 中心，和 VictoriaMetrics、vmalert 在一起 | 服务本身（`serve`）和它的命令行：`check-config`、`config`、`token`、`enroll`、`machine`、`ssh`、`silence`、`drill`、`healthcheck` |
| `beacon-probe` | 每个检测点：边缘节点、用户所在地的小 VPS | `agent` 把结果推给网关；`serve` 像 Blackbox Exporter 一样等人来抓；`check` 校验检测模块文件；`run` 检测一次 |
| `beacon-oncall` | Agent 所在的机器 | 参考实现：核对钩子的签名，启动你指定的 Agent 程序，把载荷送到它的标准输入 |

## 一个进程，三个端口 {#one-process-three-ports}

`beacon serve` 是一个进程，里面有网关、路由、告警桥、REST API、MCP server、人从卡片点开的页面，以及 SSH 通道。

| 端口 | 给谁 | 谁来回答 |
| --- | --- | --- |
| 17001 | 人和 Agent | REST API（`/api/v1`）、MCP（`/mcp`）、公开页面（`/`、`/status`）、控制台（`/console`，以及卡片链到的页面：`/console/alerts/{id}`、`/console/sessions/{id}`）、`/healthz`、`/readyz`、`/metrics`（只回答直连）和演练用的目标 |
| 17002 | Agent，以及 NAT 后面的机器 | SSH 通道：普通的 SSH，直接对外开放，不经 HTTP 代理 |
| 17006 | 机器 | 网关：检测点、主机、心跳、规则引擎的告警、变更事件、指标查询 |

状态都在数据目录里的一个 bbolt 文件中：告警组和它们的备注、唤醒 Agent 的记录，静默，令牌，机器，SSH 会话的索引，变更事件，配置和它的历史，以及审计日志。SSH 会话的录制是放在旁边 `recordings/YYYY/MM/DD/` 下的文件。指标和规则不归 Beacon 管：存储和查询是 VictoriaMetrics 的事，规则由 vmalert 计算。命令行经数据目录里的管理套接字连到运行中的服务，所以 `token`、`enroll`、`machine`、`ssh`、`silence`、`config` 和 `drill` 不需要网络，也不需要令牌。

一整套只有四个容器（Beacon、VictoriaMetrics、vmalert，以及在前面做 TLS 的 Caddy），每台检测机器一个程序。

## 一条告警走过的路 {#an-alerts-path}

1. **检测点推送。**`beacon-probe agent` 每分钟检测一遍网关下发的目标，把样本按 Prometheus 文本格式推上来，断网时先存进磁盘队列。它的令牌写明了它是哪个检测点；网关按目录给它写入的一切打上检测点、地区、网络和主机标签，所以谁也冒充不了别人，机器已经到期的检测点根本写不进来。
2. **规则判断。**vmalert 在 VictoriaMetrics 上计算规则，把触发的告警按 Alertmanager 的格式、带着规则令牌发到网关的 `POST /api/v2/alerts`。参考规则只有在至少两个检测点、并且至少一半有数据的检测点都失败时，才认为服务故障。
3. **路由分组。**Alertmanager 的活在 Beacon 里完成：分组、`group_wait` 和 `group_interval`、`resolve_timeout`、抑制和静默。告警组在重启后依然保留。名为 `Watchdog` 的告警用来证明整条链路正常，不会展示。
4. **告警桥通知人。**新的告警组变成告警群里的一张卡片；严重告警没人认领时，10 分钟后在飞书应用里加急，15 分钟后发 Pushover 紧急通知。低级别告警、以及超出应用每月额度的告警，改由群自定义机器人发送。规则、路由、送达都正常时，告警桥每分钟 ping 一次 healthchecks.io，所以中心挂了照样有电话打到手机上。
5. **告警桥唤醒 Agent。**配置里的钩子（带签名的 webhook，或一次 GitHub workflow 触发）在告警组第一次通知时调用一次，按配置在恢复时再调用一次。这些调用和送达并行，送达从不等它们。

## Agent 走过的路 {#an-agents-path}

Agent 被钩子唤醒，或者被人叫来，每扇门都用同一个令牌：

- **REST 和 MCP** 调用同样的告警桥方法。`get_evidence` 一次给出每个检测点的结果和原因（来自 VictoriaMetrics）、同一故障域里的告警、之前刚发生的变更事件，以及人和 Agent 已经做了什么。`add_note`、`ack_alert` 和 `silence_alert` 是写操作；每次写操作都记审计，并回显到告警群。
- **按需检测。**`probe_now` 往一块请求板上放一个请求，每个检测点都经网关长轮询这块板；检测这个目标的检测点立刻检测，像平时一样推送样本，再交回答复，这次调用在一分钟内把答复返回。
- **机器**经 SSH 通道到达，见下文。
- **它自己的令牌**在到期前自行续期（`renew_token`），所以没人需要给 Agent 发长期令牌。

来自 Beacon 之外的文本（标签、被检测服务的应答、变更事件、机器打印出来的东西）会去掉控制字符和不可见字符，太长的截断，并在每个带有它的应答里标为 `untrusted`。

## 到达一台机器 {#reaching-a-machine}

Agent 用令牌上的密钥登录 SSH 通道，用户名就是机器名。通道核对这个令牌能否到达这台机器，用钉住的主机密钥连上机器，并出示一张它为这次连接签发、五分钟有效的证书；机器只信任 Beacon 的 CA。Agent 的 `ssh`、`scp`、`sftp`、`rsync` 和 `ssh -L` 原样通过，MCP 工具（`run_command`、`read_file`、`write_file`、`edit_file`）走的也是这条路。

每次会话都录制成 asciicast v3 以便回放，交互 shell 借助 shell 集成标记拆成一条条命令和退出码，告警群会收到执行了什么。`ssh pause` 立刻关掉所有会话，恢复之前拒绝新会话。Beacon 连不到的机器用普通的 `ssh -R` 和为它登记的密钥连出来；Beacon 再经这条连接回头到达它的 sshd。

## 配置 {#configuration}

配置文件写明其余的一切，密钥只以 `*_file` 路径出现。Beacon 监控的内容（检测点、心跳和会到期的资源组成的目录，检测目标组，检测模块）存在数据库里，经 API、MCP 和命令行修改。每次修改都像文件一样校验，立即生效，并成为一个版本，记下谁改的、改前改后的值，可以撤销。文件只用来导入和导出：第一次启动时导入，`SIGHUP` 在此期间没有人经 Beacon 改过时再次导入，`config export` 把它们写回文件。

## 代码包 {#packages}

| 包 | 是什么 |
| --- | --- |
| `cmd/beacon` | 服务和它的命令行；把一切接起来，在第一次启动和收到 `SIGHUP` 时导入配置文件 |
| `cmd/beacon-probe` | 检测程序：推送模式、拉取模式、校验 |
| `cmd/beacon-oncall`、`internal/oncall` | Agent 机器上的参考实现；`contrib/oncall` 里有 Claude Code 和 Codex 的封装脚本、提示词和一个 systemd 单元 |
| `internal/gateway` | 机器的入口：令牌作用域、按目录打标签、到 VictoriaMetrics 的写入和导入代理、给检测点的检测目标、检测模块和设置、规则引擎的告警、变更事件、给 `metrics:read` 的查询代理 |
| `internal/router` | 分组、计时、抑制、静默和 Watchdog |
| `internal/bridge` | 告警组到人（卡片、认领、静默、升级、额度、就绪状态、healthchecks.io 的 ping），以及到 Agent（钩子、证据、时间线、备注、变更事件、清理不可信文本） |
| `internal/probenow` | 按需检测：网关长轮询提供、`probe_now` 等候的那块请求板 |
| `internal/feishu`、`internal/notify` | 飞书应用（卡片、长连接、加急）；Pushover、Bark 和群机器人 |
| `internal/api`、`internal/mcpserver` | REST（`openapi.yaml`）和 MCP，调用同样的告警桥和 SSH 通道方法 |
| `internal/web` | 人从卡片点开的页面，以及打开它们的签名链接 |
| `internal/sshgate` | SSH 通道：凭令牌上的密钥登录、给机器的证书、转接会话和转发的端口、录制和文字记录、工具、暂停、回显到告警群 |
| `internal/store` | bbolt 数据库：告警组、静默、令牌（只存哈希）、机器、会话、变更事件、审计日志 |
| `internal/ctl` | 命令行背后的管理套接字 |
| `internal/catalog`、`internal/targets`、`internal/configdb` | 目录、检测目标组和检测协议的类型；数据库里的配置，带版本、历史和撤销 |
| `internal/probe`、`internal/hostmetrics`、`internal/agent` | 各种检测器（兼容 Blackbox，另有镜像仓库、HTTP 上的 Git 和 RDAP），带失败原因；从 `/proc` 读的主机指标；检测程序的周期和磁盘队列 |
| `internal/inventory` | 对照集群的 DNS 轮转核对 CDN 节点 |
| `internal/config`、`internal/i18n`、`internal/metrics`、`internal/vm`、`internal/clientip`、`internal/version`、`internal/alertmanager` | 配置和密钥；翻译；`/metrics` 输出；VictoriaMetrics 查询；客户端地址；构建信息；Alertmanager 的数据格式 |

## 每个部分都守的规矩 {#rules-every-part-keeps}

- **密钥只从文件读**，文件由配置写明；从不写进日志，也不写进错误信息。
- **令牌只存 SHA-256 哈希**，带作用域和有效期；按客户端地址限制失败次数。检测点或主机的令牌只能以它自己的身份写入。
- **每个操作既是 REST 路径也是 MCP 工具**，走同一个方法，有作用域检查、审计记录和到告警群的回显。错误是 `{"error", "code"}`，错误码保持稳定（[REST API](reference/api.md)）。
- **检测协议只增不改**：只加字段和路径，从不修改或删除，所以不同版本的检测点和网关始终能配合。
- **贴近 Agent 习惯**：SSH、shell、HTTP 加 JSON、Prometheus 查询和 MCP，不自创协议或配置语言。
- **没有审批流程，也不调用模型**：Beacon 记录并展示 Agent 做了什么；拿主意的是 Agent，暂停开关和每台机器的权限是仅有的刹车。
- **给人看的都有翻译**：卡片、提示和群消息有英文和中文；日志、命令行输出和 API 错误保持英文。
