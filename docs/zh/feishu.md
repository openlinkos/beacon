# 配置飞书应用

[English](../feishu.md) | 简体中文

LinkOS Status 通过飞书（或 Lark）的**企业自建应用**发告警卡片，并接收卡片上的按钮点击。群自定义机器人做不到这些：它发的消息没有回调，也不能更新。不过自定义机器人仍然有用，可以和应用一起发不占 API 额度的普通消息。

建两个应用，一个正式用、一个测试用（例如「LinkOS Status」和「LinkOS Status Dev」）：飞书把每次回调只交给一个连接，测试实例如果用正式应用，会抢走正式环境的点击。

## 1. 创建应用

1. 在[飞书开放平台](https://open.feishu.cn/app)（或 [Lark](https://open.larksuite.com/app)）创建**企业自建应用**。
2. 在「添加应用能力」里添加「机器人」。
3. 在「凭证与基础信息」里记下 **App ID** 和 **App Secret**，分别写进 `feishu.app_id_file`、`feishu.app_secret_file` 指向的文件。

## 2. 权限

在「权限管理」里开通：

| 权限 | 用途 |
| --- | --- |
| 以应用身份发消息（`im:message:send_as_bot`） | 向告警群发卡片 |
| 获取与更新应用发送的消息（`im:message` 或 `im:message:update`） | 原地更新卡片 |
| 发送应用内加急消息（`im:message.urgent`） | 给值班的人加急 |

如果控制台里的名字不一样，就找接口「发送消息」「更新应用发送的消息卡片」「发送应用内加急」对应的权限。

## 3. 回调

在「事件与回调」→「回调配置」里：

- **长连接**（推荐，`feishu.callback: websocket`）：选「使用长连接接收回调」。服务主动连到飞书，不需要公网地址。然后添加回调「卡片回传交互」（`card.action.trigger`）。
- **HTTP**（`feishu.callback: http`）：请求地址填 `https://<你的域名>/hooks/feishu`，把 **Verification Token**（开了加密的话还有 **Encrypt Key**）写进 `feishu.verification_token_file`、`feishu.encrypt_key_file` 指向的文件。然后添加「卡片回传交互」。

注意要在**回调**页签添加 `card.action.trigger`，不是事件页签：旧版卡片回调走不了长连接。

## 4. 发布

创建版本并发布，由管理员在管理后台审批。权限变化只有在发布的版本里才生效。

## 5. 告警群

1. 建一个群（例如「LinkOS 告警」），拉入值班的人。
2. 把应用的机器人加进群（群设置 → 群机器人 → 添加机器人）。
3. 找到群的 `chat_id`（`oc_…`），例如在 API 调试台以应用身份调用「获取用户或机器人所在的群列表」，填进 `feishu.chat_id`。
4. 找到每个值班的人在这个应用下的 `open_id`（`ou_…`），写进 `feishu.on_call`，附上卡片上显示的名字。只有他们能操作卡片，加急也只发给他们。
5. 在同一个群里再加一个**自定义机器人**。安全设置选关键词（对应 `feishu.webhook_keyword`，默认 `LinkOS`）和/或签名校验（密钥写进 `feishu.webhook_secret_file`），把它的 webhook 地址写进 `feishu.webhook_url_file`。

## 6. 检查

启动服务，日志里应出现 `feishu long connection ready`。然后演练：`linkos-status drill fail 15m` 让 `/drill/healthz` 失败；检测点发现、告警触发后，群里会收到卡片。认领、静默，再 `linkos-status drill off`，看卡片变绿。

## 额度

免费版下，一个企业的所有自建应用共用每月的 API 调用额度（写这篇时是 1 万次，以控制台为准）。发卡片、通过 API 更新卡片、加急各算一次；点击按钮后的卡片更新不算。把 `feishu.monthly_budget` 设成你的额度：用到 70% 以后，只有严重告警用应用发，其余改由自定义机器人发。

电话、短信加急需要企业认证；LinkOS Status 只用应用内加急，再往上升级到 Pushover。
