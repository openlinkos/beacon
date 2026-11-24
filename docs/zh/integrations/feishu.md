---
sourceHash: sha256-8a5ed5ac
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
  # webhook_url_file: /etc/beacon/secrets/feishu-webhook-url      # 可选的群自定义机器人
  # webhook_keyword: Beacon
```

卡片写明什么坏了、从什么时候开始、哪些检测点失败及原因、Agent 留了什么备注。告警有变化，卡片就原地更新，恢复后变绿。

- 值班的人用卡片上的按钮认领和静默。
- 严重告警 10 分钟没人认领，就在应用内给他们加急。
- 同一个群里再放一个自定义机器人，发不占 API 额度的普通消息。

演练需要一个 Beacon 中心和两个正在上报的检测点。你还需要能在飞书或 Lark 组织中创建自建应用、将应用加入测试群，并请组织管理员审批应用发布。请预留组织审批所需的时间。

Beacon 通过**企业自建应用**发卡片，并接收卡片上的按钮点击。只用群自定义机器人做不到：它发的消息没有回调，也不能更新。不过这个机器人和应用搭配仍然有用，负责发普通消息：

- `info` 级别的告警；
- 应用当月额度快用完之后的告警；
- 人和 Agent 操作的回显。

已有正式应用时，请用独立的应用和群做测试。飞书每次只把回调交给一个连接，测试实例复用正式应用时可能收到正式告警的卡片操作。

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
| 获取群组信息（`im:chat:readonly`） | 配置时查找测试群和群成员 |

如果后台里的权限名称不一样，就找接口「发送消息」「更新应用发送的消息卡片」「发送应用内加急」对应的权限。

## 3. 发布应用 {#3-publish}

发布一个包含机器人能力和上述权限的应用版本，并将测试群成员纳入应用的可用范围。审批通过后再把应用加入群。回调订阅等 Beacon 运行起来后再发布。

## 4. 准备测试群 {#4-prepare-the-test-group}

1. 在自己的组织里创建群，例如「Beacon 测试告警」，拉入会认领测试告警的成员。
2. 在群设置中打开**群机器人 → 添加机器人**，选择刚发布的应用。
3. 在所用平台的 API 调试台，用**同一个测试应用**的 `tenant_access_token` 调用[获取用户或机器人所在的群列表](https://open.feishu.cn/document/server-docs/group/chat/list)（`GET /im/v1/chats`）。复制测试群的 `chat_id`（`oc_…`）。
4. 保持选中同一个应用，调用[获取群成员列表](https://open.feishu.cn/document/server-docs/group/chat-member/get)（`GET /im/v1/chats/{chat_id}/members`）。将 `member_id_type` 设为 `open_id`，把每位测试值班人的 `member_id`（`ou_…`）填入 `feishu.on_call`。飞书为不同应用分别生成这些 ID。Beacon 的控制台账号由**成员**页面单独管理。
5. 如需测试纯文本消息和备用送达，在同一测试群添加**自定义机器人**。关键词设为 `Beacon`，或与 `feishu.webhook_keyword` 保持一致。将 webhook 地址写入 `feishu.webhook_url_file` 指向的文件。若启用签名校验，再把签名密钥写入 `feishu.webhook_secret_file` 指向的文件。首次只测试应用卡片时，可以保留这些配置的注释。

## 5. 启动 Beacon 并订阅回调 {#5-configure-callbacks}

把 App ID 和 App Secret 分别写入 `app_id_file` 和 `app_secret_file` 指向的文件。参考部署的这些文件位于 `/opt/beacon/secrets/`。编辑 `/opt/beacon/config.yaml` 中的 `feishu:`，填入测试群 ID 和至少一位测试值班人的 `open_id`。使用 Lark 时设置 `domain: lark`。

应用配置前，先在 Beacon 配置里选好回调方式。

- **长连接（推荐）**。设置 `callback: websocket`，允许机器向飞书或 Lark 开放平台发起连接。
- **HTTP**。设置 `callback: http`。将应用的 **Verification Token** 写入 `verification_token_file` 指向的文件。应用启用回调加密时，将 **Encrypt Key** 写入 `encrypt_key_file` 指向的文件。将文件路径填到 `feishu:` 下。飞书需要能通过你的公网域名访问 `https://status.example.com/hooks/feishu`。

