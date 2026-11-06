---
sourceHash: sha256-969524f7
---

# 让 Agent 值班

配好钩子之后，服务一出问题，Beacon 就会叫醒你指定的 Agent：它一次调用读到全部证据，通过 SSH 通道登上机器处理，修完让每个检测点复测，再留下一条备注，人在卡片和控制台里都能看到。Beacon 不调用任何模型，也不要求任何人审批；Agent 做什么由运行它的 harness 决定，而它做过的一切都有记录。

你需要：Beacon 已经运行（[装在服务器上](../self-hosting/install.md)的第 1 到 4 步）；一个已通过 MCP 接入的 Agent（[Claude Code](../integrations/claude-code.md)、[Codex](../integrations/codex.md)或[用令牌的 Agent](../integrations/headless.md)）；以及一个钩子够得着的地址：一台运行 `beacon-oncall` 的服务器、你自己写的接收端，或一个 GitHub workflow。

## 先手动走一遍 {#try-the-loop-by-hand}

接钩子之前，先由你代替钩子，把整个流程走一遍：[演练一次故障](drills.md)说明怎样开始一次演练、`ServiceDown` 什么时候触发，[你替钩子走一遍循环](drills.md#walk-the-loop)给出要问 Agent 的话，以及它接着会做什么。被叫醒的 Agent 也是这样工作的，钩子只是代替了那个开口问的人。

## 叫醒 Agent {#wake-an-agent}

1. **在配置里列出钩子。** 钩子从 `config.yaml` 读取：

   ```yaml
   public_url: https://status.example.com     # 钩子要告诉 Agent 证据在哪里

   agents:
     - name: claude-oncall
       kind: webhook
       url: https://agents.example.com/hooks/beacon
       secret_file: /etc/beacon/secrets/agent-hook-secret   # 至少 24 个随机字符
       severities: [critical, warning]
       resolved: true                            # 告警恢复时也通知
   ```

   `severities` 说明哪些级别的告警组会叫醒它（默认 `[critical]`），可选的 `service` 正则可以再收窄。`kind: github` 的钩子改为触发一个 workflow：配置块和各字段的含义见 [GitHub Actions](../integrations/github-actions.md)。

2. **校验配置并重启。** 配了钩子却没有 `public_url`、webhook 既没有 `secret_file` 也没有 `headers_file`（给有自己密钥的接收端用）、密钥不足 24 个字符，`beacon check-config` 都会拒绝：

   ```bash
   docker exec beacon beacon check-config -config /etc/beacon/config.yaml && docker compose restart beacon
   ```

3. **把另一头立起来。** webhook 需要有程序来运行 Agent。`beacon-oncall` 是参考 harness：它校验签名，然后启动你指定的程序，载荷从标准输入传入（[无头 Agent 与 CI](../integrations/headless.md)）；请求头和载荷见[被 webhook 唤醒的 Agent](../integrations/webhooks.md)。

4. **弄坏点什么，看它触发。** 再开始一次演练，等卡片出来，在卡片上找关于 Agent 的那一行。每次调用的结果同样记在审计日志（`wake_agent`）、告警群和控制台的 **Hook** 页面里：

   ```
   10:02 已唤醒 Agent claude-oncall
   ```

新告警组第一次发出通知时钩子被调用一次，告警持续期间不再调用；设了 `resolved` 的话，告警组恢复 `resolved_after`（默认一分钟）之后再调用一次。调用没有应答、超时、返回 429 或 5xx 时，分别在 5 秒、20 秒、1 分钟、3 分钟后重试；告警本身的送达从不等它。

## 证据 {#the-evidence}

一次调用拿到 Agent 判断告警所需的全部信息：`GET /api/v1/alerts/{id}/evidence`，或 MCP 工具 `get_evidence`（作用域 `status:read`）。下面是一个删减过的例子：两个检测点连不上 `git.example.com`；四分钟前这个服务刚部署过一次；再早一点，部署脚本在这个服务的机器上执行 reload 失败了。

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
- **related** 是同一故障域里其他正在触发的告警组；**changes** 是窗口内的[变更事件](#change-events)；**done** 是人和 Agent 已经做过的事，包括相关机器上的会话，每条标着在告警之前、期间还是之后。
- **时间窗口**从告警组开始前 30 分钟起（`?before=2h` 可加长，最多 24 小时），所以造成告警的那次会话也在答案里。

同一件事按时间排列就是 `GET /api/v1/alerts/{id}/timeline`，或 `get_timeline`：告警、钩子、变更、会话和备注的事件各占一行。控制台的告警页面把它和证据并排显示。这些答案里来自外部的文本怎样被清理，见安全模型（[来自 Beacon 之外的文字](../self-hosting/security.md#text-from-outside-beacon)）。

## 立刻复测 {#probe-again-now}

修完之后，Agent 不必等下一轮检测：`POST /api/v1/probe-now`，或 MCP 工具 `probe_now`（作用域 `alerts:write`），让所有检测这个目标的检测点立刻重测一次，并返回每个检测点的结果：

```bash
curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"alert_id": "0cdfbada62dd", "wait": 45}' https://status.example.com/api/v1/probe-now
```

你会看到每个检测点一行，失败的排在前面，还有一行 `summary`，比如 `7 OK, 2 failing (dns)`；`missing` 列出没有及时回答的检测点。检测点推送的样本和平时每轮相同，所以告警在规则引擎下一次评估时就恢复。同一个目标最多每 15 秒按需复测一次，一个令牌同时最多三个请求；一次请求最多 20 个目标，最多等 90 秒。`probe_now` 把检测点列为 `missing` 时，见 [probe_now 把检测点列为 missing](../self-hosting/troubleshooting.md#probe_now-names-locations-as-missing)。

## 备注 {#notes}

备注告诉大家出了什么问题、做了什么、还剩什么：`POST /api/v1/alerts/{id}/notes`，请求体 `{"text": "…"}`，或 MCP 工具 `add_note`（作用域 `alerts:write`）。卡片显示最新的三条，告警群里每条都会出现，审计日志全部保留（每条最多 2000 个字符，每个告警组保留最新的 100 条）。写备注不等于认领告警；认领用 `ack_alert`，而且只应该由人来做的。以 `public:` 开头的备注还会作为事件的进展显示在[状态页](status-pages.md)上。

## 变更事件 {#change-events}

部署和 Flux 的同步以变更事件送到 Beacon 之后，证据就能说出故障前刚改了什么。怎样发送：见[部署与 Flux](../integrations/change-events.md)。

## 自动续期的令牌 {#tokens-that-renew-themselves}

无头 Agent 的令牌会在到期前自己续期，不用人再发新的：见[无头 Agent 与 CI](../integrations/headless.md#tokens-that-renew-themselves)。

## 检查一下 {#check-it}

第 4 步的演练结束后，问 Agent *「上一个告警你做了什么？」*，把 `get_timeline` 和控制台的告警页对着看：钩子的调用、机器上的会话、`probe_now` 的结果和备注都应该在。再看 **Hook** 页面（`/console/hooks`）：每次调用都是 202，没有重试。

不工作时：[Agent 没有被叫醒](../self-hosting/troubleshooting.md#the-agent-is-not-woken)排查从不触发的钩子，[告警一直没有来](../self-hosting/troubleshooting.md#an-alert-never-arrives)排查根本没触发的告警。

## 下一步 {#next}

- [给 Agent 一台机器](machines.md)：证据把 Agent 送过去的那条 SSH 通道。
- [演练一次故障](drills.md)：同一个循环，故意演练一遍。
- [复查 Agent 做了什么](console.md)：时间线、会话和回放，给人看的。
