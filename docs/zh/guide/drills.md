---
sourceHash: sha256-5aedff91
---

# 演练一次故障

读完这一页，你已经在不动任何真实服务的前提下，完整看过一遍值班循环：检测点看到故障，规则触发，卡片送达，你的 Agent 读取证据、登上机器、让人复测修复、留下备注，告警按检测点其后看到的结果恢复。演练的用处，是在一切还正常的时候就发现某部手机收不到通知、某个钩子配错了——而不是在凌晨三点。

你需要：Beacon 已经运行、至少两个检测点在汇报（单个检测点的话永远不会算中断）；一个已接入的 Agent（[让 Agent 值班](on-call.md)）；想看卡片的话，还要配好聊天渠道（[飞书](../integrations/feishu.md)）。从零开始：[装在服务器上](../self-hosting/install.md)第 1 到 4 步，或[在本机试一试](quickstart.md)在一台机器上跑完整循环。命令在中心上执行，参考部署里是 `docker exec beacon beacon …`。

## 演练服务 {#the-drill-service}

Beacon 自带一个供演练用的服务：`/drill/healthz` 平时回答 200，直到你下令改变；每个检测点都把它作为 critical 服务 `drill` 来检测（`targets/all-drill.yaml`）。自带的状态页不含它，公众不会看到演练。

## 弄坏它 {#break-it}

```bash
docker exec beacon beacon drill fail 15m
```

你会看到 `drill until …: /drill/healthz answers 503`；`beacon drill status` 随时报告状态，`beacon drill off` 结束演练。开始和结束都记为变更事件，告警的证据里就能看到正在演练、以及演练到点自己结束的时间：处理告警的 Agent 结束不了演练，但可以过了那个时间用 `probe_now` 确认。以两个检测点为例，接下来是：

1. **一分钟内**两个检测点都看到 503，`probe_success{service="drill"}` 各自变为 0。
2. **满 3 分钟**后，规则把每个检测点计为失败。两个占两个，既满「至少两个」也满「不少于一半」，于是 **`ServiceDown` 再过 1 分钟触发**——命令之后大约 4 到 5 分钟。它出现在 `GET /api/v1/alerts` 和 MCP 的 `list_alerts` 里。
3. **聊天渠道配好的话**，卡片在告警之后约 30 秒（`group_wait`）到达群里，恢复时变绿；配了 Pushover 的，未经确认的 critical 告警 15 分钟后打到手机上。

只有一个检测点时，什么都不会触发——设计如此。

## 你替钩子走一遍循环 {#walk-the-loop}

先什么都不接，自己当钩子，问你的 Agent：*「现在有什么在告警吗？看看证据，检查一下机器，告诉我你打算怎么做。」* 它用 `list_alerts` 找到告警，读 `get_evidence`——每个检测点的结果和原因（`http_status`，503）一次拿全——然后按检测点看到的情况回答。被唤醒的 Agent 也是这么工作的；钩子只是替你把问题问出来。

然后让它逐步动手，一次练一环：

- `ack_alert` 带备注确认，`add_note` 写下发现：两者都进告警的时间线，人们在卡片和控制台上读得到。
- `silence_alert` 静默告警；在中心上，`beacon silence create -matchers 'alertname=ServiceDown,service=drill' -duration 1h -comment 'drill'` 从命令行建一条（审计日志会记为演练），`beacon silence list` 能看到它。
- 演练的机器若在 [SSH 通道](machines.md)手里，让 Agent 在上面执行一条命令：会话会连同回放出现在告警页上。
- `probe_now` 让每个检测点立刻复测目标；它不会改变演练的答案（服务仍是 503），但练的是 Agent 修完真故障后要打的那通电话。

## 结束演练，看它恢复 {#end-it-and-watch-it-recover}

```bash
docker exec beacon beacon drill off
```

检测点下一轮就成功，一分钟以内；每 30 秒评估一次的规则引擎随即报告告警恢复，Beacon 立刻将其恢复。卡片变绿，告警连同时间线和会话在控制台里保留 7 天。

## 检查一下 {#check-it}

告警期间和恢复之后：

- 在中心的 VictoriaMetrics（`http://127.0.0.1:8428/vmui`）里，`probe_success{service="drill"}` 有两条序列，演练期间都是 0；`curl -i https://status.example.com/drill/healthz` 回答 `503`。
- 控制台的告警页上有每个检测点的结果、备注和静默，还有 Agent 开过的会话及其回放（[复查 Agent 做了什么](console.md)）。
- 6 分钟后还没有告警，先确认两个检测点都在汇报、healthz 确实回答 503；[告警一直没有来](../self-hosting/troubleshooting.md#an-alert-never-arrives)讲得更全。

## 下一步 {#next}

- [让 Agent 值班](on-call.md)：由钩子在真实告警时唤醒 Agent，不用人问。
- [检测一个服务](monitoring.md)：演练借用的目标和规则，换成你自己的服务。
- [发布状态页](status-pages.md)：把演练服务加进页面，看它怎么告诉你的用户。
