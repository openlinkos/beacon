---
sourceHash: sha256-d466ea99
---

# 目录文件格式 {#the-catalogue}

Beacon 监控什么放在它的数据库里，经 API、MCP 和命令行修改；文件用于导入导出，也用于首次启动，布局沿用部署一直使用的 `/etc/beacon`（[检测一个服务](../guide/monitoring.md)）。本页讲这些文件的格式，以及每一处修改无论从哪里进来都要通过的校验；API 和 MCP 收发的也是同样的形状，只是 JSON。通不过的修改以 `config.invalid` 和原因拒绝，什么都不会变。

| 文件 | 放什么 | 配置键 |
| --- | --- | --- |
| `catalog/*.yaml` | 目录：检测点位置、心跳、到期资源、状态页 | `catalog_dir` |
| `targets/*.yaml` | 检测目标，一个集合一个文件，文件名即集合名 | `gateway.targets_dir` |
| `targets/modules.yml` | 检测模块 | `gateway.modules_file` |

三条规则贯穿所有文件：

- **不认未知字段。** 目录文件和目标文件按严格模式读取：下表没有列出的字段会让整个文件校验失败，写错一个键不会悄悄溜过去。走 API 时，同样的检查拒绝 JSON 请求体里的未知字段。
- **目录可以拆成多个文件。** 目录下的每个 `*.yaml` 都会被读取，列表按文件名顺序合并；`catalog/locations.yaml`、`catalog/heartbeats.yaml` 是惯例不是要求。
- **导入是全量的。** `beacon config import DIR` 让目录里的文件成为全部配置，文件里没有的条目会被删掉；先跑 `-dry-run` 看会改什么（[命令行](cli.md#beacon-config)）。

## 位置 {#locations}

```yaml
locations:
  - id: edge-1                         # internal: metrics, cards, configuration; the machine's name
    name: United States - Los Angeles  # public, for the status page: where and on which network, never which vendor
    region: overseas                   # overseas or mainland
    network: CN2 GIA
    address: edge-1.example.net        # CDN nodes: the node's address (a name or an IP) ...
    rotation: cdn.example.com          # ... and its cluster's DNS name
    standby: false                     # true: the node joins the rotation only when another fails
    probe:
      timeout: 30s                     # every probe here gets at least this long (at most 1m)
      retries: 1                       # a probe that fails without an answer is tried again at once (at most 2)
```

| 字段 | 类型 | 规则 |
| --- | --- | --- |
| `id` | 字符串 | 小写字母、数字和连字符，至多 63 个字符；不得重复。资源里若有同名的 `vps`，该位置只被期望报告到那天为止——到期当天整天仍算数——过期后它的令牌不能再写入 |
| `name` | 字符串 | 必填，对外：在哪里、走什么网络，永远不写哪家厂商 |
| `region` | `overseas` 或 `mainland` | 必填；规则会比较两者 |
| `network` | 字符串 | 自由文本，与名字一起展示 |
| `address` | 字符串 | CDN 节点用；有 `rotation` 就必须有它 |
| `rotation` | 字符串 | 集群的 DNS 名，每分钟解析一次（[指标与告警规则](metrics.md)） |
| `standby` | 布尔 | 为 true：节点只在另一个节点故障时才加入轮换；需要 `rotation` |
| `probe.timeout` | 时长 | 该位置任何一次检测至少有这么多时间，0 到 `1m`（agent 的周期）；零表示沿用各模块的超时 |
| `probe.retries` | 0、1 或 2 | 没有得到应答的失败（`dns`、`connect`、`timeout`）立刻再试这么多次；有应答的失败从不重试 |

## 心跳 {#heartbeats}

```yaml
heartbeats:
  - name: nas-backup          # lower-case letters, digits and dashes
    max_age: 26h              # at least 1m
    severity: warning         # critical, warning or info
    description: Nightly backup of the NAS
    machine: nas-1            # optional: the machine the job runs on, as the SSH channel names it
```

| 字段 | 类型 | 规则 |
| --- | --- | --- |
| `name` | 字符串 | 小写字母、数字和连字符，至多 63 个字符；不得重复 |
| `max_age` | 时长 | 至少 `1m`；成功过多久还不上报就触发 `HeartbeatStale` |
| `severity` | `critical`、`warning` 或 `info` | 告警的级别 |
| `description` | 字符串 | 自由文本 |
| `machine` | 字符串 | 可选；机器名，小写字母、数字和连字符。写入（API、MCP）会拒绝 SSH 通道没有的机器，除非心跳本来就叫这台；导入和回退接受，附一条警告 |

任务怎么上报成功、心跳怎么变成 Agent 能着手的东西，见[心跳与到期资源](../guide/heartbeats.md)。

## 资源 {#resources}

会到期的东西。只有名字、类别和日期；没有价格。

```yaml
resources:
  - {name: edge-1, kind: vps, expires: 2027-04-02}
  - {name: example.com, kind: domain, expires: 2027-01-15, note: renews automatically}
```

| 字段 | 类型 | 规则 |
| --- | --- | --- |
| `name` | 字符串 | 必填，不得重复 |
| `kind` | `vps`、`domain`、`cert`、`token` 或 `service` | 必填 |
| `expires` | 日期 | `YYYY-MM-DD` |
| `note` | 字符串 | 自由文本 |

导出为 `beacon_resource_expiry_timestamp_seconds{resource, kind}`；`ResourceExpiring` 提前 30 天警告、提前 7 天转紧急。与位置同名的 `vps` 同时约束那个位置（[位置](#locations)）。

## 状态页 {#status-pages}

状态页是目录的一部分，放在 `status_pages` 下，和其他条目一样被修改、版本化和导入。这里是字段；页面展示什么、完整示例见[发布状态页](../guide/status-pages.md#define-your-own-page)。

| 字段 | 类型 | 规则 |
| --- | --- | --- |
| `name` | 字符串 | 小写字母、数字和连字符；不得重复；页面地址 `/status/<name>` |
| `title`、`description` | 文本 | 一个字符串，或按语言各一份（`{en: …, zh: …}`）；title 必填，至多 120 字符；description 至多 1000 |
| `host` | 主机名 | 可选；也以这个主机在 `/` 提供页面；不带端口、不带协议、不得与其他页重复 |
| `default` | 布尔 | 至多一页是 `default` |
| `brand` | `beacon`、`linkos`、`linkos-status`、`linkos-mirrors`、`openlinkos` | 穿哪套设计系统的品牌 |
| `languages` | 列表 | `en` 和 `zh`，各一次，第一个是默认 |
| `components` | 列表 | 至少一个；每个要有 `name` 和 `title`，以及 `services`（其检测目标的 service 标签）或 `page`（目标上的一个 page 标签） |
| `groups` | 列表 | 可选的分组标题；每组只能列页面自己的组件，每个组件至多归入一组 |
| `alerts` | 告警名列表 | 哪些告警算事件；默认是可用性告警 |
| `notes` | `public` 或 `all` | 哪些备注作为更新发布：以 `public:` 开头的（默认）或全部 |
| `links`、`menu` | 链接 | 只接受 `https`、`http` 或 `mailto` 链接 |

## 目标集合 {#target-sets}

目标集合是 Prometheus file service discovery 格式的组列表：目标，加上它们共享的标签。每个集合是目标目录下的一个文件，文件名是集合名加 `.yaml`。

```yaml
- targets: [https://example.com/healthz]
  labels: {module: http_head, service: example.com, severity: critical}
```

| 字段 | 类型 | 规则 |
| --- | --- | --- |
| `targets` | 列表 | 必填；空组会让文件校验失败 |
| `labels.module` | 字符串 | 必填；模块文件里的一个名字 |
| `labels.service` | 字符串 | 必填；结果归属什么：告警、卡片和状态页都按服务说话 |
| 其他标签 | 字符串 | 保留：`severity`（`critical`、`warning` 或 `info`）成为告警级别，`failure_domain` 指明什么会一起坏 |

集合名决定哪些位置检测它（API 里显示为 `probed_by`）；别的名字一律失败，写错不会让检测悄悄停掉：

| 集合名（文件名） | 谁来检测 |
| --- | --- |
| `all-…` | 每个位置 |
| `overseas-…`、`mainland-…` | 该地区的位置 |
| `<位置 id>-…` | 只有那个位置；最长的匹配 id 优先 |

## 模块 {#modules}

模块文件在下面这些部分沿用 [Blackbox Exporter 的格式](https://github.com/prometheus/blackbox_exporter/blob/master/CONFIGURATION.md)，现成的模块可以直接用，另有三个 Beacon 自己的 prober。结果沿用 Blackbox Exporter 的指标名（`probe_success`、`probe_duration_seconds`、`probe_http_status_code`、`probe_ssl_earliest_cert_expiry`），另有 `probe_failure_info{reason}` 说明失败原因，`beacon_probe_attempts` 说明试了几次。

| Prober | 字段 |
| --- | --- |
| `http` | `method`、`valid_status_codes`、`follow_redirects`、`fail_if_not_ssl`、`fail_if_body_not_matches_regexp`、`headers`、`preferred_ip_protocol`、`ip_protocol_fallback` |
| `tcp` | `tls`、`query_response`（`expect`、`send`），以及 IP 相关字段 |
| `registry` | 容器仓库：`/v2/`，需要时取 token，再 HEAD 一个 manifest，不拉层。`repository`、`reference`、`username`、`password_file` |
| `git` | HTTP 上的 Git：`info/refs?service=git-upload-pack`。`username`、`password_file` |
| `rdap` | 从 RDAP 查域名到期：`server`（默认 `https://rdap.org`） |

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

每个模块都有 `timeout`（默认 `10s`），位置的 `probe.timeout` 可以加长。`prober` 必须是这五个之一；`preferred_ip_protocol` 只能是 `ip4` 或 `ip6`；正则必须能编译。失败的检测会说明原因，写在 `probe_failure_info{reason}`：

| 原因 | 发生了什么 |
| --- | --- |
| `dns` | 名字没有解析 |
| `connect` | 连接被拒绝或重置 |
| `timeout` | 没有按时应答 |
| `tls` | TLS 握手或证书失败 |
| `not_ssl` | 要求 HTTPS 的地方是明文 HTTP |
| `http_status` | 状态码不符合预期 |
| `body` | 响应体不匹配 |
| `banner` | TCP 服务端说的不是预期的内容 |
| `protocol` | 应答类型不对（content type、JSON） |
| `auth` | 拿不到仓库 token |
| `manifest` | 仓库里没有这个 manifest |
| `config` | 模块或目标本身有错 |

`password_file` 指检测程序密钥目录里的一个文件，默认 `/etc/beacon-probe/secrets`，除非 `beacon-probe` 以 `-secrets-dir` 启动：只写名字如 `git-password`，永远不写路径。带路径分隔符或 `..`、绝对路径或控制字符的值，在写入或导入模块时被拒绝（`config.invalid`），让 `config:write` 的持有者无法让检测程序读它机器上的任意文件；由旧版 Beacon 下发的这种模块，检测程序会拒绝每一次要读它的检测，别的什么都不读。模块文件按导入时的原样保存，注释也在，检测程序逐字节拿到它；改一个模块会以标准布局重写文件，保留其余模块和它们的注释。

`beacon-probe check -modules <file>` 在任何机器上校验模块文件，`beacon-probe run -modules <file> <module> <target>` 手动跑一次检测（[命令行](cli.md#beacon-probe)）。

## 校验 {#the-checks}

汇总：每一处修改——写入、删除、导入、回退——无论从哪里进来都要通过的校验：

| 校验 | 结果 |
| --- | --- |
| 文件或请求体里有未知字段 | 拒绝，`config.invalid`，什么都不会变 |
| 位置、心跳、资源或状态页列出两次 | 拒绝，`config.invalid` |
| 目标集合的名字不以 `all-`、地区或位置 id 开头 | 拒绝，`config.invalid`；删除一个有集合单独以它命名的位置会在这里失败，直到删除或改名那个集合 |
| 心跳写了 SSH 通道没有的机器（写入；导入和回退接受） | 写入被拒绝，导入或回退附一条警告 |
| 模块的 `password_file` 不是纯文件名 | 拒绝，`config.invalid` |

通过的修改立即生效——不用重载、不用重新部署检测程序——并成为历史里的一个版本，记着谁、什么时候、通过什么、为什么改的，可以回退。警告不会拦住修改：心跳的机器没有注册、状态页组件的服务没有任何目标。`beacon check-config` 两类都会报告，什么也不改。
