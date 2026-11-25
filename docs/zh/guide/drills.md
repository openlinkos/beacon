---
sourceHash: sha256-7fb906de
---

# 演练一次故障

读完这一页，你已经在不动任何真实服务的前提下，完整看过一遍值班循环：

- 检测点看到故障，规则触发；
- 测试接收端收到通知；
- 你的 Agent 读取证据、登上机器、让检测点复测修复、留下备注；
- 告警按检测点其后看到的结果恢复。

演练的用处，是发现某部手机收不到通知、某个钩子配错了。而且是在一切还正常的时候发现，不是在凌晨三点。

你需要：

- Beacon 已经运行，至少两个检测点在汇报。单个检测点的话永远不会算中断。
- Agent 步骤需要先[接入 Agent](on-call.md)。如果要验证自动唤醒，请先启动钩子的接收端。
- 要验证通知送达，请准备测试 [Slack 频道](../integrations/slack.md)、[通知接收端](../integrations/notification-webhook.md)或[飞书群](../integrations/feishu.md)。未配置接收端时，可以在控制台查看告警。

从零开始的话，按[装在服务器上](../self-hosting/install.md)的第 1 到 4 步来。或者用[在本机试一试](quickstart.md)，在一台机器上跑完整个循环。命令在中心上执行，参考部署里是 `docker exec beacon beacon …`。

## 演练服务 {#the-drill-service}

Beacon 自带一个供演练用的服务。`/drill/healthz` 平时回答 200，直到你下令改变。每个检测点都把它作为 critical 服务 `drill` 来检测（`targets/all-drill.yaml`）。请让 `drill` 留在用户可访问的状态页组件之外。

## 弄坏它 {#break-it}

```bash
docker exec beacon beacon drill fail 15m
```

你会看到 `drill until …: /drill/healthz answers 503`。

- `beacon drill status` 报告演练的状态。
- `beacon drill off` 结束演练。

开始和结束演练都记为变更事件。所以告警的证据里能看到正在演练，以及演练到点自己结束的时间。处理告警的 Agent 结束不了演练，但可以过了那个时间用 `probe_now` 确认。

以两个检测点为例，接下来是：

1. **一分钟内**两个检测点都看到 503，`probe_success{service="drill"}` 各自变为 0。
2. **满 3 分钟**后，规则把每个检测点计为失败。两个占两个，既满「至少两个」也满「不少于一半」，于是 **`ServiceDown` 在这个比例持续 40 秒后触发**。
3. **等待该路由的告警分组时长**。参考部署中，严重 `ServiceDown` 等待 5 秒。随后 Beacon 记录告警组，唤醒匹配的 Agent，并向配置的渠道提交通知。从执行命令起算，约五分钟后可以在**告警**、`GET /api/v1/alerts` 或 MCP 的 `list_alerts` 里看到它。手机升级按设置的延时执行。

只有一个检测点时，什么都不会触发，这是有意的设计。

## 你替钩子走一遍循环 {#walk-the-loop}

先什么都不接，自己当钩子。问你的 Agent：*「现在有什么在告警吗？看看证据，检查一下机器，告诉我你打算怎么做。」*

它用 `list_alerts` 找到告警，再读 `get_evidence`。这一个答案里就有每个检测点的结果和原因（`http_status`，503）。Agent 按检测点看到的情况回答。被唤醒的 Agent 也是这么工作的：钩子只是代替了那个开口问的人。

然后让它逐步动手，一次练一环：

- **`add_note`** 把 Agent 的发现记到时间线和卡片。参考 Agent 把认领和静默留给值班人。值班人的认领有效期间，暂停向人升级通知。用控制台或卡片亲自验证接手流程。
- **在机器上执行命令。** 请 Agent 在一台[已登记的机器](machines.md)上运行 `hostname`，然后在**会话**中回放。参考演练使用 Beacon 自身的健康检查端点，没有关联机器。有明确关联机器的告警会在详情页同时显示相关会话。
- **`probe_now`** 让每个检测点立刻复测目标。它不会改变演练的答案：服务仍是 503。但练的正是 Agent 修完真故障后要做的那次调用。

## 结束演练，看它恢复 {#end-it-and-watch-it-recover}

```bash
docker exec beacon beacon drill off
```

