# 目录、检测目标和检测模块

[English](../catalog.md) | 简体中文

除了配置文件，Beacon 还读三类文件。收到 `SIGHUP` 时都会重新读取，`linkos-beacon check-config` 会和配置一起校验它们。

| 内容 | 位置（配置项） | 谁用 |
| --- | --- | --- |
| 目录：检测点、心跳、会到期的资源 | `catalog_dir` 下的所有 `*.yaml` | Beacon：导出成指标给规则用，并据此给检测点写入的数据打标签 |
| 检测目标 | `gateway.targets_dir` 下的所有 `*.yaml` | 网关：给每个检测点发它自己的列表 |
| 检测模块 | `gateway.modules_file`（默认 `<targets_dir>/modules.yml`） | 网关：发给每个检测点 |

目录可以拆成多个文件，各列表会合并。

## 检测点

检测点是一台跑 `linkos-probe agent` 的机器。它的令牌来自 `linkos-beacon enroll probe <id>`，网关给它写入的所有数据加上 `location`、`region`、`network`、`host` 标签，检测点冒充不了别的检测点。

```yaml
locations:
  - id: edge-la-1                      # 内部用：指标、卡片、配置；就是机器名
    name: United States - Los Angeles  # 公开名（状态页）：写地点和网络，不写厂商
    region: overseas                   # overseas 或 mainland
    network: CN2 GIA
    address: edge-la-1.example.net     # CDN 节点：节点地址（域名或 IP）……
    rotation: cdn.example.com          # ……和所在集群对外的轮转域名
    standby: false                     # true：备用节点，只在别的节点故障时才进轮转
    probe:
      timeout: 30s                     # 这里的每个检测至少给这么长时间（最多 1m）
      retries: 1                       # 没有得到应答就失败的检测，立刻再试（最多 2 次）
```

| 字段 | 含义 |
| --- | --- |
| `id` | 小写字母、数字和 `-`。资源里有同名的 `vps` 时，检测点只在那个日期之前算「应在线」：机器不续费就不再计入，它的令牌也写不进来；续费（改日期）就恢复。 |
| `name` | 公开名，必填。 |
| `region` | `overseas` 或 `mainland`。规则会比较两者（只有大陆访问不了，是线路问题，不是服务故障）。 |
| `network` | 自由文本，和公开名一起显示。 |
| `address`、`rotation` | 可选，CDN 节点用。Beacon 每分钟解析两者，导出 `linkos_location_in_rotation{location, rotation, role}`（节点地址在轮转应答里为 1）、`linkos_rotation_resolve_success`、`linkos_rotation_members`，以及应答里没有检测点认领的地址 `linkos_rotation_unknown_address{rotation, address}`。写了轮转就必须写地址。 |
| `standby` | 备用节点只在别的节点故障时才在轮转里：`role="standby"`（否则是 `primary`），规则可以在主节点不在时告警、备用节点顶上时通知。必须写轮转。 |
| `probe.timeout` | 给线路更慢的机器：网关把它告诉这台的 agent（`GET /api/v1/agent`），这里超时更短的模块都改用它。为 0 时用模块自己的超时。 |
| `probe.retries` | 给连接时不时会断的机器：没有得到应答就失败的检测（`dns`、`connect`、`timeout`）立刻再试，最多这么多次（0 到 2）。得到了应答的失败（比如状态码不对）从不重试。`linkos_probe_attempts` 记下每个结果试了几次，线路不稳仍然看得见。连接已经卡住时，超时再长也没用，换一个新连接重试才有用。 |

Beacon 为每个现在应在线的检测点导出 `linkos_expected_location{location, region, network}`，规则据此发现不再上报的检测点。

## 心跳

必须至少每隔 `max_age` 成功上报一次的任务：备份、定时任务。装了 agent 的机器，任务往它的 textfile 目录写一行（`/var/lib/linkos-agent/textfile/heartbeat_<名字>.prom`，部署里的 `linkos-heartbeat` 脚本就是这么做的）；其他机器用 `linkos-beacon enroll heartbeat <名字>` 发的令牌推送：

