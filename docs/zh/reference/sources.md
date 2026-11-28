---
sourceHash: sha256-2a668dd9
---

# 数据来源

Beacon 依据别人发给它的东西做判断。这一页列出每个来源：谁发、发到哪里、用什么令牌、什么格式。

机器和工具发到 17006 端口的网关，各用各的令牌，谁也冒充不了别人。人和 Agent 用 17001 端口。这些路径列在 [REST API](api.md#the-gateway) 参考里。

机器写入的每个样本，网关都把 `job` 设成它令牌的种类：`probe`、`host` 或 `heartbeat`，不管机器自己发的是什么。参考规则只从 `job="probe"` 取检测结果，只从 Beacon 自己的抓取（`job="beacon"`）取目录的指标。一次导入最多 200,000 个样本、64 MiB 文本；更大的会被拒绝，返回 `import.too_large`。网关在 VictoriaMetrics 之前先读每个样本；[网关放行什么](#what-the-gateway-passes)说明它丢弃哪些。

| 来源 | 路径 | 令牌 | 格式 |
| --- | --- | --- | --- |
| 检测点 | `POST /api/v1/import/prometheus` | `probe:write`，它自己的检测点 | Prometheus 文本格式，gzip |
| 主机 | `POST /api/v1/import/prometheus`、`POST /api/v1/write` | `host:write`，它自己的主机 | 文本格式，或 Prometheus remote write |
| 心跳 | `POST /api/v1/import/prometheus` | `heartbeat:write` | 每个任务一个样本 |
| 规则引擎 | `POST /api/v2/alerts` | 规则令牌，或 `alerts:send` | Alertmanager API v2 |
| CI 和脚本 | `POST /api/v1/changes` | `changes:write` | Beacon 的变更事件，JSON |
| Flux | `POST /api/v1/changes/flux` | `changes:write` | Flux 的 `generic` provider |
| 告警群 | 飞书长连接，或 `POST /hooks/feishu` | 应用的凭据 | 飞书卡片回调 |
| 人和 Agent | `/api/v1`、`/mcp`、SSH 通道 | 各自的令牌 | JSON、MCP、SSH |

## 检测点 {#probes}

`beacon-probe agent` 跑在每个检测点上，令牌来自 `beacon enroll probe <检测点>`。它每分钟：

1. 从网关取一次要做的事：`GET /api/v1/targets`、`GET /api/v1/modules`，以及检测点的设置 `GET /api/v1/agent`；
2. 检测；
3. 把结果按 Prometheus 文本格式 gzip 后推上来。

断网时，结果先存进磁盘队列。在周期之外，它还挂着一个长轮询，用于[按需检测](../guide/on-call.md#probe-again-now)。

网关按目录给检测点写入的一切打上 `location`、`region`、`network` 和 `host` 标签，再加上 `job="probe"`，取代检测点自己带来的同名标签。不在目录里、或机器已经到期的检测点会被拒绝，返回 `location.inactive`。

结果沿用 Blackbox Exporter 的指标名（`probe_success`、`probe_duration_seconds`、`probe_http_status_code`、`probe_ssl_earliest_cert_expiry` 等）。另有两个指标，分别说明失败原因（`probe_failure_info{reason}`）和试了几次（`beacon_probe_attempts`），见[指标](metrics.md#the-probes-results)。检测点还会推送少量来自 `/proc` 的主机指标，以及写进它 textfile 目录的心跳。

`beacon-probe serve` 则像 Blackbox Exporter 一样提供 `/probe` 和 `/metrics`，给你自己的抓取程序用。

## 主机 {#hosts}

不做检测点的机器以主机身份上报，令牌来自 `beacon enroll host <名字>`：文本格式发到 `POST /api/v1/import/prometheus`，或 Prometheus remote write 发到 `POST /api/v1/write`。网关给它写入的数据打上 `host` 标签和 `job="host"`。主机自己的抓取程序设的 `job` 会被替换，所以它各个抓取任务的序列靠 `instance` 区分。

remote write 用的是 Prometheus 的协议第 1 版，snappy 压缩，Prometheus 和 vmagent 就是这样发的。网关读每个请求（解压后最多 32 MiB），把它放行的序列重新写一遍再转发。VictoriaMetrics 自己的协议（zstd）会被拒绝，返回 `write.encoding`（415），vmagent 收到后会改回 Prometheus 的协议。

## 心跳 {#heartbeats}

必须至少每隔 `max_age` 报告一次成功的任务（备份、定时任务）在成功时推送一个样本，令牌来自 `beacon enroll heartbeat <名字>`：

```bash
curl -fsS -H "Authorization: Bearer $TOKEN" --data-binary \
  "beacon_heartbeat_last_success_timestamp_seconds{heartbeat=\"nas-backup\"} $(date +%s)" \
  https://metrics.example.com/api/v1/import/prometheus
```

这个令牌只能写这一个心跳。网关给样本打上 `heartbeat` 标签（即令牌登记时的名字）和 `job="heartbeat"`，不管样本里写的是什么。其他指标会被拒绝，返回 `heartbeat.metric`；一次超过 100 个样本返回 `import.too_large`；没有登记心跳名字的心跳令牌返回 `heartbeat.unnamed`。每个心跳各用一个令牌。

在装了检测程序的机器上，任务改为把同样一行写进检测程序的 textfile 目录。这个样本只对目录分给这台机器的心跳算数：心跳的 `machine` 是这个检测点的位置，或者主机令牌登记时的名字。没有写 `machine` 的心跳只能由它自己的心跳令牌写入。心跳本身、它的 `max_age` 和级别写在目录里（[心跳](catalogue.md#heartbeats)）。

检测程序只从 textfile 目录发送它自己的行：检测结果（`probe_…`）和心跳以外的 Beacon 指标（`beacon_…`）只由检测程序自己报告，目录里的这些行会被略过。

## 网关放行什么 {#what-the-gateway-passes}

检测点、主机和心跳发来的每个样本，网关都在 VictoriaMetrics 之前先读一遍，只放行说得清楚自己是什么的：

- **普通的文本格式。** 一行是 `name{label="value",…} value [timestamp]`，指标名写在大括号前面，标签名不加引号。VictoriaMetrics 还认大括号里带引号的指标名，以及代替前面指标名的 `__name__` 标签；网关两种都不放行。超过 64 KiB 的行不算样本。
- **每个标签只写一次。** 同一个标签写了两次的样本或 remote write 序列不放行。
- **心跳归它的机器。** 检测点或主机只能为目录里 `machine` 是这个检测点位置或这台主机的心跳写 `beacon_heartbeat_last_success_timestamp_seconds`。

检测点或主机发来的、不放行的样本被丢弃，导入的其余部分照常写入，`beacon_gateway_samples_dropped_total` 按机器（`subject`）和原因（`reason`：`syntax`、`name_label`、`labels` 或 `heartbeat`）计数。每种情况第一次出现时写日志。心跳令牌的导入里有这样的行，整个导入被拒绝：别的指标或 `__name__` 标签返回 `heartbeat.metric`，其余返回 `bad_input`。

## 规则引擎 {#the-rule-engine}

vmalert 在 VictoriaMetrics 上计算规则，像发给 Alertmanager 一样把触发的告警发给网关：

```bash
vmalert -notifier.url=http://beacon:17006 -notifier.bearerTokenFile=/etc/vmalert/rules-token   # rules.token_file 指的那个文件
```

告警需要标签 `severity`（`critical`、`warning` 或 `info`）和 `service`，以及注释 `summary`。有 `description`、`runbook` 和 `dashboard` 时也会用上。名为 `Watchdog`、一直在触发的告警用来证明整条链路正常，不会展示。

别的发送方也可以用 `alerts:send` 令牌这样发告警。它的告警只属于它自己。Beacon 给它们打上 `sender` 标签（令牌的名字），不管告警里写的是什么，并和规则引擎的告警分开分组。它们不能解除或改动规则引擎或其他发送方的告警，也只能抑制这个发送方自己的告警。它们的 `runbook` 和 `dashboard` 注解不会取代配置的链接。只有规则引擎的 `Watchdog` 算数，也只有规则引擎能发级别为 `none` 的告警。令牌发来的这两种会被丢弃，计入 `beacon_gateway_alerts_dropped_total`。一个令牌每分钟最多发 120 次，超出返回 `429` 和 `alerts.too_many`。每次最多放行 200 条告警，多出的被丢弃，计入 `beacon_gateway_alerts_capped_total`。一个令牌同时最多有 500 条正在告警、50 个告警组，免得它把告警群、电话和 Agent 的 hook 淹没：超出任何一项的新告警被丢弃，计入 `beacon_router_alerts_capped_total`，每小时最多记一次日志；它已有的告警照常更新和解除。规则引擎从不受限。

参考部署自带的规则列在[指标与告警规则](metrics.md#the-alert-rules)里。

## 变更事件 {#change-events}

部署和配置变更以变更事件的形式送到 Beacon，这样告警的证据就能说出之前刚改了什么。变更事件从不叫醒任何人。

- CI 任务或脚本把 Beacon 自己的 JSON 事件发到 `POST /api/v1/changes`。
- Flux 的 notification-controller 经一个类型为 `generic` 的 Provider，把它的事件发到 `POST /api/v1/changes/flux`。

两者都需要 `changes:write` 令牌，每分钟最多 60 个事件。字段和 Flux 的配置见[部署与 Flux](../integrations/change-events.md)。

## 告警群 {#the-chat}

人在告警群的卡片上操作：带备注认领，或静默 1、4 或 24 小时。只有 `feishu.on_call` 里列出的人能操作。

飞书经服务保持的长连接（`feishu.callback: websocket`）把卡片回调交给 Beacon。或者用 17001 端口的 `POST /hooks/feishu`（`feishu.callback: http`），用应用的 Encrypt Key 加密并签名，每个回调只接受一次，且要在时间戳的 5 分钟以内。应用那一侧怎么配，见[配置飞书应用](../integrations/feishu.md)。

在 Pushover 里确认的紧急通知，在 Beacon 里也算认领：Beacon 会跟踪这条通知的回执。

## 人和 Agent {#people-and-agents}

备注、认领、静默、配置修改和机器上的会话，经 REST API、MCP 和 SSH 通道进来，每次都带着操作者自己的令牌。每次写操作都记审计，并回显到告警群。见 [REST API](api.md)、[MCP 工具](mcp.md)和[给 Agent 一台机器](../guide/machines.md)。
