# 目录、检测目标和检测模块

[English](../catalog.md) | 简体中文

Beacon 监控什么，存在它自己的数据库里：目录（检测点、心跳、会到期的资源）、检测目标和检测模块。人和 Agent 通过 API、MCP 和命令行修改。每次修改都按文件一贯的规则校验，立即生效（不需要重载），并作为一个版本记进历史：谁、什么时候、经什么、为什么改的，都可以查，也可以撤销。文件只用来导入和导出，目录结构和部署一直以来的一样，所以部署仍然可以把它们放在 git 里。

| 内容 | API 和 MCP 里 | 文件（导入、导出） | 谁用 |
| --- | --- | --- | --- |
| 目录：检测点、心跳、会到期的资源 | `locations`、`heartbeats`、`resources` | `catalog/*.yaml`（配置项 `catalog_dir`） | Beacon：导出成指标给规则用，并据此给检测点写入的数据打标签 |
| 检测目标，分成有名字的组 | `targets` | `targets/*.yaml`，每组一个文件（`gateway.targets_dir`） | 网关：给每个检测点发它自己的列表 |
| 检测模块 | `modules` | `targets/modules.yml`（`gateway.modules_file`） | 网关：发给每个检测点 |

本节之后各节用 YAML 给出每一项的写法，和文件里一样；API 和 MCP 收发的是同样结构的 JSON。目录可以拆成多个文件，各列表会合并。

## 修改

### 通过 API 和 MCP

读取需要 `status:read`（或 `config:write`），修改需要 `config:write` 作用域。每一项都按它在文件里的结构读写：

```bash
curl -H "Authorization: Bearer $TOKEN" https://status.example.com/api/v1/config            # 全部，带版本号
curl -H "Authorization: Bearer $TOKEN" https://status.example.com/api/v1/config/targets    # 检测目标组，以及每组由谁检测
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "https://status.example.com/api/v1/config/locations/edge-la-1?note=new+node" \
  -d '{"name": "United States - Los Angeles", "region": "overseas", "network": "CN2 GIA", "probe": {"timeout": "30s"}}'
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/config/targets/all-web \
  -d '[{"targets": ["https://example.com/healthz"], "labels": {"module": "http_head", "service": "example.com"}}]'
curl -X DELETE -H "Authorization: Bearer $TOKEN" https://status.example.com/api/v1/config/heartbeats/nas-backup
```

路径是 `/api/v1/config/{locations,heartbeats,resources,targets,modules}`（列表）和 `…/{id 或名字}`（对单项 `GET`、`PUT`、`DELETE`），说明见 [internal/api/openapi.yaml](../../internal/api/openapi.yaml)。MCP 工具是 `get_config`、`put_config`、`delete_config`，参数是 `kind`（`locations` 等）和 `key`。每次修改返回版本号，以及每一项改前和改后的值；写入和现有一样的值不算修改。通不过校验的修改会被拒绝，返回 `config.invalid` 和原因，什么都不变：还有检测目标组只以某个检测点命名时，这个检测点不能删；组名必须以已知的前缀开头。每次修改都记审计，并回显到告警群，附上备注（如果给了）。`config:write` 决定每个检测点检测什么、怎么检测，包括模块把哪个文件当密码发出去（`password_file`）；检测点只按文件名从它的密钥目录（`/etc/linkos-agent/secrets`，`linkos-probe -secrets-dir`）读这类文件，所以拿着 `config:write` 的人只能让检测点发出别人放在那里的东西，别的拿不到。只给你愿意让他改这些文件的人和 Agent。

修改可以说明它基于哪个版本：`PUT` 和 `DELETE` 加 `?if_version=12`，`put_config` 和 `delete_config` 加 `if_version`，值是 `GET /api/v1/config`（`get_config`）返回的版本号。已有更新的版本生效时，修改被拒绝，返回 `config.conflict` 并指出生效的版本，什么都不变：重新读一遍再做决定。Agent 先读配置、再思考、然后写入时带上它，就不会覆盖别人在这期间改的东西。

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "https://status.example.com/api/v1/config/heartbeats/nas-backup?if_version=12&note=longer+window" \
  -d '{"max_age": "30h", "severity": "warning", "description": "Nightly backup of the NAS"}'
