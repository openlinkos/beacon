---
sourceHash: sha256-949c7776
---

# 检测什么，从哪里检测

这一页写给决定 Beacon 监控什么的人，也写给拿着相应令牌的 Agent。它说明哪些机器在检测、检测什么、怎么检测，哪些定时任务必须按时报到，哪些东西快到期了；然后说明怎样通过 Beacon 修改这些内容，又不会覆盖别人刚做的修改。示例以[30 分钟装好](install.md)里的参考部署为准，命令都通过 `docker exec beacon beacon …` 执行。

## 整体关系 {#how-it-fits-together}

- **检测点**是一台运行 `beacon-probe agent`（检测程序）的机器：放在用户附近的小 VPS、边缘节点，或者中心本身（运行 Beacon 的机器）。它每分钟向 Beacon 的网关要一次检测清单，检测完把结果推回去。
- **检测目标**是被检测的对象：一个 URL、一个 `host:port`、镜像仓库里的一个仓库、一个域名。检测目标按**目标集**分组，目标集的名字决定由哪些检测点来测。
- **检测模块**规定怎么检测：发一个 `HEAD` 请求并要求返回 200，做一次 TLS 握手，查镜像仓库的 manifest，查域名到期日。每次失败都带着原因（`dns`、`connect`、`timeout`、`tls`、`http_status` 等）。
- 单个检测点说了不算。按参考规则（`deploy/rules/probes.yml`），一个检测点连续 3 分钟测不通，才算它认为这个服务失败；至少两个检测点失败，并且不少于有数据的检测点的一半，服务才算故障。
- **心跳**是必须按时报告成功的定时任务，比如备份；**到期资源**是会过期的东西，比如域名或租用的机器。这两样都不检测，Beacon 只看它们的时间。

这些内容都存在 Beacon 的数据库里，不在文件里。人和 Agent 通过 API、MCP 和命令行修改。每次修改都先校验，通过后立即生效（不用重载，也不用重新部署检测点），并成为历史里的一个版本：谁改的、什么时候、通过什么、为什么改，都记着，也都能撤销。文件只用于导入和导出，目录结构和以前的部署一样，所以仍然可以放在 git 里。

| 内容 | API 和 MCP 里的名字 | 文件（导入、导出） | 谁来读 |
| --- | --- | --- | --- |
| 目录：检测点、心跳、到期资源 | `locations`、`heartbeats`、`resources` | `catalog/*.yaml`（配置项 `catalog_dir`） | Beacon：导出成指标给规则用，并据此给检测点写入的数据打标签 |
| 检测目标，按目标集分组 | `targets` | `targets/*.yaml`，每个目标集一个文件（`gateway.targets_dir`） | 网关：给每个检测点发它自己的清单 |
| 检测模块 | `modules` | `targets/modules.yml`（`gateway.modules_file`） | 网关：发给所有检测点 |
| 公开状态页 | `status_pages` | `catalog/*.yaml`，写在 `status_pages` 下 | Beacon：提供这些页面（[状态页](status.md)） |

下面各节用 YAML 写出每一项的结构，和文件里一样；API 和 MCP 收发的是同样结构的 JSON。目录可以拆成多个文件，各文件里的列表会合并。

## 检测点 {#locations}

一个检测点就是一台运行 `beacon-probe agent` 的机器，它有自己的令牌，用 `beacon enroll probe <id>` 生成。先把检测点加进目录，再登记：目录里没有的 id 登记时会失败，提示 `location edge-2 is not in the catalogue`。网关给检测点写入的所有数据打上目录里的 `location`、`region`、`network` 和 `host` 标签，检测点自己报的一概不用，所以一个检测点冒充不了另一个。

