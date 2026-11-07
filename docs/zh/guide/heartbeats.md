---
sourceHash: sha256-b78ea1fc
---

# 心跳与到期资源

读完这一页，你的一个定时任务（备份、清理）必须按时报告成功，否则告警触发。会到期的东西（域名、租用的机器）在过期之前提醒你。

这两样都不做检测：Beacon 盯着它们的日期。这一页接在[检测一个服务](monitoring.md)后面，那一页讲目标和模块。

**你需要：**

- Beacon 已经运行。
- 一个要盯的任务。
- 一个有 `config:write` 的凭据：控制台的**配置**页面，或一个令牌。

## 建一个心跳 {#make-a-heartbeat}

1. **把任务列进目录。** `max_age` 是它必须多久成功一次，至少一分钟。`severity` 是告警的级别：

   ```bash
   curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
     https://status.example.com/api/v1/config/heartbeats/nas-backup \
     -d '{"max_age": "26h", "severity": "warning", "description": "Nightly backup of the NAS"}'
   ```

   你会看到新的版本号和这一项。任务报得太晚，`HeartbeatStale` 触发。列了心跳却从来没有报过，过了一天 `HeartbeatMissing` 触发。

2. **让任务报告。** 在装有检测程序的机器上，在任务末尾调用参考部署安装的 `beacon-heartbeat` 脚本。它往检测程序读取的目录里写一条数据：

   ```bash
   restic backup … && beacon-heartbeat nas-backup
   ```

   其他机器上的任务用专属令牌推送一条数据。令牌在中心上用 `beacon enroll heartbeat nas-backup` 生成：

   ```bash
   curl -fsS -H "Authorization: Bearer $TOKEN" --data-binary \
     "beacon_heartbeat_last_success_timestamp_seconds{heartbeat=\"nas-backup\"} $(date +%s)" \
     https://metrics.example.com/api/v1/import/prometheus
   ```

3. **写明它所在的机器**，只要任务跑在 [SSH 通道](machines.md)能到达的机器上：

   ```bash
   curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
     "https://status.example.com/api/v1/config/heartbeats/nas-backup?note=names+the+machine" \
     -d '{"max_age": "26h", "severity": "warning", "machine": "nas-1", "description": "Nightly backup of the NAS"}'
   ```

   有了它，心跳超时才成为 Agent 能处理的事。`HeartbeatStale` 的[证据](on-call.md#the-evidence)会写明是哪台机器，并列出这台机器上的会话和变更，告警之前刚发生的也在内，比如停掉任务定时器的那次会话。

   通过 API 写入时，通道里没有的机器会被拒绝，返回 `config.invalid`。导入和撤销则照样接受，只是带一条警告，因为文件常常在机器登记之前就导入了。

## 盯住会到期的东西 {#watch-things-that-expire}

只写名字、类别和日期，不写价格。`ResourceExpiring` 在日期前 30 天发警告，前 7 天变成严重：

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/config/resources/example.com \
  -d '{"kind": "domain", "expires": "2027-01-15", "note": "renews automatically"}'
```

`kind` 是 `vps`、`domain`、`cert`、`token` 或 `service`。

和检测点同名的 `vps` 同时决定这个检测点的期限。过了日期，它就不再算「应当上报」，令牌也写不进数据。改成新日期就恢复。

续租，或有意放弃。然后把新日期写进来，告警就结束。

## 检查一下 {#check-it}

任务跑过一次之后，查它发来的数据。用中心的 VictoriaMetrics，地址是 `http://127.0.0.1:8428/vmui`：

```
beacon_heartbeat_last_success_timestamp_seconds{heartbeat="nas-backup"}
```

你会看到一条时间戳新鲜的数据。

不想等一晚上就看到告警那头：

1. **配一个 `max_age: 1m` 的心跳**，给一个每几分钟跑一次的任务。
2. **停掉任务。** 几分钟内 `HeartbeatStale` 就会触发，证据里带着那台机器。

不工作时：

- **心跳从来没报过。** 任务没有以 `beacon-heartbeat` 结尾，或者推送用错了令牌。
- **修改被 `config.invalid` 拒绝。** 通常是通道里没有那台机器：[修改被 config.conflict 或 config.invalid 拒绝](../self-hosting/troubleshooting.md#a-change-is-refused-with-configconflict-or-configinvalid)。
- **告警压根没来。** 见[告警一直没有来](../self-hosting/troubleshooting.md#an-alert-never-arrives)。

## 下一步 {#next}

- [让 Agent 值班](on-call.md)：心跳超时叫醒的 Agent，已经知道该查哪台机器。
- [检测一个服务](monitoring.md)：目标和模块，从用户所在的地方检测。
