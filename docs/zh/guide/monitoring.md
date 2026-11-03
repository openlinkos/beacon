---
sourceHash: sha256-144e34bf
---

# 检测一个服务

读完这一页，你的一个服务会由你运行的每个检测点来检测，每次失败都带着原因，而 Beacon 监控的内容以版本的形式存在数据库里：每次修改先校验再立即生效（不用重新部署检测点），也都能撤销。必须按时报到的任务和会到期的东西在下一页：[心跳与到期资源](heartbeats.md)。

你需要：一个至少有一个检测点在上报的中心（添加检测点见[检测点](../self-hosting/probes.md)）；一个有 `config:write` 的凭据：控制台的**配置**页面，或一个令牌。读取需要 `status:read`。参考部署里命令都通过 `docker exec beacon beacon …` 执行。

## 添加检测点 {#locations}

一个检测点就是一台运行 `beacon-probe agent` 的机器。先把它加进目录（目录里没有的 id 登记时会失败），再在那台机器上安装检测程序：

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "https://status.example.com/api/v1/config/locations/edge-2?note=new+node" \
  -d '{"name": "Germany - Frankfurt", "region": "overseas", "network": "BGP", "probe": {"timeout": "30s"}}'
```

你会看到新的版本号和这一项，修改也会回显到告警群。网关给检测点写入的所有数据打上目录里的 `location`、`region`、`network` 和 `host` 标签，检测点自己报的一概不用。`region`（`overseas` 或 `mainland`）让规则分得清线路问题和故障；`name` 是公开的，显示在[状态页](status-pages.md)上，所以按地点和网络命名，不写厂商。条目的每个字段见目录参考：[目录](../reference/catalogue.md)。

## 添加目标集 {#targets}

检测目标是被动检测的对象（一个 URL、一个 `host:port`、镜像仓库里的一个仓库、一个域名）；目标按目标集分组，目标集的名字决定由哪些检测点来测。一个目标集是一组列表，格式和 Prometheus 的文件服务发现相同：一组目标，加上它们共用的标签：

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/config/targets/all-web \
  -d '[{"targets": ["https://example.com/healthz"], "labels": {"module": "http_head", "service": "example.com"}}]'
```

