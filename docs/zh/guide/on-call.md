---
sourceHash: sha256-a243eced
---

# 让 Agent 值班

配好钩子之后，服务一出问题，就会叫醒你指定的 Agent。Agent 接着会：

- 一次调用读到全部证据；
- 通过 SSH 通道登上机器处理；
- 修完让每个检测点复测；
- 留下一条备注，人在卡片和控制台里都能看到。

Beacon 不调用任何模型，也不要求任何人审批。Agent 做什么由运行它的 harness 决定，而它做过的一切都有记录。

你需要：

- Beacon 已经运行（[装在服务器上](../self-hosting/install.md)的第 1 到 4 步）。
- 一个已通过 MCP 接入的 Agent：[Claude Code](../integrations/claude-code.md)、[Codex](../integrations/codex.md)或[用令牌的 Agent](../integrations/headless.md)。
- 一个钩子够得着的地址：一台运行 `beacon-oncall` 的服务器、你自己写的接收端，或一个 GitHub workflow。

## 先手动走一遍 {#try-the-loop-by-hand}

接钩子之前，先由你代替钩子，把同一个循环走一遍：

- [演练一次故障](drills.md)说明怎样开始一次演练，以及 `ServiceDown` 什么时候触发。
- [你替钩子走一遍循环](drills.md#walk-the-loop)给出要问 Agent 的话，以及它接着会做什么。

被叫醒的 Agent 也是这样工作的。钩子只是代替了那个开口问的人。

## 叫醒 Agent {#wake-an-agent}

1. **在配置里列出钩子。** 把钩子添加到 `config.yaml` 的 `agents` 中。保留部署已有的 `public_url`，让钩子能找到告警证据。

   ```yaml
   agents:
     - name: claude-oncall
       kind: webhook
       url: https://agents.example.com/hooks/beacon
       secret_file: /etc/beacon/secrets/agent-hook-secret   # 至少 24 个随机字符
       service: '^drill$'
       resolved: true                            # 告警恢复时也通知
   ```

   `severities` 说明哪些级别的告警组会叫醒它（默认 `[critical]`），可选的 `service` 正则可以再收窄。`kind: github` 的钩子改为触发一个 workflow，配置块和各字段的含义见 [GitHub Actions](../integrations/github-actions.md)。

2. **校验配置并重启。** 使用参考部署时，在 `/opt/beacon` 下运行 `sudo ./install.sh`，设置新增密钥文件的权限、校验并重启。其他部署请用该部署的配置文件运行 `beacon check-config`，然后重启服务。

   以下情况 `beacon check-config` 会拒绝：

   - 配了钩子却没有 `public_url`；
   - webhook 既没有 `secret_file` 也没有 `headers_file`（给有自己密钥的接收端用）；
   - 密钥不足 24 个字符。

3. **把另一头立起来。** webhook 需要有程序来运行 Agent。`beacon-oncall` 是参考 harness：它校验签名，然后启动你指定的程序，载荷从标准输入传入（[无头 Agent 与 CI](../integrations/headless.md)）。请求头和载荷见[被 webhook 唤醒的 Agent](../integrations/webhooks.md)。

4. **先检查渠道，再演练。** 如果 harness 在 Linux 中心上运行，按[中心上的私有接收端](#a-private-receiver-on-the-centre)设置，再[检查通知和唤醒渠道](#check-the-routes-before-the-drill)。开始演练后，在控制台打开该告警，在时间线查找钩子的受理记录：

   ```
   告警已于 10:02 送达钩子 claude-oncall
   ```

   每次调用的结果同样记在审计日志（`wake_agent`）、告警群和控制台的 **Hook** 页面里。

   送达表示钩子返回了成功的 HTTP 响应。例如，参考接收端可以在告警入队后立即返回 202。配置[程序执行回报](../integrations/headless.md#report-programme-execution)后，可以在告警页查看程序何时启动、如何退出。用复测结果和告警规则的状态验证恢复。

### 中心上的私有接收端 {#a-private-receiver-on-the-centre}

在 Linux 参考部署中，`beacon` 容器接入 Docker 网桥。首次演练时，如果 `beacon-oncall` 与中心同机运行，使用网桥网关作为私有地址。默认配置的公开域名 `status.example.com` 对 `/hooks/beacon` 返回 HTTP 404。

在 `/opt/beacon` 下找到网关地址和对应的网卡。为此地址的 TCP 17007 端口设置入站规则，丢弃来自网桥以外的流量。下面使用 `iptables`。如果你已有防火墙管理器，请将等价规则放在宽泛的放行规则之前。

```sh
beacon_gateway=$(docker network inspect beacon --format '{{(index .IPAM.Config 0).Gateway}}')
beacon_bridge=$(ip -o -4 address show | awk -v gateway="$beacon_gateway" '{split($4, address, "/"); if (address[1] == gateway) print $2}')
test -n "$beacon_bridge" && {
  sudo iptables -C INPUT ! -i "$beacon_bridge" -d "$beacon_gateway/32" -p tcp --dport 17007 -j DROP ||
    sudo iptables -I INPUT 1 ! -i "$beacon_bridge" -d "$beacon_gateway/32" -p tcp --dport 17007 -j DROP
} && printf 'http://%s:17007/beacon\n' "$beacon_gateway"
```

将上面打印出的 URL 填入钩子的 `url`。首次测试时设置 `service: '^drill$'`，运行 `sudo ./install.sh` 应用中心配置。harness 监听在此地址期间，请保留防火墙规则。

[装好 harness、包装脚本和 Agent 账户](../integrations/headless.md#as-a-service)后，首次前台演练时让 systemd 服务保持停止。在另一个终端，将 `beacon_gateway` 设为前面找到的地址，再运行以下命令。示例使用 Claude Code 包装脚本，两端使用同一份签名密钥。

```sh
sudo -u beacon-oncall -H env \
  PATH=/var/lib/beacon-oncall/.local/bin:/usr/local/bin:/usr/bin:/bin \
  BEACON_TOKEN_FILE=/etc/beacon-oncall/beacon-token \
  /usr/local/bin/beacon-oncall -listen "$beacon_gateway:17007" \
  -secret-file /etc/beacon-oncall/hook-secret \
  -run /usr/local/lib/beacon-oncall/claude-code.sh \
  -log-dir /var/lib/beacon-oncall/logs
```

回到 `/opt/beacon`，从 Beacon 所在的 Docker 网络检查接收端，应返回 `ok`。

```sh
docker compose exec -T caddy wget -q -O - "http://$beacon_gateway:17007/healthz"
```

同机的通知接收端也可以绑定这个网桥地址，并为自己的端口设置入站规则。有健康检查路径时，就用它验证可达性。接收端在另一台机器上时，使用两台机器所在的私有网络，或使用 Beacon 能访问的 HTTPS 端点。

### 演练前检查渠道 {#check-the-routes-before-the-drill}

- **配置。** `beacon check-config` 检查设置和密钥文件。**概览 → 严重告警通知**显示所选渠道。这两项检查通过后，接收端可能还没收到 Beacon 的请求。
- **网络可达性。** 从中心所在的网络检查 harness 和接收端提供的健康检查路径。如果探测失败，先修正监听地址或防火墙规则，再启动演练。
- **送达。** 演练时同时核对时间线和接收端。签名接收端返回 HTTP 401 时，检查双方共享的密钥和请求签名。`beacon-oncall` 返回 HTTP 202 表示唤醒请求已被接收端受理，另外核对程序执行结果。验收聊天和手机通知时，查看目标测试群或设备是否实际显示了通知。

### 钩子什么时候被调用 {#when-a-hook-is-called}

- **每个告警组一次**：新告警组第一次发出通知时。告警持续期间不再调用。
- **恢复时再一次**：设了 `resolved` 的话，告警组恢复 `resolved_after`（默认一分钟）之后。
- **失败后重试**：调用没有应答、超时、返回 429 或 5xx 时，分别在 5 秒、20 秒、1 分钟、3 分钟后重试。

告警本身的送达从不等钩子。

## 接手告警 {#take-over-an-alert}

告警页的处置区显示钩子和通知的提交结果。即使 Agent 无法启动，或还没有留下可供查看的操作记录，钩子也可能已经接收告警。在值班人认领之前，人工升级通知按配置继续。已识别身份的 Agent 认领后，升级通知仍会继续。明确设置的静默仍会暂停通知。

值班人可以打开告警页，点击**认领**。上一班值班人或 Agent 已认领时，点击**接手**。用简短备注交代下一步。时间线最多保留最近 20 次历史认领及其备注，审计日志记录每次认领。通过复测结果和告警状态确认恢复。

正式启用通知渠道前，用演练[验证手机实际送达和两位值班人的交接](drills.md#verify-phone-delivery-and-handover)。

## 证据 {#the-evidence}

一次调用拿到 Agent 判断告警所需的全部信息：`GET /api/v1/alerts/{id}/evidence`，或 MCP 工具 `get_evidence`（作用域 `status:read`）。

下面是一个删减过的例子。两个检测点连不上 `git.example.com`。四分钟前这个服务刚部署过一次。再早一点，部署脚本在这个服务的机器上执行 reload 失败了。

```jsonc
{
  "alert": {"id": "0cdfbada62dd", "status": "firing", "severity": "critical", "title": "git.example.com is down",
            "service": "git.example.com", "labels": {"alertname": "ServiceDown", "failure_domain": "origin-1"}},
  "window": {"from": "2026-11-02T01:28:00Z", "to": "2026-11-02T02:03:10Z"},
  "probes": {"services": ["git.example.com"], "locations": 2, "failing": ["edge-1", "edge-2"],
             "results": [{"location": "edge-1", "instance": "https://git.example.com/", "module": "http_head",
                          "ok": false, "failing_since": "2026-11-02T01:54:30Z", "reason": "connect"}]},
  "related": [{"id": "7a01c2e9f3b4", "title": "registry.example.com is down", "why": "failure_domain=origin-1"}],
  "changes": [{"time": "2026-11-02T01:53:40Z", "kind": "deploy", "service": "git.example.com", "summary": "git v2.4.1"}],
  "done": {"machines": ["origin-1"],
           "sessions": [{"id": "20261102-015312-3fa9c1", "machine": "origin-1", "command": "sudo systemctl reload nginx",
                         "exit": 1, "phase": "before"}]},
  "links": {"runbook": "https://github.com/example/ops/blob/main/runbooks/ServiceDown.md"}
}
```

- **probes** 是告警组的服务在每个检测点的一条结果：是否正常、从什么时候开始失败、为什么失败（`dns`、`connect`、`timeout`、`tls` 等）。心跳的告警还会加上心跳的条目和要去的机器（[心跳](heartbeats.md)）。
- **related** 是同一故障域里其他正在触发的告警组。
- **changes** 是窗口内的[变更事件](#change-events)。
- **done** 是人和 Agent 已经做过的事，包括告警所在机器上的会话。每条标着 `before`、`during` 或 `after`，即在告警之前、期间还是之后。
- **时间窗口**从告警组开始前 30 分钟起（`?before=2h` 可加长，最多 24 小时），所以造成告警的那次会话也在答案里。

同一件事按时间排列就是 `GET /api/v1/alerts/{id}/timeline`，或 `get_timeline`。告警、钩子、变更、会话和备注的事件各占一行。控制台的告警页面把时间线和证据并排显示。

这些答案里来自外部的文本怎样被清理，见安全模型（[来自 Beacon 之外的文字](../self-hosting/security.md#text-from-outside-beacon)）。

## 立刻复测 {#probe-again-now}

修完之后，Agent 不必等下一轮检测。`POST /api/v1/probe-now`，或 MCP 工具 `probe_now`（作用域 `alerts:write`），让所有检测这个目标的检测点立刻重测一次，并返回每个检测点的结果：

```bash
curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"alert_id": "0cdfbada62dd", "wait": 45}' https://status.example.com/api/v1/probe-now
```

每个目标在各检测点分别有结果，失败的排在前面，还有一行 `summary`，比如 `7 OK, 2 failing (dns)`。`missing` 列出至少缺失一项结果的检测点。告警页的**立即复测**会运行同样的检查。按告警 ID 发起的复测会进入该告警的时间线和审计日志。检测点推送的样本和日常检测相同，规则引擎判断满足恢复条件后，告警才恢复。

限制：

- 同一个目标最多每 15 秒按需复测一次。
- 一个令牌同时最多三个请求。
- 一次请求最多 20 个目标，最多等 90 秒。

`probe_now` 把检测点列为 `missing` 时，见 [probe_now 把检测点列为 missing](../self-hosting/troubleshooting.md#probe_now-names-locations-as-missing)。

## 备注 {#notes}

备注告诉大家出了什么问题、做了什么、还剩什么。用 `POST /api/v1/alerts/{id}/notes` 发送，请求体 `{"text": "…"}`，或用 MCP 工具 `add_note`（作用域 `alerts:write`）。

- 卡片显示最新的三条。
- 告警群里每条都会出现。
- 审计日志全部保留。每条最多 2000 个字符，每个告警组保留最新的 100 条。

参考 Agent 用备注记录进展，将认领和静默留给值班人。已识别身份的 Agent 调用 `ack_alert` 时，只记录其响应。人认领后会暂停人工升级通知，持续时间由 `ack_ttl` 决定。独立凭据和未记录身份来源的历史认领沿用原有行为，仍会暂停升级通知。以 `public:` 开头的备注还会作为事件的进展显示在[状态页](status-pages.md)上。

## 变更事件 {#change-events}

部署和 Flux 的同步以变更事件送到 Beacon 之后，证据就能说出故障前刚改了什么。怎样发送，见[部署与 Flux](../integrations/change-events.md)。

## 自动续期的令牌 {#tokens-that-renew-themselves}

无头 Agent 的令牌会在到期前自己续期，不用人再发新的。见[无头 Agent 与 CI](../integrations/headless.md#tokens-that-renew-themselves)。

## 检查一下 {#check-it}

1. **第 4 步的演练结束后，问 Agent** *「上一个告警你做了什么？」*
2. **把 `get_timeline` 和控制台的告警页对着看。** 钩子的调用、机器上的会话、`probe_now` 的结果和备注都应该在。
3. **打开 Hook 页面**（`/console/hooks`）。每次调用都应该是 202，没有重试。

不工作时：

- [Agent 没有被叫醒](../self-hosting/troubleshooting.md#the-agent-is-not-woken)：钩子从不触发。
- [告警一直没有来](../self-hosting/troubleshooting.md#an-alert-never-arrives)：告警根本没触发。

## 下一步 {#next}

- [给 Agent 一台机器](machines.md)：证据把 Agent 送过去的那条 SSH 通道。
- [演练一次故障](drills.md)：同一个循环，故意演练一遍。
- [复查 Agent 做了什么](console.md)：时间线、会话和回放，给人看的。
