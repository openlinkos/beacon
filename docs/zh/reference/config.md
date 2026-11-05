---
sourceHash: sha256-b52e00bb
---

# config.yaml

`beacon serve` 启动时读一个 YAML 文件，默认 `/etc/beacon/config.yaml`；`-config` 指定别的。文件只在启动时读一次，改了要重启。它指名的目录、目标和模块文件（`catalog_dir`、`gateway.targets_dir`、`gateway.modules_file`）是另一回事：服务在首次启动时把它们导入数据库，之后在 SIGHUP 时再导入（[配置](../self-hosting/configuration.md)）。仓库里的示例文件 `config.example.yaml` 带注释列出了每一个键。

两条规则贯穿整个文件：

- **不接受未知键。** 下面的表里没有的键会让启动失败，写错不会静默放过。`beacon check-config -config FILE` 不启动就校验文件、它指名的文件和数据库（[命令行](cli.md)）。
- **密钥是文件，不是值。** 每个凭据都是一个 `*_file` 路径：`rules.token_file`、`feishu.app_id_file`、hook 的 `secret_file`。Beacon 启动时读一次文件，去掉首尾空白，从不记录内容；空文件会让启动失败。这些文件怎么填是部署自己的事：Beacon 不依赖任何密钥管理器。`rules.token_file` 和 webhook hook 的 `secret_file` 至少 24 个字符。

时长按 Go 的写法：`30s`、`5m`、`4h`、`720h`。

## 服务 {#the-service}

| 键 | 类型 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `listen` | 地址 | `:17001` | 控制台、API 与 MCP 在 TLS 代理后面应答的地址 |
| `data_dir` | 路径 | `/var/lib/beacon` | bbolt 状态（告警组、令牌、成员、配置），会话录像在它旁边。CLI 用的管理套接字也在这里；`BEACON_DATA_DIR` 只为 CLI 指定别的目录 |
| `catalog_dir` | 路径 | `/etc/beacon/catalog` | 目录文件（检测点、心跳、到期资源、状态页），首次启动和 SIGHUP 时导入数据库 |
| `public_url` | URL | — | Beacon 自己的地址，在代理后面：它发出去的链接（控制台、一次性链接）和 MCP 客户端登录用的 issuer。Agent 的 hook 也需要它 |
| `trusted_proxies` | 地址或 CIDR 段列表 | — | 前面的反向代理（服务眼中的地址），让 API 看到真实客户端地址 |
| `language` | `zh` 或 `en` | `zh` | 卡片和聊天消息的语言 |
| `timezone` | 时区 | `Asia/Shanghai` | 卡片上的时间；状态页历史的每一天从这里的午夜开始 |
| `storage.driver` | `sqlite` | `sqlite` | 数据库驱动。PostgreSQL 在后续版本提供 |
| `storage.path` | 路径 | `<data_dir>/beacon.db` | 数据库文件。首次启动发现旁边有旧的 bbolt 文件（`status.db`）时，把它在一个事务里整体迁入数据库；旧文件保留为 `status.db.pre-sql`，供回滚 |
| `storage.audit.checkpoint_interval` | 时长 | `24h` | 审计日志的哈希链多久生成一个签名检查点 |
| `storage.audit.retention` | 时长 | `8760h` | 审计记录保留多久（365 天）；剪除旧记录后链仍可校验 |
| `storage.sqlite.journal_mode` | `wal` | `wal` | SQLite 的日志模式。WAL 数据库是一个文件加 `-wal`、`-shm` 两个伴随文件，不能放在网络文件系统上 |
| `storage.sqlite.synchronous` | `full` | `full` | SQLite 的持久化：`full` 让每次提交落盘后才应答 |
| `storage.sqlite.busy_timeout` | 时长 | `15s` | 一条语句等待别的连接持有的锁多久 |

## 保管库 {#the-vault}

密钥保管库的根密钥：它从哪来、由谁包裹。每个库的密钥由根密钥包裹；每个条目的值由其库密钥加密；每条策略行由根密钥派生的签名密钥签名——所以根密钥是唯一要守住的东西。模式在首次启动时选定：已有保管库换一个模式会生成打不开它的新密钥，启动拒绝。

