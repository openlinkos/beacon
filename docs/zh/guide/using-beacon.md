---
sourceHash: sha256-88e3452b
---

# 使用 Beacon

这些页面讲 Beacon 跑起来以后要做的事，按 Beacon 的能力分组。每页是一件事。

每页都写明需要什么、怎么做、怎么检查结果、接下来读什么。刚接触 Beacon？先读 [Beacon 是什么](index.md)、[在本机试一试](quickstart.md)和 [Beacon 如何工作](concepts.md)。

## 监控与告警 {#monitoring-and-alerts}

Beacon 检测什么、从哪里检测，以及告诉你的用户什么。

1. [检测一个服务](monitoring.md)：检测点、检测目标集和模块。每次修改都先检查，立即生效，也能回退。
2. [心跳与到期资源](heartbeats.md)：必须按时报告成功的任务，以及会到期的域名和机器。
3. [发布状态页](status-pages.md)：把检测点一致认定的结果公开出来，附带事件和维护窗口。

给值班的人在聊天里发卡片，见[配置飞书应用](../integrations/feishu.md)。

## Agent 值班 {#agents-on-call}

告警叫醒 Agent。Agent 读证据，修能修的，验证修复，留下备注。

1. [让 Agent 值班](on-call.md)：叫醒 Agent 的钩子、一次调用拿到的证据、按需重测和备注。
2. [演练一次故障](drills.md)：在真正出故障之前，用 Beacon 自带的演练服务走一遍整个闭环。
3. [复查 Agent 做了什么](console.md)：控制台，人在这里看哪些事需要处理，看告警、静默和维护，以及每次会话和它的回放。

Agent 客户端和钩子另一端的程序，见[接入](../integrations/index.md)。

## 机器与 SSH 通道 {#machines-and-the-ssh-channel}

Agent 怎样不持有密钥就登上服务器，以及怎样让它停下。

1. [给 Agent 一台机器](machines.md)：打开通道、添加机器（NAT 后面的也行）、给 Agent 访问权限。
2. [停下 Agent，暂停一切](stop.md)：每一种刹车，以及各自关掉什么。

## 团队与权限 {#the-team-and-permissions}

谁能做什么，人和他们的 Agent 都一样。

1. [邀请你的团队](people.md)：账号和角色、Agent 和它的预设、OAuth 登录、SSH 密钥，以及从令牌迁移过来。

每个角色、预设和作用域都列在[角色、预设与权限](../reference/permissions.md)里。