```yaml
locations:
  - id: edge-1                         # 内部用：指标、卡片、配置；也是机器名
    name: United States - Los Angeles  # 公开名，显示在状态页上：写地点和网络，不写厂商
    region: overseas                   # overseas 或 mainland
    network: CN2 GIA
    address: edge-1.example.net        # CDN 节点：节点地址（域名或 IP）……
    rotation: cdn.example.com          # ……以及所在集群的轮转域名
    standby: false                     # true：备用节点，别的节点故障时才进轮转
    probe:
      timeout: 30s                     # 这里的每次检测至少给这么长时间（最多 1m）
      retries: 1                       # 没有得到应答就失败的检测，立即重试（最多 2 次）
```

| 字段 | 含义 |
| --- | --- |
| `id` | 小写字母、数字和连字符。到期资源里有同名的 `vps` 时，这个检测点只在那个日期之前算「应当上报」（到期当天仍然算）：机器不续租就不再计入，它的令牌也写不进数据；续租（改成新日期）就恢复。 |
| `name` | 公开名，必填。 |
| `region` | `overseas` 或 `mainland`。规则会对比两者：只有中国大陆访问不了，是线路问题，不是服务故障。 |
| `network` | 自由文本，和公开名一起显示。 |
| `address`、`rotation` | 可选，给 CDN 节点用。Beacon 每分钟解析这两个名字，导出 `beacon_location_in_rotation{location, rotation, role}`（节点地址在轮转域名的解析结果里时为 1）、`beacon_rotation_resolve_success`、`beacon_rotation_members`；解析结果里有哪个检测点都对不上的地址时，导出 `beacon_rotation_unknown_address{rotation, address}`。写了轮转就必须写地址。 |
| `standby` | 备用节点只在别的节点故障时进入轮转：`role="standby"`，而不是 `primary`。这样规则可以在主节点掉出轮转时告警，在备用节点顶上时只做通知。必须写轮转。 |
| `probe.timeout` | 给线路较慢的机器：网关把它告诉这台机器上的 agent（`GET /api/v1/agent`），超时比它短的模块在这里都改用它。为 0 时沿用模块自己的超时。 |
| `probe.retries` | 给连接偶尔会断的机器：没有得到应答就失败的检测（`dns`、`connect`、`timeout`）立即重试，最多这么多次（0 到 2）。得到了应答的失败，比如状态码不对，从不重试。`beacon_probe_attempts` 记下每个结果试了几次，线路不稳一样看得出来。连接卡住时，加长超时没有用，换个新连接重试才有用。 |

检测点按地点和网络命名，不写厂商：这个名字会显示在公开状态页上。Beacon 为每个当前应当上报的检测点导出 `beacon_expected_location{location, region, network}`，规则据此发现不再上报的检测点：参考规则 `ProbeLocationDown` 在一个检测点 10 分钟没有结果之后，再过 5 分钟触发。

## 检测目标 {#targets}

目标集是一组列表，格式和 Prometheus 的文件服务发现相同：一组检测目标，加上它们共用的标签。写成文件时，每个目标集是检测目标目录下的一个文件，文件名是目标集名加 `.yaml`。每组至少有一个检测目标，并且必须有 `module`（模块文件里的名字）和 `service`（结果属于哪个服务；告警、卡片和状态页都按服务来说）两个标签。其他标签原样保留，规则和卡片都能用到：`severity`（`critical`、`warning` 或 `info`）就是告警的级别；`failure_domain` 标明哪些东西会一起坏（同一个源站、同一个集群），这样相关的告警会并排显示。

```yaml
- targets: [https://example.com/healthz]
  labels: {module: http_head, service: example.com, severity: critical}
```

目标集的名字决定由哪些检测点来测（API 里显示为 `probed_by`）：

| 目标集名（文件名） | 由谁检测 |
| --- | --- |
| `all-…`（`all-*.yaml`） | 所有检测点 |
| `overseas-…`、`mainland-…` | 该地区的检测点 |
| `<检测点 id>-…` | 只有这个检测点（多个 id 都匹配时取最长的） |