```bash
curl -fsS -H "Authorization: Bearer $TOKEN" --data-binary \
  "linkos_heartbeat_last_success_timestamp_seconds{heartbeat=\"nas-backup\"} $(date +%s)" \
  https://metrics.example.com/api/v1/import/prometheus
```

```yaml
heartbeats:
  - name: nas-backup          # 小写字母、数字和 -
    max_age: 26h              # 至少 1m
    severity: warning         # critical、warning 或 info
    description: Nightly backup of the NAS
```

导出为 `linkos_heartbeat_max_age_seconds{heartbeat, severity}`。

## 资源

会到期的东西。只写名字、类别和日期，不写价格。

```yaml
resources:
  - {name: edge-la-1, kind: vps, expires: 2027-04-02}
  - {name: example.com, kind: domain, expires: 2027-01-15, note: 自动续费}
```

`kind` 是 `vps`、`domain`、`cert`、`token` 或 `service`。导出为 `linkos_resource_expiry_timestamp_seconds{resource, kind}`。和检测点同名的 `vps` 同时决定这个检测点的期限（见上）。

## 检测目标

每个文件是一组列表，格式和 Prometheus 的文件服务发现一样。每组都要有标签 `module`（模块文件里的名字）和 `service`（结果属于哪个服务）；其他标签原样保留，规则和卡片都能用。

```yaml
- targets: [https://example.com/healthz]
  labels: {module: http_head, service: example.com, severity: critical}
```

文件名决定哪些检测点去测：

| 文件名 | 谁测 |
| --- | --- |
| `all-*.yaml` | 所有检测点 |
| `overseas-*.yaml`、`mainland-*.yaml` | 该地区的检测点 |
| `<检测点 id>-*.yaml` | 只有这个检测点（按最长的 id 匹配） |

其他文件名会被拒绝。改检测目标只需要让 Beacon 重载，不用重新部署检测点：每个 agent 每分钟取一次自己的列表。

## 检测模块

下表这些部分的写法和 [Blackbox Exporter 的模块文件](https://github.com/prometheus/blackbox_exporter/blob/master/CONFIGURATION.md) 一样，现有的模块照样能用；另有三种 Beacon 自己的检测方式。结果用 Blackbox Exporter 的指标名（`probe_success`、`probe_duration_seconds`、`probe_http_status_code`、`probe_ssl_earliest_cert_expiry` 等），失败时还会说明原因：`probe_failure_info{reason}`，值是 `dns`、`connect`、`timeout`、`tls`、`not_ssl`、`http_status`、`body`、`banner`、`protocol`、`auth`、`manifest` 或 `config`。

| 检测方式 | 字段 |
| --- | --- |
| `http` | `method`、`valid_status_codes`、`follow_redirects`、`fail_if_not_ssl`、`fail_if_body_not_matches_regexp`、`headers`、`preferred_ip_protocol`、`ip_protocol_fallback` |
| `tcp` | `tls`、`query_response`（`expect`、`send`），以及 IP 相关字段 |
| `registry` | 镜像仓库：`/v2/`，需要时取令牌，再 HEAD 一个 manifest，不下载层。`repository`、`reference`、`username`、`password_file` |
| `git` | Git over HTTP：`info/refs?service=git-upload-pack`。`username`、`password_file` |
| `rdap` | 从 RDAP 查域名到期：`server`（默认 `https://rdap.org`） |

每个模块都有 `timeout`（默认 10s），检测点的 `probe.timeout` 可以把它延长。`password_file` 是检测点机器上的路径。

```yaml
modules:
  http_head:
    prober: http
    timeout: 20s
    http:
      method: HEAD
      valid_status_codes: [200]
      follow_redirects: false
      fail_if_not_ssl: true
      preferred_ip_protocol: ip4
      ip_protocol_fallback: false
```

`linkos-probe check -modules <文件>` 校验模块文件，`linkos-probe run -modules <文件> <模块> <目标>` 手动跑一次检测。
