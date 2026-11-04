---
sourceHash: sha256-d20444dc
---

# 配置飞书应用

做完这一页，告警会以卡片的形式发到飞书（或 Lark）群里。配置如下：

```yaml
feishu:
  app_id_file: /etc/beacon/secrets/feishu-app-id
  app_secret_file: /etc/beacon/secrets/feishu-app-secret
  domain: feishu                       # 或 lark
  callback: websocket                  # 或 http，同时要 verification_token_file（以及 encrypt_key_file）
  chat_id: oc_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
  on_call:                             # 只有他们能操作卡片，加急也只发给他们
    - {open_id: ou_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx, name: Alex}   # name 显示在卡片上
  monthly_budget: 10000                # 应用每月的 API 调用次数（见「额度」）
  webhook_url_file: /etc/beacon/secrets/feishu-webhook-url        # 自定义机器人
  webhook_keyword: Beacon
```

卡片写明什么坏了、从什么时候开始、哪些检测点失败及原因、Agent 留了什么备注；告警有变化，卡片就原地更新，恢复后变绿。值班的人用卡片上的按钮认领和静默；严重告警 10 分钟没人认领，就在应用内给他们加急。同一个群里再放一个自定义机器人，发不占 API 额度的普通消息。你需要在自己的组织里建一个自建应用，并由管理员审批：操作本身大约一刻钟，审批要多久取决于你的组织。

Beacon 通过**企业自建应用**发卡片，并接收卡片上的按钮点击。只用群自定义机器人做不到：它发的消息没有回调，也不能更新。不过它和应用搭配仍然有用，负责发普通消息：`info` 级别的告警、应用当月额度快用完之后的告警，以及人和 Agent 操作的回显。

建两个应用，一个正式用、一个测试用（例如「Beacon」和「Beacon Dev」）。飞书把每次回调只交给一个连接，测试实例如果用正式应用，会抢走正式环境的点击。

## 1. 创建应用 {#1-create-the-app}