参考部署按下列命令应用变更。`install.sh` 会把密钥文件的属主设为 65532、权限设为 0400，校验配置，并在配置或密钥变更后重启 Beacon。

```bash
cd /opt/beacon
sudo ./install.sh
docker exec beacon beacon check-config -config /etc/beacon/config.yaml
```

配置检查应显示 `Feishu app true`。然后打开开发者后台的**事件与回调 → 回调配置**。

1. **长连接**。在 `docker compose logs beacon` 中看到 `feishu long connection ready` 后，选择**使用长连接接收回调**并保存。飞书要求客户端在线才允许保存。[飞书长连接配置指南](https://open.feishu.cn/document/event-subscription-guide/callback-subscription/step-1-choose-a-subscription-mode/configure-callback-request-address)
2. **HTTP**。选择将回调发送到开发者服务器，保存上面配置的 HTTPS 回调地址。Beacon 必须运行，才能响应飞书的地址校验。
3. 在**回调配置**中添加**卡片回传交互**（`card.action.trigger`）。发布新版并等审批通过，再测试卡片操作。[飞书回调订阅指南](https://open.feishu.cn/document/event-subscription-guide/callback-subscription/callback-overview)

## 6. 检查 {#6-check-it}

在中心检查就绪状态。

```bash
curl -sS -i http://127.0.0.1:17001/readyz
```

就绪接口返回 HTTP 200 且两个检测点都在上报后，启动演练。

```bash
docker exec beacon beacon drill fail 15m
```

1. 在**测试群**中等待 `drill` 的严重 `ServiceDown` 卡片。启动演练后约需五分钟。打开**时间线**，登录 Beacon 控制台，确认显示的是同一条告警。
2. 用 `feishu.on_call` 里登记的飞书账号，在卡片上选择**认领并备注**。确认同一告警的时间线上出现备注和值班人的名字。
3. 执行 `docker exec beacon beacon drill off`。确认探测恢复、同一张卡片变绿。如需测试定时手机推送或群自定义机器人备用送达，请按[通知渠道专项演练](../guide/drills.md#verify-phone-delivery-and-handover)验证。

如果某一步没有按预期发生：

- **长连接一直没就绪。** 检查 App ID、App Secret、域名和出站网络。HTTP 回调模式不会打印长连接日志。
- **`/readyz` 返回 503。** 查看 `not_ready` 列出的原因，可能涉及长连接、最近的飞书 API 调用失败、每月额度或未收到规则引擎的 Watchdog。也可以用有 `status:read` 权限的令牌，通过 `GET /api/v1/bridge` 或 MCP 的 `status_summary` 查看原因和 API 用量。收到卡片并能认领，才能验证你们群内的送达。
- **卡片到了，但认领失败。** 检查已发布的回调订阅是否包含 `card.action.trigger`，值班人的 `ou_…` 是否由这个应用查到。若另一个实例复用同一应用，它可能收到这次点击。

## 额度 {#quota}

免费版下，一个组织的所有自建应用共用每月的 API 调用额度。写这篇时是 1 万次，以后台显示为准。

发卡片、通过 API 更新卡片、加急各算一次。点击按钮后的卡片更新不算。通过 API 或 MCP 留的备注、为告警唤醒 Agent，都会通过 API 更新一次卡片，额度用到 90% 为止。

把 `feishu.monthly_budget` 设成你的额度。月份按配置里的 `timezone` 计算。随着额度用掉：

- 用到 70% 以后，只有严重告警通过应用发，其余改由自定义机器人发；
- 用到 90% 以后，不再通过 API 更新卡片，只在告警恢复时更新；
- 用到 100% 时，这个月剩下的时间里不再通过应用发任何东西，`/readyz` 也会报出来。

电话和短信加急需要企业认证。Beacon 只用应用内加急，再往上升级到 Pushover。默认（`escalation.critical`）情况下，严重告警没人认领，10 分钟后加急。配置了 Pushover 的话，15 分钟后以 Pushover 的紧急级别发出。
