# Beacon · 烽火

[English](README.md) | 简体中文

Beacon 是给做运维的 AI Agent 用的基础设施：从用户所在的地方检测，告诉你服务对用户是否正常；让 Agent 经 SSH 到达你的机器，自己却从不拿到密钥，每次会话都有录制；并把值班的人和他们的 Agent 放在同一个平台上。一个 Go 服务加一个很小的检测点程序，底下是 VictoriaMetrics 和 vmalert。它要做到什么、不做什么，见 [docs/zh/goals.md](docs/zh/goals.md)。

> 名字取自长城的烽燧：散在边境的哨台发现敌情就点火，一台接一台传回都城，烽火的数量表示敌情大小。

项目还在早期。Agent 已经在生产环境里经它干活：看到告警和证据，用自己的 ssh 和 MCP 工具到达登记过的机器而不拿任何密钥，每次会话都有记录；多地检测点的拨测在喂告警。正在做的：第一个端到端值班的 Agent（参考的接收程序和按需重测）、人从卡片点开的页面、NAT 后面的机器，以及给其他团队的参考部署。

## 能做什么

### 看得见：监控

- **从用户所在的地方检测。**`linkos-probe` 跑在任何一台小机器上（CDN 边缘节点、大陆的 VPS），检测 HTTP、TCP、TLS、镜像仓库、Git（HTTP）和域名到期，用法和指标名都和 Blackbox Exporter 一样，并说清每次失败的原因：解析、连接、超时、TLS、状态码、内容不符。它还上报少量主机指标和心跳，推送时带磁盘队列，断网也不丢数据。一个静态程序，内存不到 20 MB。
- **几个地点都说坏了才算坏。**至少两个检测点、并且至少一半有数据的检测点失败，才算服务故障；一个点的抖动永远不会叫醒人。检测点跟着机器走：机器不续费，这个检测点就不再计入。
- **机器只有一个入口。**网关给每个检测点和主机发各自的令牌（`linkos-beacon enroll`），并给它写入的数据打标签，谁也冒充不了别人。检测目标、检测模块和各检测点的设置（比如给线路不稳的机器更长的超时）也由网关下发，改检测内容不用重新部署；心跳、规则引擎和 Flux 的告警、Agent 的指标查询也都走网关。
- **自带告警路由。**分组、抑制、静默，和 Alertmanager 一样，但在服务里完成；规则引擎像发给 Alertmanager 一样把告警发过来即可。
- **飞书告警卡片。**新告警会变成告警群里的一张交互卡片：出了什么问题、从什么时候开始、哪些检测点在失败，附处理手册和面板的链接。卡片上的按钮可以认领（可写备注），或者静默 1、4、24 小时。卡片原地更新，告警恢复后变绿。
- **升级到手机。**严重告警没人认领时，10 分钟后在飞书里加急，15 分钟后发 Pushover 紧急通知：手机会一直响，直到有人在 Pushover 里确认，这也算在这里认领。告警还在触发时，认领 4 小时后失效，重新开始升级。
- **不会悄悄丢掉。**低级别告警、以及超出飞书应用每月 API 额度的告警，改由群自定义机器人发送（不占额度）。卡片发不出去时，严重告警直接发 Pushover。规则、路由、送达都正常时，服务每分钟 ping 一次 healthchecks.io，所以监控主机挂了、飞书连接断了，手机照样会收到。
- **CDN 节点是否在轮转里。**检测点在 CDN 节点上时，Beacon 会发现节点被踢出所在集群的解析（机器在线、还在检测，但已经不服务用户）、备用节点顶上，也会发现集群里有没人看管的节点。

### 够得着：SSH 通道

- **Agent 用它熟悉的工具。**ssh、scp、sftp、rsync 和端口转发经 Beacon 照常使用；MCP 工具可以执行命令、读文件、改文件，返回干净的结构化结果。
- **Agent 不拿密钥。**凭据由 Beacon 保管，每次连接签一张几分钟有效的证书；机器只信任 Beacon 的 CA。
- **每次会话都有记录。**会话录制下来可以回放，交互 shell 拆成一条条命令并带退出码。执行什么由 Agent 自己判断；它做的每件事人都看得到，也能一键暂停所有 Agent。

### 连得上：平台