| 键 | 类型 | 默认 | 是什么 |
| --- | --- | --- | --- |
| `vault.mode` | `file`、`passphrase`、`aws-kms`、`gcp-kms`、`transit` | `file` | 根密钥从哪来，启动时读取：secrets 目录里的文件（`file`）；owner 输入的口令（`passphrase`，重启后保管库封存直到输入——监控与 SSH 通道照常）；或代为解开它的保管方（`aws-kms`、`gcp-kms`、`transit`）。轮换用 `beacon key rotate` |
| `vault.dir` | 路径 | `<data_dir>/secrets` | 密钥文件所在目录，首次启动创建，仅属主可读 |
| `vault.aws_kms.region` | 字符串 | — | AWS KMS 模式：密钥所在区域 |
| `vault.aws_kms.key_id` | 字符串 | — | 密钥的 id 或别名（`alias/beacon`） |
| `vault.aws_kms.endpoint` | URL | — | 自建 KMS，测试用；留空是 AWS 自己的 |
| `vault.aws_kms.access_key_file` | 路径 | — | 访问密钥，文件方式 |
| `vault.aws_kms.secret_key_file` | 路径 | — | 私有密钥，文件方式 |
| `vault.aws_kms.session_token_file` | 路径 | — | 会话令牌，部署用临时凭据时的做法 |
| `vault.gcp_kms.key_name` | 字符串 | — | Cloud KMS 模式：密钥的完整资源名 `projects/…/locations/…/keyRings/…/cryptoKeys/…` |
| `vault.gcp_kms.endpoint` | URL | — | 自建 KMS，测试用；留空是 Google 自己的 |
| `vault.gcp_kms.token_file` | 路径 | — | Bearer 令牌，文件方式 |
| `vault.transit.address` | URL | — | Vault 或 OpenBao transit 模式：它的地址 |
| `vault.transit.key_name` | 字符串 | — | transit 密钥的名字 |
| `vault.transit.token_file` | 路径 | — | 令牌，文件方式 |

## 规则与路由 {#rules-and-routing}

| 键 | 类型 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `rules.token_file` | 路径 | 必填 | 规则引擎（vmalert 或 Prometheus）向网关的 `/api/v2/alerts` 发告警时所用 bearer 令牌，与发给 Alertmanager 的相同。至少 24 个字符 |

路由做的是 Alertmanager 的 `route` 和 `inhibit_rules` 的事。`match`、`source` 和 `target` 里的标签值是匹配整个值的正则表达式。

| 键 | 类型 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `routing.group_by` | 标签名列表 | `[alertname, service]` | 告警组共享的标签 |
| `routing.group_wait` | 时长 | `30s` | 一组的第一条告警之后，先等更多告警再发卡片 |
| `routing.group_interval` | 时长 | `2m` | 之后至多按这个频率更新卡片 |
| `routing.resolve_timeout` | 时长 | `5m` | 规则引擎不再发送的告警在这之后转为恢复 |
| `routing.routes.match` | 标签到正则的映射 | — | 路由匹配什么；第一个匹配的路由改变分组 |
| `routing.routes.group_by` | 标签名列表 | — | 该路由自己的分组，多个服务合一张卡 |
| `routing.inhibit.source` | 标签到正则的映射 | — | 源告警触发期间 |
| `routing.inhibit.target` | 标签到正则的映射 | — | `equal` 标签取值相同的目标告警被静默 |
| `routing.inhibit.equal` | 标签名列表 | — | 必须取值相同的标签 |

## 网关 {#the-gateway}