- `module` 说明用哪个模块；`service` 说明结果属于哪个服务（告警、卡片和状态页都按服务来说）。两者必填。
- `severity`（`critical`、`warning` 或 `info`）就是告警的级别；`failure_domain` 标明哪些东西会一起坏，相关的告警会在[证据](on-call.md#the-evidence)里并排显示。
- 名字决定谁来测：`all-…` 是所有检测点，`overseas-…` 和 `mainland-…` 是该地区，`<检测点 id>-…` 只有这个检测点。其他名字一律拒绝，名字打错不会让某项检测悄悄停掉。

检测点下次拉取时就会拿到修改，最多一分钟。`GET /api/v1/config/targets` 列出每个目标集和检测它的检测点（`probed_by`）。

## 手动测一次目标 {#check-a-target-by-hand}

检测失败时，`probe_failure_info{reason}` 给出原因：`dns`、`connect`、`timeout`、`tls`、`not_ssl`、`http_status`、`body` 等。想把一个模块先试一遍再发给所有检测点，在任何装了 `beacon-probe` 的机器上：

```bash
beacon-probe run -modules modules.yml http_head https://example.com/healthz
```

你会看到这次结果的指标，失败时是 `failed: <原因>: <细节>`。只有某一个检测点总是测不通时，[目标总是失败](../self-hosting/troubleshooting.md#a-target-always-fails)逐条讲了原因。

## 模块 {#modules}

模块规定怎么检测：参考部署自带 `http_head`、`http_get_200`、`http_get_healthy`、`tcp_tls` 和 `rdap_domain` 五个。文件格式在 `http` 和 `tcp` 上与 [Blackbox Exporter](https://github.com/prometheus/blackbox_exporter/blob/master/CONFIGURATION.md) 相同，另有 Beacon 自己的 `registry`、`git` 和 `rdap` 检测方式；每个模块都有 `timeout`（默认 10 秒），检测点的 `probe.timeout` 可以把它加长。`password_file` 只能写检测点密钥目录（默认 `/etc/beacon-probe/secrets`）里的文件名——值里带路径就会被拒绝，返回 `config.invalid`，所以拿着 `config:write` 的人没法让检测点读它机器上的任意文件。模块同样走 `/api/v1/config/modules/{name}`；每种检测方式的每个字段见目录参考：[目录](../reference/catalogue.md)。

## 改动不踩到别人 {#change-it-without-stepping-on-anyone}

不管通过什么途径，每次修改都按文件一贯的规则校验；通不过的返回 `config.invalid`，什么都不改。修改需要 `config:write`；只把它给你愿意让他改这些文件的人（[作用域](../reference/api.md#scopes)）。

1. **写明你基于哪个版本。** 先读、想好、再写入的 Agent 应当带上 `if_version`，这样永远不会覆盖别人在这期间做的修改：

   ```bash
   curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
     "https://status.example.com/api/v1/config/targets/all-web?if_version=12" \
     -d '[{"targets": ["https://example.com/healthz", "https://www.example.com/"], "labels": {"module": "http_head", "service": "example.com"}}]'
   ```

   生效的已是更新的版本时，返回 `config.conflict`；重新读一遍，再做决定。

2. **看历史，撤销修改。** 每次修改都是一个版本，记着谁、通过什么和备注；最近 10,000 次都保留：

   ```bash
   docker exec beacon beacon config history
   docker exec beacon beacon config revert 12
   ```

   之后另一次修改又动过其中某一项时，撤销返回 `config.conflict`；先撤销那一次。

## 文件的导入与导出 {#files-import-and-export}

目录、目标集和模块文件也可以是文件（`catalog/*.yaml`、`targets/*.yaml`、`targets/modules.yml`），用于导入和导出。导入把文件当作完整的配置：**文件里没有的项会被删除**，所以每次都先看一眼：

```bash
docker exec beacon beacon config import /etc/beacon -dry-run
docker exec beacon beacon config import /etc/beacon -note "ops repository at 4f2c1e0"
```

`config export DIR` 把当前生效的配置写回一个目录。两种做法选一种并坚持下去：要么以文件为准，每次修改都是一次导入；要么以 Beacon 为准，文件只是导出的结果。数据库为空时 Beacon 第一次启动会导入一次文件；此后收到 `SIGHUP`，只在上次导入之后没有通过 Beacon 改过配置时才导入（[配置](../self-hosting/configuration.md)）。

## 心跳 {#heartbeats}

必须按时报到的任务和会到期的东西，靠日期而不是检测来盯：见[心跳与到期资源](heartbeats.md)。

## 检查一下 {#check-it}

目标集写入一分钟后，在中心的 VictoriaMetrics（`http://127.0.0.1:8428/vmui`）里查检测点发来的数据：

```
probe_success{service="example.com"}
```

每个检测点一行，服务正常应答时都是 1。按参考规则，至少两个检测点失败、并且不少于有数据的检测点的一半，持续 3 分钟，服务才算故障——单个检测点的检查不会惊动任何人（[Beacon 如何工作](concepts.md#an-alerts-path)）。

不工作时：[检测点没有数据](../self-hosting/troubleshooting.md#a-probe-location-shows-no-data)、[目标总是失败](../self-hosting/troubleshooting.md#a-target-always-fails)、[导入或 SIGHUP 没有改变配置](../self-hosting/troubleshooting.md#an-import-or-sighup-did-not-change-the-configuration)、[修改被 config.conflict 或 config.invalid 拒绝](../self-hosting/troubleshooting.md#a-change-is-refused-with-config-conflict-or-config-invalid)。

## 下一步 {#next}

- [心跳与到期资源](heartbeats.md)：Beacon 按日期盯着的任务和到期日。
- [发布状态页](status-pages.md)：检测点的共识，公之于众。
- [让 Agent 值班](on-call.md)：由此产生的告警叫醒谁。