其他名字一律拒绝，免得名字打错导致某项检测悄悄停掉。检测点下次拉取时就会拿到修改，最多一分钟；不用重载，也不用重新部署检测点。

## 检测模块 {#modules}

模块文件下表列出的部分和 [Blackbox Exporter 的模块文件](https://github.com/prometheus/blackbox_exporter/blob/master/CONFIGURATION.md)格式相同，现有的模块可以直接用；另外还有三种 Beacon 自己的检测方式。检测结果沿用 Blackbox Exporter 的指标名（`probe_success`、`probe_duration_seconds`、`probe_http_status_code`、`probe_ssl_earliest_cert_expiry` 等）。

| 检测方式 | 字段 |
| --- | --- |
| `http` | `method`、`valid_status_codes`、`follow_redirects`、`fail_if_not_ssl`、`fail_if_body_not_matches_regexp`、`headers`、`preferred_ip_protocol`、`ip_protocol_fallback` |
| `tcp` | `tls`、`query_response`（`expect`、`send`），以及 IP 相关字段 |
| `registry` | 镜像仓库：先访问 `/v2/`，需要时取令牌，再对一个 manifest 发 HEAD，不拉取镜像层。`repository`、`reference`、`username`、`password_file` |
| `git` | 基于 HTTP 的 Git：`info/refs?service=git-upload-pack`。`username`、`password_file` |
| `rdap` | 通过 RDAP 查域名到期日：`server`（默认 `https://rdap.org`） |

每个模块都有 `timeout`（默认 10s），检测点的 `probe.timeout` 可以把它加长。参考部署自带五个模块：`http_head`、`http_get_200`、`http_get_healthy`、`tcp_tls` 和 `rdap_domain`。

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

检测失败时，`probe_failure_info{reason}` 给出原因。Agent 看证据时，最先读的就是它：

| 原因 | 发生了什么 |
| --- | --- |
| `dns` | 域名解析不出来 |
| `connect` | 连接被拒绝或被重置 |
| `timeout` | 规定时间内没有应答 |
| `tls` | TLS 握手或证书出错 |
| `not_ssl` | 要求 HTTPS，对方却是明文 HTTP |
| `http_status` | 状态码不在预期之内 |
| `body` | 响应内容不匹配 |
| `banner` | TCP 服务没有回出预期的内容 |
| `protocol` | 应答类型不对（内容类型、JSON） |
| `auth` | 拿不到镜像仓库的令牌 |
| `manifest` | 镜像仓库里没有这个 manifest |
| `config` | 模块或检测目标本身写错了 |

`password_file` 是检测点密钥目录里的一个文件名，这个目录默认是 `/etc/beacon-probe/secrets`，启动 `beacon-probe` 时可以用 `-secrets-dir` 改：写 `git-password` 这样的文件名，不写路径。值里带路径分隔符或 `..`，或者是绝对路径，写入或导入模块时就会被拒绝，返回 `config.invalid`，这样拿着 `config:write` 的人没法让检测点读它机器上的任意文件；旧版 Beacon 发来这样的模块时，检测点会拒绝每一次需要读它的检测（`probe_failure_info{reason="config"}`，记一行日志），什么都不读。检测点为模块读文件，从不越出这个目录。这一版之前的检测点会把这个名字当作相对于工作目录的路径：先升级检测点，再加带 `password_file` 的模块。

在 API 里，每个模块单独算一项（`/api/v1/config/modules/http_head`，JSON 格式）。修改或删除一个模块时，模块文件会按标准格式重写，其他模块和它们的注释都保留。

想在发给所有检测点之前先试一个模块，在任何装了 `beacon-probe` 的机器上：`beacon-probe check -modules <文件>` 校验模块文件，`beacon-probe run -modules <文件> <模块> <目标>` 手动跑一次检测。

## 心跳 {#heartbeats}

心跳是至少每隔 `max_age` 必须报告一次成功的任务：备份、定时任务。报得太晚，`HeartbeatStale` 触发；这里列出的心跳从来没报过，`HeartbeatMissing` 触发。装了检测程序的机器上，任务往检测程序的 textfile 目录写一行（`/var/lib/beacon-probe/textfile/heartbeat_<名字>.prom`；参考部署里的 `beacon-heartbeat` 脚本做的就是这件事，在任务末尾加一句 `beacon-heartbeat <名字>` 即可）；其他机器上的任务用 `beacon enroll heartbeat <名字>` 生成的令牌推送一条数据：

```bash
curl -fsS -H "Authorization: Bearer $TOKEN" --data-binary \
  "beacon_heartbeat_last_success_timestamp_seconds{heartbeat=\"nas-backup\"} $(date +%s)" \
  https://metrics.example.com/api/v1/import/prometheus
```

```yaml
heartbeats:
  - name: nas-backup          # 小写字母、数字和连字符
    max_age: 26h              # 至少 1m
    severity: warning         # critical、warning 或 info
    description: Nightly backup of the NAS
    machine: nas-1            # 可选：任务所在的机器，用 SSH 通道里的名字
```

只要任务跑在 [SSH 通道](ssh.md)能到达的机器上，就写上 `machine`。名字和 `beacon machine list`、`list_machines` 显示的一致。有了它，心跳超时才成为 Agent 能处理的事：这个心跳的告警，证据和时间线里会写明是哪台机器，并列出这台机器上的会话和变更，告警之前刚发生的也在内。被 `HeartbeatStale` 叫醒的 Agent 因此知道该去哪里看，比如能看到三分钟前停掉这个任务定时器的那次会话（[证据](agents.md#the-evidence)）。

通过 API、MCP 写入时，SSH 通道里没有的机器会被拒绝，返回 `config.invalid`，并说明怎么改；这个心跳原来就写着这台机器的除外。导入和撤销则照样接受，因为文件常常在机器登记之前就导入了；这时会在这次修改的 `warnings`、日志和 `config import` 的输出里提醒。`check-config` 也会列出这样的心跳，但不算校验失败。

导出为 `beacon_heartbeat_max_age_seconds{heartbeat, severity}`，心跳写了机器时还带 `machine` 标签。规则按 `heartbeat` 和 `severity` 聚合，所以这个标签不影响任何告警。

## 到期资源 {#resources}

会过期的东西。只写名字、类别和日期，不写价格。

```yaml
resources:
  - {name: edge-1, kind: vps, expires: 2027-04-02}
  - {name: example.com, kind: domain, expires: 2027-01-15, note: renews automatically}
```

`kind` 是 `vps`、`domain`、`cert`、`token` 或 `service`；`expires` 是 `2027-04-02` 这样的日期。导出为 `beacon_resource_expiry_timestamp_seconds{resource, kind}`，由 `ResourceExpiring` 盯着：提前 30 天发警告，提前 7 天变成严重。和检测点同名的 `vps` 同时决定这个检测点的期限（见[检测点](#locations)）。

## 修改 {#changing-it}

修改需要 `config:write` 作用域，读取需要 `status:read` 或 `config:write`。`config:write` 决定每个检测点测什么、怎么测，包括模块把哪个文件当作密码发出去（`password_file`）。检测点只按文件名从它的密钥目录读这类文件，所以拿着 `config:write` 的人只能让检测点发出别人放在那里的东西，别的拿不到。只把它给你愿意让他改这些文件的人。

不管通过什么途径，每次修改都按文件一贯的规则校验。通不过的修改会被拒绝，返回 `config.invalid` 和原因，什么都不改：还有目标集只以某个检测点命名时，这个检测点删不掉；目标集名必须以已知的前缀开头。每次修改都记入审计日志，并回显到告警群，带上备注（如果写了）。

### 通过 API 和 MCP {#through-the-api-and-mcp}

每一项都按它在文件里的结构读写：

```bash
curl -H "Authorization: Bearer $TOKEN" https://status.example.com/api/v1/config            # 全部配置，带版本号
curl -H "Authorization: Bearer $TOKEN" https://status.example.com/api/v1/config/targets    # 各目标集，以及每个由谁检测
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "https://status.example.com/api/v1/config/locations/edge-2?note=new+node" \
  -d '{"name": "Germany - Frankfurt", "region": "overseas", "network": "BGP", "probe": {"timeout": "30s"}}'
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/config/targets/all-web \
  -d '[{"targets": ["https://example.com/healthz"], "labels": {"module": "http_head", "service": "example.com"}}]'
curl -X DELETE -H "Authorization: Bearer $TOKEN" https://status.example.com/api/v1/config/heartbeats/nas-backup
```

路径是 `/api/v1/config/{locations,heartbeats,resources,targets,modules,status_pages}`（列表）和 `…/{id 或名字}`（对单项 `GET`、`PUT`、`DELETE`），列在 [API 参考](reference/api.md)里，完整定义见 `internal/api/openapi.yaml`。MCP 工具是 `get_config`、`put_config` 和 `delete_config`，参数是 `kind`（`locations` 等）和 `key`。每次修改返回新的版本号，以及每一项改前、改后的值；写入和现有内容相同的值不算修改。

修改可以注明它基于哪个版本：`PUT` 和 `DELETE` 加 `?if_version=12`，`put_config` 和 `delete_config` 传 `if_version`，值就是 `GET /api/v1/config`（`get_config`）返回的版本号。如果生效的已经是更新的版本，修改会被拒绝，返回 `config.conflict`（`the configuration changed: version 13 is in force, not 12 as you last saw; read it again and retry`），什么都不改：重新读一遍，再做决定。Agent 先读配置、想好、再写入时，应当带上它，这样永远不会覆盖别人在这期间做的修改。

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "https://status.example.com/api/v1/config/heartbeats/nas-backup?if_version=12&note=longer+window" \
  -d '{"max_age": "30h", "severity": "warning", "description": "Nightly backup of the NAS"}'
```

### 历史与撤销 {#history-and-revert}

`GET /api/v1/config/history`（MCP 是 `list_config_changes`，命令行是 `beacon config history`）按从新到旧列出修改：版本号、时间、谁、通过什么（`api`、`mcp`、`cli`、`console`；服务自己导入文件时是 `start` 和 `signal`）、做了什么（`put`、`delete`、`import`、`revert`）、新增、修改或删除了哪些项，以及备注。用 `kind` 和 `key` 可以只看某一项。`GET /api/v1/config/history/{version}`（`get_config_change`）给出每一项改前和改后的值。命令行每行一次修改，列名是 `VERSION`、`TIME`、`WHO`、`VIA`、`ACTION`、`ITEMS` 和 `NOTE`；新部署的第一行是版本 1，由 `beacon` 通过 `start` 做的一次 `import`。

`POST /api/v1/config/history/{version}/revert`（`revert_config_change`，`beacon config revert VERSION`）把一次修改撤销掉，撤销本身算一次新的修改：那次修改动过的每一项都恢复成改前的值。如果之后另一次修改又动过其中某一项，撤销会被拒绝，返回 `config.conflict` 并指出是哪次修改；先撤销那一次，或者直接写入你想要的值。最近 10,000 次修改都保留。

### 导入与导出 {#import-and-export}

导入把一个目录里的文件当作完整的配置，算作一次修改。这正是它的用处，也是它的坑：**文件里没有的项会被删除**，包括通过 API 加进来的检测点和目标集。每次都先用 `-dry-run` 看一眼。目录结构和上面的表一致：`catalog/*.yaml`、`targets/*.yaml` 和 `targets/modules.yml`。路径由命令自己读取，所以用 `docker exec` 时，它是容器里的路径。

```bash
docker exec beacon beacon config import /etc/beacon -dry-run
docker exec beacon beacon config import /etc/beacon -note "ops repository at 4f2c1e0"
```

试运行只说会改什么，什么都不改；随后的导入才真正修改，并给出版本号。每行一项，`+` 是新增，`~` 是修改，`-` 是删除：

```
would change 2 items (nothing changed):
  + locations edge-2
  ~ targets all-web
version 2: import of 2 items
  + locations edge-2
  ~ targets all-web
```

文件内容和当前生效的配置一样时，输出 `nothing to change: version 2 holds this already`。文件通不过校验时什么都不改，错误信息会说明原因。

导出把当前生效的配置写进一个目录：目录写成 `catalog/locations.yaml`、`heartbeats.yaml`、`resources.yaml` 和 `status_pages.yaml`（有内容的才写），每个目标集写成 `targets/<目标集>.yaml`，模块文件按保存的原样写出。它会删掉这两个目录里其余的 `*.yaml` 文件（配置里没有模块文件时，也删掉 `modules.yml`），这样再导入这个目录，什么都不会变；输出 `exported version 2 to DIR: 5 files`，删了文件的话每个再列一行。来回导入导出内容不丢；目录文件和目标集文件里的注释、排版不保留，列表按 id 和名字排序。模块文件按导入时的样子保存，注释都在，检测点拿到的逐字节相同。

### 参考部署里的文件 {#files-in-the-reference-deployment}

`deploy/compose.yaml` 把部署目录里的 `catalog/` 和 `targets/` 以只读方式挂进容器，路径是 `/etc/beacon/catalog` 和 `/etc/beacon/targets`。因此：

- **想通过文件修改**：在主机上改 `/opt/beacon/catalog/` 和 `/opt/beacon/targets/` 下的文件，运行 `docker exec beacon beacon config import /etc/beacon -dry-run`，确认后去掉 `-dry-run` 再运行一次。
- **想把通过 Beacon 做的修改带回文件**：导出到容器能写的目录，也就是数据卷，再复制出来。替换之前先对比一下，因为 `docker cp` 只会新增和覆盖，不会删除：

```bash
docker exec beacon beacon config export /var/lib/beacon/export
docker cp beacon:/var/lib/beacon/export ./export && diff -r ./export/catalog /opt/beacon/catalog
```

两种做法选一种，并坚持下去：要么以文件为准，每次修改都是一次导入；要么以 Beacon 为准，文件只是导出的结果。两种混着用，导入就会删掉别人通过 API 加的东西。

### 启动时和收到 SIGHUP 时的文件 {#the-files-at-start-and-on-sighup}

数据库里还没有配置时，第一次启动会导入配置里指定的文件（`catalog_dir`、`gateway.targets_dir`、`gateway.modules_file`），并记一行日志 `imported the configuration files into the database, where it is changed from now on`；文件通不过校验，服务就不启动。此后以数据库为准，只在被要求时才读文件：

- 收到 `SIGHUP` 时导入这些文件，但前提是上次导入之后没有通过 Beacon 改过配置，免得文件悄悄覆盖人和 Agent 做的修改。否则它在日志里说明之后来了哪次修改，以及怎么继续：用 `config export` 把那次修改带进文件，或者用 `config import` 覆盖它。把文件放在 git 里、拉取后发 `SIGHUP` 的部署照常可用。
- `beacon config import DIR` 随时可以导入一个目录。

`beacon check-config` 会校验存在的文件，以及数据库里的配置：服务正在运行、并在 socket 上应答时，通过服务来查，否则直接读数据库文件。输出的最后两行分别说明文件和数据库里有什么，比如 `files: 2 probe locations, 0 heartbeats, 0 resources, 3 probe targets in 2 sets, 5 modules, 1 status pages` 和 `database: version 2 in the running service: …`，有警告的话再逐行列出。