```

### 历史与撤销

`GET /api/v1/config/history`（MCP `list_config_changes`，命令行 `linkos-beacon config history`）按从新到旧列出修改：版本号、时间、谁、经什么（`api`、`mcp`、`cli`；服务自己导入文件时是 `start` 和 `signal`）、做了什么（`put`、`delete`、`import`、`revert`）、新增、修改或删除了哪些项，以及备注。用 `kind` 和 `key` 只看某一项。`GET /api/v1/config/history/{version}`（`get_config_change`）给出每一项改前和改后的值。

`POST /api/v1/config/history/{version}/revert`（`revert_config_change`，`linkos-beacon config revert VERSION`）把一次修改撤销成一次新的修改：它动过的每一项都恢复成改前的值。如果之后又有修改动过其中某项，撤销会被拒绝，返回 `config.conflict` 并指出那次修改；先撤销那次，或者直接写入想要的值。最近的 10,000 次修改会保留。

### 导入与导出

```bash
docker exec linkos-beacon linkos-beacon config import /srv/ops/beacon -dry-run   # 看看会改什么
docker exec linkos-beacon linkos-beacon config import /srv/ops/beacon -note "ops repository at 4f2c1e0"
docker exec linkos-beacon linkos-beacon config export /srv/ops/beacon
```

目录结构同上：`catalog/*.yaml`、`targets/*.yaml` 和 `targets/modules.yml`。导入把这些文件作为完整的配置，算一次修改：文件里没有的项会被删除；文件通不过校验时什么都不变。导出把目录写成 `catalog/locations.yaml`、`heartbeats.yaml` 和 `resources.yaml`，每个检测目标组写成 `targets/<组名>.yaml`，模块文件原样写出，并删掉这两个目录里其余的 `*.yaml` 文件（配置里没有模块文件时也删掉 `modules.yml`），这样再导入这个目录什么都不会变。来回导入导出内容不丢；目录和检测目标文件里的注释和排版不保留，列表按 id 和名字排序。模块文件按导入时的样子保存，注释也在，检测点拿到的逐字节相同。

### 启动时和收到 SIGHUP 时的文件

数据库里还没有配置时，第一次启动会导入配置里指定的文件（`catalog_dir`、`gateway.targets_dir`、`gateway.modules_file`）并记日志；文件通不过校验就不启动，和以前一样。此后以数据库为准，只在要求时才读文件：

- `SIGHUP` 会导入这些文件，但只在上次导入之后没有经 Beacon 改过时才导入，免得文件悄悄覆盖人和 Agent 做的修改。否则它在日志里说明之后是哪次修改、接下来怎么办：用 `config export` 把那次修改带进文件，或者用 `config import` 覆盖它。把文件放在 git 里、拉取后发 `SIGHUP` 的部署照样能用。
- `linkos-beacon config import DIR` 随时导入一个目录。

`linkos-beacon check-config` 会校验存在的文件和数据库里的配置：服务在运行并在 socket 上应答时问服务，否则直接读数据库文件。

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
    machine: nas-1            # 可选：任务所在的机器，用 SSH 通道里的名字
```

`machine` 写任务跑在哪台机器上，名字和 `linkos-beacon machine list`、`list_machines` 显示的一致。这样，这个心跳的告警的证据和时间线就会点出这台机器，并列出这台机器上的会话和变更，告警之前刚发生的也在内，被 `HeartbeatStale` 唤醒的 Agent 就知道该去哪里看（[agents.md](agents.md#证据)）。通过 API、MCP 修改时，SSH 通道里没有的机器会被拒绝，返回 `config.invalid` 并说明怎么改，除非这个心跳原来就写的是这台机器；导入和回滚则照样接受，因为文件常常在机器登记之前就导入了，同时在这次变更的 `warnings`、日志和 `config import` 的输出里提示；`check-config` 也会列出这样的心跳，但不算失败。

导出为 `linkos_heartbeat_max_age_seconds{heartbeat, severity}`，心跳写了机器时还带 `machine`。规则按 `heartbeat` 和 `severity` 聚合，所以这个标签不会改变任何告警。

## 资源

会到期的东西。只写名字、类别和日期，不写价格。

```yaml
resources:
  - {name: edge-la-1, kind: vps, expires: 2027-04-02}
  - {name: example.com, kind: domain, expires: 2027-01-15, note: 自动续费}
```

`kind` 是 `vps`、`domain`、`cert`、`token` 或 `service`。导出为 `linkos_resource_expiry_timestamp_seconds{resource, kind}`。和检测点同名的 `vps` 同时决定这个检测点的期限（见上）。

## 检测目标

检测目标分成有名字的组，每组是一个列表，格式和 Prometheus 的文件服务发现一样；写成文件时，每组是检测目标目录里的一个文件，文件名就是组名加 `.yaml`。每一项都要有标签 `module`（模块文件里的名字）和 `service`（结果属于哪个服务）；其他标签原样保留，规则和卡片都能用。

```yaml
- targets: [https://example.com/healthz]
  labels: {module: http_head, service: example.com, severity: critical}
```

组名决定哪些检测点去测（API 里显示为 `probed_by`）：

| 组名（文件名） | 谁测 |
| --- | --- |
| `all-…`（`all-*.yaml`） | 所有检测点 |
| `overseas-…`、`mainland-…` | 该地区的检测点 |
| `<检测点 id>-…` | 只有这个检测点（按最长的 id 匹配） |

其他名字会被拒绝。检测点下次拉取（一分钟内）就会拿到修改，不用重载，也不用重新部署检测点。

## 检测模块

下表这些部分的写法和 [Blackbox Exporter 的模块文件](https://github.com/prometheus/blackbox_exporter/blob/master/CONFIGURATION.md) 一样，现有的模块照样能用；另有三种 Beacon 自己的检测方式。结果用 Blackbox Exporter 的指标名（`probe_success`、`probe_duration_seconds`、`probe_http_status_code`、`probe_ssl_earliest_cert_expiry` 等），失败时还会说明原因：`probe_failure_info{reason}`，值是 `dns`、`connect`、`timeout`、`tls`、`not_ssl`、`http_status`、`body`、`banner`、`protocol`、`auth`、`manifest` 或 `config`。

| 检测方式 | 字段 |
| --- | --- |
| `http` | `method`、`valid_status_codes`、`follow_redirects`、`fail_if_not_ssl`、`fail_if_body_not_matches_regexp`、`headers`、`preferred_ip_protocol`、`ip_protocol_fallback` |
| `tcp` | `tls`、`query_response`（`expect`、`send`），以及 IP 相关字段 |
| `registry` | 镜像仓库：`/v2/`，需要时取令牌，再 HEAD 一个 manifest，不下载层。`repository`、`reference`、`username`、`password_file` |
| `git` | Git over HTTP：`info/refs?service=git-upload-pack`。`username`、`password_file` |
| `rdap` | 从 RDAP 查域名到期：`server`（默认 `https://rdap.org`） |

每个模块都有 `timeout`（默认 10s），检测点的 `probe.timeout` 可以把它延长。

`password_file` 是检测点密钥目录里的一个文件名，目录默认是 `/etc/linkos-agent/secrets`，`linkos-probe` 启动时可用 `-secrets-dir` 改：写 `git-password`，不写路径。含路径分隔符或 `..` 的值、绝对路径，在写入或导入模块时就被拒绝，返回 `config.invalid`，这样 `config:write` 不能让检测点读它机器上的任意文件；旧版 Beacon 发来这样的模块时，检测点拒绝每一次要读它的检测（`probe_failure_info{reason="config"}`，记一行日志），什么都不读。检测点为模块读文件从不越出这个目录。本版之前的检测点把这个名字当作相对于工作目录的路径：先升级检测点，再添加带 `password_file` 的模块。

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

在 API 里每个模块是单独的一项（`/api/v1/config/modules/http_head`，JSON）。修改或删除一个模块时，模块文件会按标准排版重写，其他模块和它们的注释保留。

`linkos-probe check -modules <文件>` 校验模块文件，`linkos-probe run -modules <文件> <模块> <目标>` 手动跑一次检测。