- **给 Agent 用的 API 和 MCP。**列出告警、认领、静默、结束静默、写备注，令牌按作用域授权、会过期。每次写操作都记审计，并回显到告警群，Agent 做了什么所有人都看得到。
- **唤醒 Agent，证据随手可取。**新告警可以唤醒 Agent：一个带签名的 webhook，或一次 GitHub workflow 触发，告诉它证据在哪里。一次调用就能拿到证据：每个检测点的结果和失败原因、同一故障域里的告警、之前刚做的变更（部署、Flux），以及已经做了什么。Agent 留的备注显示在卡片上，一条时间线串起告警、备注、唤醒的 Agent、变更和会话。Beacon 不调用任何模型，由 Agent 自己判断。见 [docs/zh/agents.md](docs/zh/agents.md)。
- **配置在 Beacon 里。**检测点、心跳、会到期的资源、检测目标和检测模块存在 Beacon 的数据库里，通过 API、MCP 和命令行修改，校验后立即生效。每次修改都是一个版本，记下谁改的、改前改后的值，可以撤销；文件只用来导入和导出，所以仍然可以放在 git 里。
- **很轻。**一整套只有四个容器（Beacon、VictoriaMetrics、vmalert、Caddy），每个检测点一个程序。

## 结构

```
linkos-probe（各检测点）─推送─▶ 网关 ─▶ VictoriaMetrics ─▶ vmalert ─告警─▶ 路由 ─▶ 告警桥 ─▶ 飞书应用 ◀─▶ 值班的人
主机、CI、Flux ───────────────▶ 网关                                                   ├─▶ 飞书群机器人
Agent ─▶ /api/v1、/mcp ─▶ linkos-beacon                                                ├─▶ Pushover / Bark
                                                                                       ├─▶ Agent 的钩子（webhook、GitHub workflow）
                           healthchecks.io ◀─ 规则、路由、送达都正常时每分钟一次
```

网关、路由、告警桥都在 `linkos-beacon` 里。告警需要标签 `severity`（`critical`、`warning` 或 `info`）和 `service`，以及注释 `summary`（可选 `description`、`runbook`、`dashboard`）。名为 `Watchdog` 的告警用来证明整条链路正常，不会展示。

## 运行

```bash
docker run -d --name linkos-beacon -p 127.0.0.1:17001:17001 -p 127.0.0.1:17006:17006 -p 17002:17002 \
  -v ./config.yaml:/etc/linkos-beacon/config.yaml:ro \
  -v ./catalog:/etc/linkos-beacon/catalog:ro \
  -v ./targets:/etc/linkos-beacon/targets:ro \
  -v ./secrets:/etc/linkos-beacon/secrets:ro \
  -v linkos-beacon:/var/lib/linkos-beacon \
  ghcr.io/openlinkos/beacon:latest
```

从 [config.example.yaml](config.example.yaml) 开始改；`linkos-beacon check-config` 会校验配置、目录（检测点、心跳、会到期的资源）、检测目标和检测模块文件，写法见 [docs/zh/catalog.md](docs/zh/catalog.md)。第一次启动时这些文件会导入数据库，此后在数据库里修改；上面两个目录只在这时、收到 `SIGHUP` 时和 `config import` 时才用到。17001 端口提供 API 和 MCP，以后也提供状态页；17006 端口是给机器用的网关；17002 端口是 SSH 通道，配置见 [docs/zh/ssh.md](docs/zh/ssh.md)。

让 vmalert 把告警发给网关：`-notifier.url=http://linkos-beacon:17006 -notifier.bearerTokenFile=<rules.token_file 里那个文件>`。

给每个检测点发令牌，再在那台机器上启动检测程序：

```bash
docker exec linkos-beacon linkos-beacon enroll probe edge-1 > token     # edge-1 是目录里的一个检测点
linkos-probe agent -gateway https://metrics.example.com -token-file /etc/linkos-agent/token
```

飞书应用按 [docs/zh/feishu.md](docs/zh/feishu.md) 配置。Pushover 需要装 iOS 应用并允许紧急通知（Critical Alerts），再准备应用令牌和用户密钥。

### 令牌、机器与演练

改动运行中服务的命令，经数据目录里的一个 socket 和它通信：