检测点下一轮就会成功，通常在一分钟以内。规则引擎看到成功结果后，Beacon 记录恢复。Slack 和带签名的通知 Webhook 会发出一条新的恢复消息。飞书应用卡片会在原处更新。告警连同时间线和会话在控制台里保留 7 天。

## 验证手机送达和交接 {#verify-phone-delivery-and-handover}

由所有者准备一个独立的 Beacon 测试实例，确保有两个检测点在上报，且测试手机能访问 `public_url`。只使用测试接收端和凭据，[邀请](people.md#invite-people)两位拥有 `oncall` 角色的值班人，并设置 `service: '^drill$'` 来限定测试钩子。若只验收手机推送，请在 `config.yaml` 中设置 `escalation.critical.channel: none`，再配置手机渠道和延时。下例的 `bark.url_file` 指向测试 [Bark 接收端](../reference/config.md#pushover-bark-and-healthchecksio)。

```yaml
escalation:
  critical: {channel: none, bark: 20s}
```

如果已有 `escalation` 段落，就更新其中的 `critical` 项。在 `/opt/beacon` 下运行 `sudo ./install.sh` 应用配置。在**概览 → 严重告警通知**中核对首发渠道和手机推送延时。

1. **找到首次告警。** 先确认演练服务正常，再让测试钩子接收请求（例如返回 HTTP 202），接收后不执行操作。演练时长须覆盖规则评估和最长的手机推送延时。例如延时为 15 分钟时，运行 `docker exec beacon beacon drill fail 30m`。约五分钟内，在**告警**中找到 `drill` 的严重 `ServiceDown`，或从已配置的测试群打开通知。记录告警 ID 和开始时间，并在时间线确认钩子受理。若要验证钩子受理后程序执行失败，请启用[执行结果上报](../integrations/headless.md#report-programme-execution)，让测试程序以非零状态退出，再核对钩子受理和程序执行失败两条记录。
2. **等待定时推送。** 预期推送到达前，让告警保持触发，暂不静默或认领。在时间线记录服务商受理时间。验收手机时，再记录通知实际出现在设备上的时间。从 Pushover 或 Bark 链接登录，核对告警 ID 和开始时间。测试接收端返回 `2xx` 可验证 Beacon 提交和告警链接。设备在告警仍触发时实际显示推送，才算通过手机送达验收。
3. **交接并验证恢复。** 第一位值班人在控制台点**认领并备注**，写下看到的情况和待检查事项。第二位值班人用自己的账号登录并打开同一告警，核对前一位的备注和各地失败情况，再点**接手并备注**。时间线应显示两人的名字和备注。运行 `docker exec beacon beacon drill off`，点**立即复测**，确认检测和告警恢复。若已配置聊天渠道，也请在那里核对恢复。

### 验证飞书备用渠道 {#verify-feishu-fallback}

另起一轮演练，配置测试用的[飞书应用和自定义群机器人](../integrations/feishu.md)。等告警恢复后，将测试应用移出测试群并保留自定义机器人。确认严重告警渠道仍为 `app`，再次启动演练。在时间线核对应用提交失败，并确认群里收到备用消息。如果失败后立刻提交了 Pushover，这是备用通知，时间线会单独记录。打开机器人消息链接，登录并核对告警开始时间是否属于这一轮。结束演练并恢复测试应用。若要验证钩子拒绝唤醒，另做一轮，保留测试应用并让测试钩子返回 HTTP 503，检查失败记录和人工渠道的实际送达。

## 检查一下 {#check-it}

告警期间和恢复之后：

- **在中心的 VictoriaMetrics**（`http://127.0.0.1:8428/vmui`）里，`probe_success{service="drill"}` 有两条序列，演练期间都是 0。
- **演练服务**对 `curl -i https://status.example.com/drill/healthz` 回答 `503`。
- **在控制台上**，告警页显示每个检测点的结果、备注和已设的静默。通过 SSH 做的那次检查可在**会话**里回放（[复查 Agent 做了什么](console.md)）。
- **6 分钟后还没有告警**，先确认两个检测点都在汇报、healthz 确实回答 503。[告警一直没有来](../self-hosting/troubleshooting.md#an-alert-never-arrives)讲得更全。

## 下一步 {#next}

- [让 Agent 值班](on-call.md)：由钩子在真实告警时唤醒 Agent，不用人问。
- [检测一个服务](monitoring.md)：演练借用的目标和规则，换成你自己的服务。
- [发布状态页](status-pages.md)：选择要向用户展示的监控服务。
