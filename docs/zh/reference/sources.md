---
sourceHash: sha256-02f8535b
---

# 数据来源

Beacon 依据别人发给它的东西做判断。这一页列出每个来源：谁发、发到哪里、用什么令牌、什么格式。机器和工具发到 17006 端口的网关，各用各的令牌，谁也冒充不了别人；人和 Agent 用 17001 端口。这些路径列在 [REST API](api.md#the-gateway) 参考里。

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

`beacon-probe agent` 跑在每个检测点上，令牌来自 `beacon enroll probe <检测点>`。它每分钟从网关取一次要做的事（`GET /api/v1/targets`、`GET /api/v1/modules`，以及检测点的设置 `GET /api/v1/agent`），检测，再把结果按 Prometheus 文本格式 gzip 后推上来，断网时先存进磁盘队列。它在周期之外还挂着一个长轮询，用于[按需检测](../guide/on-call.md#probe-again-now)。

网关按目录给检测点写入的一切打上 `location`、`region`、`network` 和 `host` 标签，取代检测点自己带来的同名标签；不在目录里、或机器已经到期的检测点会被拒绝，返回 `location.inactive`。结果沿用 Blackbox Exporter 的指标名（`probe_success`、`probe_duration_seconds`、`probe_http_status_code`、`probe_ssl_earliest_cert_expiry` 等），另有 `probe_failure_info{reason}` 说明失败原因，`beacon_probe_attempts` 说明试了几次（[指标](metrics.md#the-probes-results)）。检测点还会推送少量来自 `/proc` 的主机指标，以及写进它 textfile 目录的心跳。

`beacon-probe serve` 则像 Blackbox Exporter 一样提供 `/probe` 和 `/metrics`，给你自己的抓取程序用。

## 主机 {#hosts}

不做检测点的机器以主机身份上报，令牌来自 `beacon enroll host <名字>`：文本格式发到 `POST /api/v1/import/prometheus`，或 Prometheus remote write 发到 `POST /api/v1/write`。网关给它写入的数据打上 `host` 标签。

## 心跳 {#heartbeats}

必须至少每隔 `max_age` 报告一次成功的任务（备份、定时任务）在成功时推送一个样本，令牌来自 `beacon enroll heartbeat <名字>`：

```bash
curl -fsS -H "Authorization: Bearer $TOKEN" --data-binary \
  "beacon_heartbeat_last_success_timestamp_seconds{heartbeat=\"nas-backup\"} $(date +%s)" \
  https://metrics.example.com/api/v1/import/prometheus
```

在装了检测程序的机器上，任务改为把同样一行写进检测程序的 textfile 目录。心跳本身、它的 `max_age` 和级别写在目录里（[心跳](catalogue.md#heartbeats)）。

## 规则引擎 {#the-rule-engine}

vmalert 在 VictoriaMetrics 上计算规则，像发给 Alertmanager 一样把触发的告警发给网关：

```bash
vmalert -notifier.url=http://beacon:17006 -notifier.bearerTokenFile=/etc/vmalert/rules-token   # rules.token_file 指的那个文件
```

告警需要标签 `severity`（`critical`、`warning` 或 `info`）和 `service`，以及注释 `summary`；有 `description`、`runbook` 和 `dashboard` 时也会用上。名为 `Watchdog`、一直在触发的告警用来证明整条链路正常，不会展示。别的发送方也可以用 `alerts:send` 令牌这样发告警。参考部署自带的规则列在[指标与告警规则](metrics.md#the-alert-rules)里。

## 变更事件 {#change-events}

部署和配置变更以变更事件的形式送到 Beacon，这样告警的证据就能说出之前刚改了什么。CI 任务或脚本把 Beacon 自己的 JSON 事件发到 `POST /api/v1/changes`；Flux 的 notification-controller 经一个类型为 `generic` 的 Provider 把它的事件发到 `POST /api/v1/changes/flux`。两者都需要 `changes:write` 令牌，每分钟最多 60 个事件。字段和 Flux 的配置见[部署与 Flux](../integrations/change-events.md)。变更事件从不叫醒任何人。

## 告警群 {#the-chat}

人在告警群的卡片上操作：带备注认领，静默 1、4 或 24 小时。飞书经服务保持的长连接（`feishu.callback: websocket`）把卡片回调交给 Beacon，或者用 17001 端口的 `POST /hooks/feishu`（`feishu.callback: http`），并用应用的令牌校验。只有 `feishu.on_call` 里列出的人能操作。在 Pushover 里确认的紧急通知，在 Beacon 里也算认领：Beacon 会跟踪这条通知的回执。应用那一侧怎么配，见[配置飞书应用](../integrations/feishu.md)。

## 人和 Agent {#people-and-agents}

备注、认领、静默、配置修改和机器上的会话，经 REST API、MCP 和 SSH 通道进来，每次都带着操作者自己的令牌。每次写操作都记审计，并回显到告警群。见 [REST API](api.md)、[MCP 工具](mcp.md)和[给 Agent 一台机器](../guide/machines.md)。