```bash
docker exec linkos-beacon linkos-beacon token create claude-code -scopes status:read,alerts:write,machines:ssh -expires 90d \
  -ssh-key "$(cat ~/.ssh/id_ed25519.pub)"                 # 它登录 SSH 通道用的公钥
docker exec linkos-beacon linkos-beacon token list
docker exec linkos-beacon linkos-beacon machine add edge-1 edge-1.example.net   # 固定主机密钥，并打印机器要做的配置
docker exec linkos-beacon linkos-beacon ssh pause         # 关闭 Agent 在机器上的会话，直到 ssh resume
docker exec linkos-beacon linkos-beacon enroll probe edge-1 -rotate    # 换一个新令牌，旧的立即失效
docker exec linkos-beacon linkos-beacon drill fail 15m    # /drill/healthz 返回 503：检测它，就能演练整条链路
docker exec linkos-beacon linkos-beacon drill off
docker exec linkos-beacon linkos-beacon config import /etc/linkos-beacon -dry-run   # catalog/、targets/：看看会改什么
docker exec linkos-beacon linkos-beacon config export /tmp/beacon   # 把配置按同样的目录结构写成文件
docker exec linkos-beacon linkos-beacon config history    # 谁改了什么；config revert 版本号 撤销一次修改
```

## API 与 MCP

- REST：`/api/v1`，说明见 [internal/api/openapi.yaml](internal/api/openapi.yaml)（也在 `/api/v1/openapi.yaml` 提供）。
- MCP：`/mcp`，streamable HTTP，用同样的 bearer 令牌。告警相关的工具：`status_summary`、`list_alerts`、`get_alert`、`list_silences`、`list_audit`、`ack_alert`、`silence_alert`、`expire_silence`。处理告警用的工具：`get_evidence`、`get_timeline`、`add_note`、`list_changes`。机器相关的工具：`list_machines`、`run_command`、`read_file`、`write_file`、`edit_file`、`list_sessions`、`get_session`、`add_machine`、`remove_machine`、`pause_ssh`、`resume_ssh`。配置相关的工具（修改需要 `config:write` 作用域）：`get_config`、`put_config`、`delete_config`、`list_config_changes`、`get_config_change`、`revert_config_change`。

```bash
claude mcp add --transport http linkos-beacon https://status.example.com/mcp --header "Authorization: Bearer lbt_…"
```

| 路径（17001 端口） | 用途 |
| --- | --- |
| `GET /healthz`、`GET /readyz` | 存活；就绪（收到过 Watchdog、飞书连接正常、额度未用完） |
| `GET /metrics` | Prometheus 指标，只接受直连 |
| `POST /hooks/feishu` | `feishu.callback: http` 时的飞书卡片回调 |
| `GET /drill/healthz` | 平时 200，演练期间 503 |

| 网关路径（17006 端口） | 作用域 | 用途 |
| --- | --- | --- |
| `POST /api/v1/import/prometheus` | `probe:write`、`host:write`、`heartbeat:write` | 文本格式的样本（`linkos-probe` 推送的）、心跳 |
| `POST /api/v1/write` | `probe:write`、`host:write` | Prometheus remote write |
| `GET /api/v1/targets`、`GET /api/v1/modules`、`GET /api/v1/agent` | `probe:write` | 检测什么、怎么检测、所在检测点的设置 |
| `POST /api/v2/alerts` | 规则引擎的令牌、`alerts:send` | 告警，Alertmanager API v2 格式 |
| `POST /api/v1/changes`、`POST /api/v1/changes/flux` | `changes:write` | 变更事件：部署之类，以及 Flux 的事件（它的 `generic` provider）；见 [docs/zh/agents.md](docs/zh/agents.md) |
| `GET /api/v1/query`、`query_range`、`series` 等 | `metrics:read` | 给 Agent 的指标查询 |

SSH 通道（17002 端口）接受登记在带 `machines:ssh` 作用域的令牌上的公钥；用户名就是机器名。见 [docs/zh/ssh.md](docs/zh/ssh.md)。

## 安全

令牌只存 SHA-256 摘要，并且会过期；按客户端地址限制失败次数。检测点和主机的令牌只能以自己的名义写入，机器到期的检测点不能再写。只有 `feishu.on_call` 里列出的人能操作卡片。密钥从文件读取，从不写进日志。安全问题请发到 security@linkos.io。

## 许可

[Apache License 2.0](LICENSE)。LinkOS、OpenLinkOS 的名称和标志不在此许可范围内。