机器的门：检测点和主机用 `beacon enroll` 发的令牌写入并拉取检测任务，任务推心跳，规则引擎和 Flux 发告警，`metrics:read` 令牌读取（[REST API](api.md#the-gateway)）。

| 键 | 类型 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `gateway.listen` | 地址 | `:17006` | 机器接入的地址。留空关闭网关 |
| `gateway.targets_dir` | 路径 | `/etc/beacon/targets` | 检测目标文件（`all-*.yaml`、`<location>-*.yaml`），首次启动和 SIGHUP 时导入数据库 |
| `gateway.modules_file` | 路径 | `gateway.targets_dir` 下的 `modules.yml` | 检测模块，同样导入 |
| `victoriametrics.url` | URL | 开网关即必填 | 检测结果所在处：网关写入，卡片从这里取每个检测点的结果 |

## SSH 通道 {#the-ssh-channel}

设置了 `ssh.ca_key_file` 通道才开启（[给 Agent 一台机器](../guide/machines.md)）。两把钥匙都是 OpenSSH 私钥：`ssh-keygen -t ed25519`。

| 键 | 类型 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `ssh.listen` | 地址 | `:17002` | Agent 连接通道的地址 |
| `ssh.public_address` | host:port | — | 外部到达的地址，用于 Beacon 发给 Agent 的 ssh 配置 |
| `ssh.host_key_file` | 路径 | 开通道即必填 | 通道自己的 host key，不是机器的 |
| `ssh.ca_key_file` | 路径 | — | 为每次连机器签短期证书的钥匙；设置它即开启通道。必须与 host key 是不同的钥匙 |
| `ssh.source_addresses` | CIDR 段列表 | — | 设置后，证书只从这些地址可用 |
| `ssh.keep` | 时长 | `4320h`（180 天） | 会话及其录像保留多久。至少 `24h` |

## 链接、页面与品牌 {#links-pages-and-branding}

| 键 | 类型 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `links.grafana_service` | URL 模板 | — | 告警卡片链到的仪表盘；填入 `{service}`，告警自己的注解优先 |
| `links.runbook` | URL 模板 | — | 操作手册；填入 `{alertname}`，告警自己的注解优先 |
| `pages.link_ttl` | 时长 | `720h`（30 天） | 指向控制台页面（告警时间线、会话回放）的签名链接有效期。介于 `1h` 与 366 天之间 |
| `branding.title` | 字符串 | `Beacon` | 没有状态页时 `/` 上那页对部署的称呼。一行，至多 80 个字符 |
| `branding.powered_by` | 布尔 | `true` | `false` 则公共页脚不写 "Powered by Beacon, open source" |
| `oauth.client_id_documents` | 布尔 | `true` | 按 `client_id` 所指名的元数据文档接纳 MCP 客户端（Claude Code 和 Codex 都是），Beacon 需从互联网取它。Beacon 上不了网时设 `false`：客户端改为自行注册 |

## 飞书 {#feishu}

自建应用（卡片、按钮回调、加急）和群机器人（[配置飞书应用](../integrations/feishu.md)）。

| 键 | 类型 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `feishu.app_id_file` | 路径 | — | 应用的 App ID；与 `feishu.app_secret_file`、`feishu.chat_id` 同进同出 |
| `feishu.app_secret_file` | 路径 | — | 应用的 App Secret |
| `feishu.domain` | `feishu` 或 `lark` | `feishu` | open.feishu.cn 或 open.larksuite.com |
| `feishu.callback` | `websocket` 或 `http` | `websocket` | 按钮回调怎么到达：飞书长连接，或 POST 到 `/hooks/feishu` |
| `feishu.verification_token_file` | 路径 | — | Verification Token，仅 `http` 回调 |
| `feishu.encrypt_key_file` | 路径 | — | Encrypt Key，仅开启加密的 `http` 回调 |
| `feishu.chat_id` | 字符串 | 配了应用即必填 | 告警卡片发到的群 |
| `feishu.on_call` | 列表 | 配了应用即必填 | 只有他们能在卡片上操作，加急也是加给他们 |
| `feishu.on_call.open_id` | 字符串 | — | 某个人的 `ou_…` id |
| `feishu.on_call.name` | 字符串 | — | 姓名，显示在卡片上 |
| `feishu.monthly_budget` | 整数 | `10000` | 应用每月的 API 调用数，飞书免费版的额度。至少 100 |
| `feishu.webhook_url_file` | 路径 | — | 群机器人：纯文本消息，不占 API 配额。info 级告警、超额提醒和回显走它 |
| `feishu.webhook_secret_file` | 路径 | — | 机器人对请求签名时用 |
| `feishu.webhook_keyword` | 字符串 | `Beacon` | 机器人的安全设置是关键词时填 |

## Pushover、Bark 与 healthchecks.io {#pushover-bark-and-healthchecksio}

| 键 | 类型 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `pushover.user_key_file` | 路径 | — | 紧急推送发给谁；与 `pushover.token_file` 同进同出 |
| `pushover.token_file` | 路径 | — | Pushover 应用的 token |
| `pushover.retry` | 时长 | `60s` | 紧急告警在确认前按这个频率重复。至少 `30s` |
| `pushover.expire` | 时长 | `3h` | 重复多久后放弃。至多 `3h` |
| `pushover.api` | URL | `https://api.pushover.net` | Pushover 的 API，供代理或测试用 |
| `bark.url_file` | 路径 | — | 带响铃紧急级别的 iOS 推送：`https://api.day.app/<key>`，或自建服务 |
| `healthchecks.bridge_url_file` | 路径 | — | 桥能送出告警时每分钟 ping 一次，不能时 ping `/fail`：基础设施之外的死人开关（healthchecks.io） |

## 升级与确认 {#escalation-and-acknowledgements}

各级告警怎么到达人。时长为零即关闭该步。整个 `escalation` 段缺省时，Beacon 按已配置的通道择优：配了飞书应用就用应用，只配了群机器人就用机器人，都没有则 `none`（告警只记录，API 和 MCP 照样可看）；critical 配了应用 10 分钟后加急、配了 Pushover 15 分钟后推手机；info 级在应用之外还有机器人时走机器人。

| 键 | 默认值 | 含义 |
| --- | --- | --- |
| `escalation.critical.channel` | 已配置的最优通道 | `app`（飞书卡片）、`webhook`（群机器人）或 `none`（只记录） |
| `escalation.critical.buzz` | 配了应用 `10m`，否则关 | 这段时间后在飞书上加急值班的人 |
| `escalation.critical.pushover` | 配了 Pushover `15m`，否则关 | 这段时间后向手机发紧急推送 |
| `escalation.critical.bark` | 关 | 这段时间后发 Bark 推送 |
| `escalation.warning.channel` | 已配置的最优通道 | 同 `escalation.critical.channel` |
| `escalation.warning.buzz` | 关 | 同 `escalation.critical.buzz` |
| `escalation.warning.pushover` | 关 | 同 `escalation.critical.pushover` |
| `escalation.warning.bark` | 关 | 同 `escalation.critical.bark` |
| `escalation.info.channel` | 应用；应用与机器人都有时走机器人 | 低级别告警不必占应用配额 |
| `escalation.info.buzz` | 关 | 同 `escalation.critical.buzz` |
| `escalation.info.pushover` | 关 | 同 `escalation.critical.pushover` |
| `escalation.info.bark` | 关 | 同 `escalation.critical.bark` |
| `ack_ttl` | `4h` | 确认经过这段时间后失效，升级重新开始。至少 `10m` |

## Agent hook {#agent-hooks}

新的告警组首次通知时唤醒 Agent 的 hook，告诉它证据在哪（[被 webhook 唤醒的 Agent](../integrations/webhooks.md)）。Beacon 不调用任何模型：怎么处理是 Agent 的事。列表里的每项是 `kind: webhook`（签名的 JSON POST）或 `kind: github`（触发 workflow）。

| 键 | 种类 | 含义 |
| --- | --- | --- |
| `agents.name` | 两者 | hook 的名字：小写字母、数字、点、连字符和下划线 |
| `agents.kind` | 两者 | `webhook` 或 `github` |
| `agents.severities` | 两者 | 哪些级别会唤醒它。默认 `[critical]` |
| `agents.service` | 两者 | 匹配整个 `service` 标签的正则；留空匹配任何服务 |
| `agents.resolved` | 两者 | 组恢复时也调用 |
| `agents.resolved_after` | 两者 | 恢复调用等待多久，让 Agent 上一次运行先收尾留言。默认 `1m`，至多 `5m`；`0` 立即发送 |
| `agents.url` | webhook | JSON 负载 POST 到的地址 |
| `agents.secret_file` | webhook | HMAC-SHA256 签名密钥，以 `X-Beacon-Signature` 发送；至少 24 个字符。`secret_file` 或 `headers_file` 必须有其一，Agent 才能分辨 Beacon 的调用和伪造 |
| `agents.headers_file` | webhook | 每次调用都带的 `Name: value` 行，供校验自己那套密钥的接收方；有了它 `secret_file` 可省 |
| `agents.repository` | github | 仓库，`owner/name` |
| `agents.workflow` | github | workflow 的文件名（如 `oncall.yml`）或 id |
| `agents.ref` | github | 默认 `main` |
| `agents.token_file` | github | 能触发该 workflow 的令牌（仓库的 Actions 读写权限） |
| `agents.api` | github | 默认 `https://api.github.com`；GitHub Enterprise 填自己的 |

## 变更事件 {#change-events}

| 键 | 类型 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `changes.keep` | 时长 | `720h`（30 天） | 发送方发到网关的变更事件（部署、 Flux 调和）保留多久。至少 `24h` |
