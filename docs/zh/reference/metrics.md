---
sourceHash: sha256-c28c8029
---

# 指标与告警规则 {#metrics-and-alert-rules}

本页按家族列出 Beacon 的指标，以及参考部署在 `deploy/rules/` 里自带的每条告警规则。

Beacon 判断所依据的一切都是 VictoriaMetrics 里的指标，每条告警都是在其上计算的 vmalert 规则（[数据来源](sources.md)）。每条规则都指向 `deploy/runbooks/` 里它的手册。

自己的规则也照这样写：vmalert 的格式，像发给 Alertmanager 一样发给网关。带上标签 `severity` 和 `service`，以及注释 `summary`。

## 每个指标从哪来 {#where-each-metric-comes-from}

- **检测点推送**检测结果和各位置的主机指标到网关，网关按目录打标签（[数据来源](sources.md#probes)）。
- **中心导出**自己的状态，在 `/metrics`、17001 端口，只应答直连。VictoriaMetrics 每分钟抓取一次，连同 vmalert 和 VictoriaMetrics 自己，按 `deploy/metrics/scrape.yml` 的配置。
- **检测程序随每次推送**报告它自己，所以每个位置的队列和推送错误按位置可见。

## 中心自己的指标 {#the-centres-own-metrics}

在 `/metrics` 上（参考抓取配置里的 job `beacon`）：

| 指标 | 标签 | 含义 |
| --- | --- | --- |
| `beacon_build_info` | `version`、`go` | 运行中的构建 |
| `beacon_start_time_seconds` | | 服务何时启动 |
| `beacon_bridge_ready` | | 此刻告警能否送到人，1 表示能 |
| `beacon_alert_groups_firing` | `severity` | 触发中的告警组，按级别 |
| `beacon_alerts_firing` | | 路由器持有的触发中告警 |
| `beacon_notifications_total` | `channel`、`result` | 已发送的通知，按渠道和结果 |
| `beacon_feishu_api_calls` | | 本月飞书应用 API 调用，对着预算 |
| `beacon_gateway_requests_total` | `route`、`code` | 指标网关的请求，按路径和状态码 |
| `beacon_gateway_changes_limited_total` | | 因令牌每分钟发送超过 60 个而被拒的变更事件 |
| `beacon_probe_now_requests_open` | | 等检测点应答的按需检测请求 |
| `beacon_probe_now_requests_total` | | Agent 发起的按需检测请求 |
| `beacon_probe_now_answers_total` | `location` | 检测点对按需检测请求的应答 |
| `beacon_probe_now_missing_total` | `location` | 没有按时应答的位置 |
| `beacon_expected_location` | `location`、`region`、`network` | 此刻应该上报的每个位置为 1：谁没了声音一条规则就能查出 |
| `beacon_heartbeat_max_age_seconds` | `heartbeat`、`severity`、`machine` | 心跳多久没上报就告警，来自目录 |
| `beacon_resource_expiry_timestamp_seconds` | `resource`、`kind` | 服务器、域名、证书或令牌何时到期，来自目录 |
| `beacon_rotation_resolve_success` | `rotation` | CDN 集群的 DNS 名是否解析成功 |
| `beacon_rotation_members` | `rotation` | CDN 集群的 DNS 名解析出几个地址 |
| `beacon_location_in_rotation` | `location`、`rotation`、`role` | 位置的 CDN 节点是否在其集群的 DNS 应答里 |
| `beacon_rotation_unknown_address` | `rotation`、`address` | 目录里没有位置认领的一个应答地址 |
| `beacon_token_expiry_timestamp_seconds` | `token`、`subject` | API 或网关令牌何时过期 |
| `go_goroutines`、`go_memstats_heap_inuse_bytes`、`go_memstats_sys_bytes`、`process_resident_memory_bytes` | | 进程本身 |

## 检测结果 {#the-probes-results}

检测结果沿用 Blackbox Exporter 的指标名，为谁写的规则都能用：`probe_success`、`probe_duration_seconds`、`probe_http_status_code`、`probe_ssl_earliest_cert_expiry`。另有两个：`probe_failure_info{reason}` 说明失败原因，`beacon_probe_attempts` 说明试了几次。

网关给每个结果打上目录里的 `location`、`region`、`network` 和 `host` 标签。目标自己的标签（`service`、`severity`、`failure_domain`、`module`）直达规则和卡片。

主机以 `node_*` 指标上报，读自 `/proc`：`node_cpu_seconds_total`、`node_memory_*_bytes`、`node_disk_*_total`、`node_network_*_total`、`node_time_seconds`。心跳是一个样本：`beacon_heartbeat_last_success_timestamp_seconds{heartbeat}`。

## 检测程序自己 {#the-probe-agent-about-itself}

检测程序随每次推送上报这些指标，因此按位置可见：

- `beacon_probe_build_info`
- `beacon_probe_targets`
- `beacon_probe_queue_batches` 和 `beacon_probe_queue_bytes`：磁盘上等着发给网关的内容
- `beacon_probe_push_errors_total`
- `beacon_probe_last_push_timestamp_seconds`

## 告警规则 {#the-alert-rules}

参考规则是四个文件，每组说明自己看哪个指标。`deploy/rules/probes.yml` 里的记录规则（`beacon:` 系列）承载中间步骤，告警建立在它们之上。在那里，一个位置持续失败 3 分钟才算失败，一个服务一天的可用率是 `avg_over_time(beacon:service_up[1d])`。

### 服务可用性 {#service-availability}

`deploy/rules/probes.yml`，基于 `probe_success{job="probe"}`。一个位置说了不算。至少两个位置失败、且不少于上报位置的一半，才算服务中断。

| 告警 | 级别 | 持续 | 何时 |
| --- | --- | --- | --- |
| `ServiceDown` | 目标的 `severity`；没有则 `critical` | 1m | 服务在至少一半上报位置（且至少两个）失败 3 分钟 |
| `ServiceDegraded` | `warning` | 5m | 至少两个位置失败，但不到一半：部分故障，通常是一个 CDN 节点或一条网络路径 |
| `MainlandPathDegraded` | `warning` | 5m | 所有大陆位置失败而海外成功：是从中国大陆出去的路径，不是服务 |
| `ProbeLocationDown` | `warning` | 5m | 一个位置 10 分钟没有上报（`beacon_expected_location` 对 `probe_success`） |

### 会到期的东西 {#things-that-run-out}

`deploy/rules/expiry.yml`，基于目录的导出、检测点看到的证书和心跳的样本。

| 告警 | 级别 | 持续 | 何时 |
| --- | --- | --- | --- |
| `ResourceExpiring` | `warning`，之后 `critical` | — | 目录里的资源 30 天内到期；7 天内转紧急 |
| `CertificateExpiring` | `warning`，之后 `critical` | — | 各位置看到的最早证书到期时间在 14 天内；7 天内转紧急（Let's Encrypt 提前 30 天续期，14 天意味着续期已两次落空） |
| `HeartbeatStale` | 心跳自己的 `severity` | — | 任务没有在它的 `max_age` 内上报成功 |
| `HeartbeatMissing` | 心跳自己的 `severity` | 1d | 目录里的心跳从未上报（新的心跳有一天先成功一次） |

### DNS 轮换 {#dns-rotations}

`deploy/rules/cdn.yml`，基于 `beacon_location_in_rotation` 及其同类，面向写了地址和轮换的 CDN 节点。

| 告警 | 级别 | 持续 | 何时 |
| --- | --- | --- | --- |
| `CDNNodeOutOfRotation` | `warning` | 15m | 主节点活着，集群 DNS 却不回答它的地址：它不服务任何人 |
| `CDNStandbyInRotation` | `info` | 5m | 备用节点在服务：某个主节点故障或被摘除 |
| `CDNRotationUnknownAddress` | `warning` | 30m | 轮换回答了一个目录里没有位置认领的地址：没人在看的节点 |
| `CDNRotationResolveFailing` | `warning` | 10m | 轮换的 DNS 名从监控主机解析不出来 |

### 监控本身 {#the-monitoring-itself}

`deploy/rules/meta.yml`，基于对中心、vmalert 和 VictoriaMetrics 的抓取（`deploy/metrics/scrape.yml`）。

「监控挂了」不是这里的规则。Watchdog 一直在触发。Beacon 收到它且告警能送达时，每分钟 ping 一次 healthchecks.io。整台机器停下时 ping 也停下，healthchecks.io 从外部报警。

| 告警 | 级别 | 持续 | 何时 |
| --- | --- | --- | --- |
| `Watchdog` | `none`（从不展示） | — | 链路正常时一直在触发；Beacon 把它计入就绪判断 |
| `VMAlertRuleErrors` | `warning` | 5m | vmalert 评估某个规则组失败；组里的告警可能不触发 |
| `VictoriaMetricsDiskLow` | `warning` | 10m | 数据卷可用空间低于 5 GB；低于下限后 VictoriaMetrics 不再收数据 |
| `GatewayErrors` | `warning` | 10m | 网关某条路径连续 10 分钟答服务器错误，通常是 VictoriaMetrics 拒写 |
| `ProbeQueueBacklog` | `warning` | 15m | 某位置的检测程序磁盘队列超过 8 MB；到 64 MB 会丢最老的批次 |