1. 在[飞书开放平台](https://open.feishu.cn/app)（或 [Lark 开放平台](https://open.larksuite.com/app)，这时设置 `feishu.domain: lark`）创建**企业自建应用**。
2. 在「添加应用能力」里添加「机器人」。
3. 在「凭证与基础信息」里记下 **App ID** 和 **App Secret**。它们分别写进 `feishu.app_id_file` 和 `feishu.app_secret_file` 指向的文件（第 5 步）。

## 2. 权限 {#2-permissions}

在「权限管理」里开通：

| 权限 | 用途 |
| --- | --- |
| 以应用身份发消息（`im:message:send_as_bot`） | 向告警群发卡片 |
| 获取与更新应用发送的消息（`im:message` 或 `im:message:update`） | 原地更新卡片 |
| 发送应用内加急消息（`im:message.urgent`） | 给值班的人加急 |

如果后台里的权限名称不一样，就找接口「发送消息」「更新应用发送的消息卡片」「发送应用内加急」对应的权限。

## 3. 回调 {#3-callbacks}

在「事件与回调」→「回调配置」里，选择点击怎么送到 Beacon：

- **长连接**（推荐，`feishu.callback: websocket`，也是默认值）：选「使用长连接接收回调」。由 Beacon 主动连到飞书，不需要公网地址。然后添加回调「卡片回传交互」（`card.action.trigger`）。
- **HTTP**（`feishu.callback: http`）：请求地址填 `https://<你的域名>/hooks/feishu`，把 **Verification Token**（开了加密的话还有 **Encrypt Key**）写进 `feishu.verification_token_file` 和 `feishu.encrypt_key_file` 指向的文件。然后添加「卡片回传交互」。

`card.action.trigger` 要在**回调**页签添加，不是事件页签：旧版的卡片回调不走长连接。

## 4. 发布 {#4-publish}

创建版本并发布，由管理员在管理后台审批。权限改动只在已发布的版本里生效，所以改过权限要重新发布。

## 5. 告警群 {#5-the-alert-chat}

1. 建一个群（例如「告警」），拉入值班的人。
2. 把应用的机器人加进群（群设置 → 群机器人 → 添加机器人）。
3. 找到群的 `chat_id`（`oc_…`），例如在 API 调试台以应用身份调用「获取用户或机器人所在的群列表」。
4. 找到每个值班的人在这个应用下的 `open_id`（`ou_…`）。只有这些人能操作卡片，加急也只发给他们。
5. 在同一个群里再加一个**自定义机器人**。安全设置选关键词（默认 `Beacon`，可用 `feishu.webhook_keyword` 修改）和/或签名校验（密钥写进 `feishu.webhook_secret_file` 指向的文件）。复制它的 webhook 地址。

每个密钥写进各自的文件（参考部署放在 `/opt/beacon/secrets/` 下，`install.sh` 会把它们的属主设为 65532、权限设为 0400），其余写进 Beacon 的配置：就是本页开头的那段，填上第 3 步的 `chat_id` 和第 4 步的 `open_id`。

以下情况 `beacon check-config` 会拒绝：配了应用却没有 `chat_id`，或 `on_call` 里一个人都没有；`open_id` 不以 `ou_` 开头；HTTP 回调没有 Verification Token。

## 6. 检查 {#6-check-it}

重启 Beacon，让它读到新配置（参考部署在 `/opt/beacon` 下执行 `docker compose restart beacon`），然后依次检查：

```bash
docker exec beacon beacon check-config               # configuration OK: Feishu app true, …
docker logs beacon 2>&1 | grep 'feishu long connection'
curl -s http://127.0.0.1:17001/readyz                # 在中心上：{"not_ready":null,"ready":true}
docker exec beacon beacon drill fail 15m
```

- 启动后几秒内，日志里应出现 `feishu long connection ready`（`callback: http` 时没有这一行）。没有这一行，或者出现 `feishu long connection lost`、`error`：检查文件里的 App ID 和 App Secret，确认带机器人和权限的那个版本已经发布并通过审批，确认回调选的是长连接。
- 告警送不到人时，`/readyz` 返回 503 并列出原因：「the Feishu long connection is down」（长连接断了）、「a Feishu API call failed in the last 10 minutes」（10 分钟内有飞书 API 调用失败）、「the Feishu API budget for this month is used up」（本月额度已用完），或者规则引擎的 Watchdog 没有到达。带 `status:read` 的令牌用 `GET /api/v1/bridge`（MCP 的 `status_summary`）也能看到同样的信息，还有本月已用的调用次数。
- 演练让 `/drill/healthz` 失败。检测点发现、告警触发后（两个检测点时约 4 到 5 分钟），群里会收到卡片。认领、静默，再执行 `docker exec beacon beacon drill off`，看卡片变绿。
- 点击后提示「只有值班的人可以处理告警」：点的人 `open_id` 不在 `feishu.on_call` 里，或者这个 `open_id` 是从别的应用查到的。`open_id` 是按应用区分的。如果 Beacon 根本没收到点击（卡片不变，审计日志里也没有途径为 `feishu` 的记录），说明点击去了别处：被一个用着正式应用的测试实例收走了，或者 `card.action.trigger` 加在了事件页签而不是回调页签，哪里都没送到。

## 额度 {#quota}

免费版下，一个组织的所有自建应用共用每月的 API 调用额度（写这篇时是 1 万次，以后台显示为准）。发卡片、通过 API 更新卡片、加急各算一次；点击按钮后的卡片更新不算。通过 API 或 MCP 留的备注、为告警唤醒 Agent，都会通过 API 更新一次卡片，额度用到 90% 为止。把 `feishu.monthly_budget` 设成你的额度（月份按配置里的 `timezone` 计算）：

- 用到 70% 以后，只有严重告警通过应用发，其余改由自定义机器人发；
- 用到 90% 以后，不再通过 API 更新卡片，只在告警恢复时更新；
- 用到 100% 时，这个月剩下的时间里不再通过应用发任何东西，`/readyz` 也会报出来。

电话和短信加急需要企业认证；Beacon 只用应用内加急，再往上升级到 Pushover。默认（`escalation.critical`）情况下，严重告警没人认领，10 分钟后加急；配置了 Pushover 的话，15 分钟后以 Pushover 的紧急级别发出。
