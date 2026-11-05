---
sourceHash: sha256-2a4183f1
---

# 设置

Beacon 运行所依据、又能调节的每个值都是配置项：存在数据库里，开箱即有合理默认值，带版本、记着谁在何时为什么改的，可以回滚。本页列出全部配置项和默认值。`*_file` 密钥和端口这类——数据库打开之前就要用的——仍在 [config.yaml](config.md)。

构建会核对本页：它读取 `internal/settings` 里每一次 `register`，每个键都必须中英文成对出现在反引号里，否则站点构建失败。

## 配置放在哪里 {#where-settings-live}

在数据库里，不在文件里：运行时改、立即生效，每次修改都是一个版本，带修改人、时间、原因和改动前后的值——和[目录](catalogue.md)同样的待遇。改配置需要 `secrets:admin`；owner 默认持有，也可以授给某个 Agent，它的每次改动同样记录。控制台上每个配置项都显示当前值、默认值、在哪一级被覆盖、谁在何时改的。

`beacon settings get [KEY]`、`beacon settings set KEY VALUE -reason "…"`、`beacon settings reset KEY`、`beacon settings history` 和 `beacon settings revert VERSION` 在命令行做同样的事。值按存的样子写 JSON。REST 和 MCP 用 `get_config` 与 `put_config`（`settings` 类）。每次都有审计条目并回显到告警群。

## 逐级覆盖 {#how-they-override}

`system → collection → tag → entitlement → request`：越具体的越优先，申请只能在上一级给出的上限之内取值。条目级别不走这条链，单独取最严（见[权限](permissions.md#the-secrets-levels-entitlements-and-actions)）。同一条目命中多个标签、各自覆盖了同一项时，取更严的值：次数和期限取小的，超时后果取更保守的——依次是 deny、expire、next、wait、approve。

## 申请与授权 {#requests-and-grants}

| 键 | 默认值 | 可选与说明 |
| --- | --- | --- |
| `request.default_uses` | `unlimited` | 整数或 `unlimited`；申请没写次数时用它 |
| `request.max_uses` | `unlimited` | 申请可以要到的上限 |
| `request.default_duration` | `auto`：`never`；`review`：`30d`；`person`：`7d` | 时长或 `never` |
| `request.max_duration` | `unlimited` | 上限 |
| `request.reason_min_length` | `10` | `0` 表示不要求原因 |
| `use.reason` | `required`，最短 `2` | `required` / `optional`；可选时仍自动记录上下文 |
| `request.pending_ttl` | `24h` | 没人处理的申请可以等多久 |
| `request.on_pending_expiry` | `expire` | `expire` / `deny` / `approve` |
| `grant.renewal` | `auto` 级 `auto`；其余 `request` | `auto` / `request` / `none` |
| `grant.expiry_notice` | 提前 `3d` | 短于一天的授权不提醒 |
| `grant.on_policy_tightened` | `revoke` | `revoke` / `keep_until_expiry` / `notify`；放宽不影响既有授权。行还在时，取用和续期仍会重查策略 |
| `grant.on_owner_access_lost` | `revoke` | 同上 |
| `grant.on_item_changed` | `notify_holders` | `notify_holders` / `none`；新版本不需要新授权 |
| `grant.alert_grace` | `15m` | 绑定告警的授权在告警恢复后还能用多久 |
| `grant.unused_after` | `30d` | 活着的授权闲置多久后，卫生扫描会处理 |
| `grant.on_unused` | `mark` | `mark` / `notify` / `auto_release` |
| `agents.default_entitlements` | *(空)* | 没有另行选择时 Agent 得到的资格模板 |

## 审核 {#review}

| 键 | 默认值 | 可选与说明 |
| --- | --- | --- |
| `review.chain` | `[rules, agents, people]` | 三层的任意组合和顺序 |
| `review.agent.may_approve` | `[review]` | 审核 Agent 能批的级别；`person` 按定义只能人批 |
| `review.agent.max_uses` | `unlimited` | 审核 Agent 能给出的上限 |
| `review.agent.max_duration` | `unlimited` | 同上 |
| `review.agent.timeout` | `10m` | |
| `review.agent.on_timeout` | `next` | `next` / `deny` / `approve` / `wait`；选 `approve` 时控制台会提示风险 |
| `review.people.timeout` | `24h` | |
| `review.people.on_timeout` | `expire` | `expire` / `deny` / `approve` |
| `review.approvals_required` | `1` | 按级别分别设；`2` 需要两个，比如审核 Agent 加人 |
| `review.separation` | `requester` | `requester` / `owner`；`owner` 连「审核人的所有者是申请人的所有者」也禁止 |
| `review.notify_decisions` | 告警群 | 审核 Agent 的决定推送到哪里 |

## 取用、限流与异常 {#uses-rate-limits-and-anomalies}

| 键 | 默认值 | 可选与说明 |
| --- | --- | --- |
| `rate.per_agent_per_minute` | `600` | 每个 Agent 每分钟的取用 |
| `rate.on_exceed` | `throttle` | `throttle` / `alert` |
| `anomaly.rules` | 下面五条全开、只通知 | 第一次用某个库；超过 7 天均值五倍；被拒次数突增；签名校验失败；密钥库处于封存。每条可关、可调阈值 |
| `anomaly.on_detect` | `notify` | `notify` / `suspend_agent` |
| `otp.seed_to_agents` | `false` | `get_secret` 读 `otp` 字段时给不给种子，还是只给验证码 |
| `reveal.reauth_window` | `5m` | 人输过密码后多久内可以继续查看值 |
| `scrub.encodings` | 原值、base64、URL 编码、十六进制 | 输出里打码值的哪些编码形式；`plain` 始终在内 |
| `http.allow_private` | `false` | `http_request` 可否发往私网地址 |
| `http.follow_redirects` | `same_host` | `none` / `same_host` / `any_bound` |
| `file.on_change` | `deliver` | `deliver` / `next_window` / `notify`——值变化后已下发的文件怎么办 |
| `sealed.on_request` | `refuse` | `refuse`（返回 `secrets.sealed`）/ `queue` |

## 卫生 {#hygiene}

| 键 | 默认值 | 可选与说明 |
| --- | --- | --- |
| `hygiene.hibp_check` | `false` | 健康报告是否再问泄漏语料（HIBP 的 k-匿名接口）；会把每个口令哈希的前五个字符发到库外 |
| `hygiene.weak_min_length` | `8` | 短于这个长度，报告就判为弱口令 |
| `hygiene.password_age` | `90d` | 超过这个年龄，报告就判为过旧 |

## 保留期 {#retention}

| 键 | 默认值 | 可选与说明 |
| --- | --- | --- |
| `retention.audit` | `365d` | 可按事件类型分别设 |
| `retention.uses` | `365d` | |
| `retention.requests` | `365d` | |
| `retention.item_versions` | `365d`，且始终保留最近 `10` 个 | |
| `retention.trash` | `30d` | |
| `audit.checkpoint_interval` | `24h` | 多久写一次链的签名检查点 |
| `audit.checkpoint_publish` | 进每份备份；配了聊天则同时发 | |
| `audit.export` | *(无)* | `syslog` / `s3`，JSONL |

## 导入 {#import}

| 键 | 默认值 | 可选与说明 |
| --- | --- | --- |
| `import.levels.by_source` | 1Password 的 `Personal` 和 `Private` → `person`；Chrome → `person`；其余 vault → `review` | dry-run 时可按库改 |
| `import.levels.by_category` | 证件和金融类 → `never` | 类别清单见[导入](../integrations/1password.md) |
| `import.on_conflict` | `skip` | `new_version` / `skip` / `keep_both`。没有 `ask`：需要人拍板的导入会被拒绝，无头跑不会卡住 |
| `attachments.max_size` | `10MB` | 10,485,760 字节 |

## 备份与存储 {#backup-and-storage}

同一类配置项；更长的说明在[备份与恢复](../self-hosting/backup.md)和 [PostgreSQL](../self-hosting/postgres.md)。数据库驱动及其文件、根密钥的启封和备份接收者的公钥在数据库打开之前就要读，所以放在 [config.yaml](config.md) 和 secrets 目录里。

| 键 | 默认值 | 可选与说明 |
| --- | --- | --- |
| `backup.schedule.database` | `1h` | 多久备份一次数据库 |
| `backup.schedule.recordings` | `24h` | 多久备份一次录像和配置目录 |
| `backup.retention.hourly` | `24` | 保留的按小时备份 |
| `backup.retention.daily` | `14` | 保留的按日备份 |
| `backup.retention.weekly` | `8` | 保留的按周备份 |
| `backup.retention.monthly` | `12` | 保留的按月备份 |
| `backup.verify.integrity` | `true` | 加密前检查数据库完整性 |
| `backup.verify.readback` | `true` | 上传后再读回来核对 |
| `backup.retries` | `3` | 失败重试几次 |
| `backup.on_failure` | `alert` | `alert` / `alert_pause_use` |
| `backup.heartbeat` | `2h` | 心跳多久没看到备份就算迟到 |
| `backup.pg_dump_daily` | `false` | 用 PostgreSQL 时，加密备份里是否再放一份每日 `pg_dump` |
| `storage.lease.renew` | `5s` | 主机多久续一次租约 |
| `storage.lease.expires` | `15s` | 不续约的租约能活多久，也就是故障切换大概要多久 |

指标：`beacon_secret_uses_total{collection,agent,action}`、`beacon_secret_requests_total{decision,decider_kind}`、`beacon_secret_grants_active`、`beacon_secret_grant_expiring`（见[指标](metrics.md)）。

## 和方案不一致的地方 {#where-the-shipped-code-differs-from-the-design}

- **`import.on_conflict`**：方案默认是 `ask`。代码没有 `ask`；默认是 `skip`，可选只有 `new_version`、`skip`、`keep_both`。
- **导入入口**：`beacon secret import` 走数据目录里的管理 socket，文件在跑 Beacon 的那台主机上解析。没有 REST 或 MCP 的导入：Agent 看不到导出文件。
- **卫生相关键**（`grant.unused_after`、`grant.on_unused`、`hygiene.hibp_check`、`hygiene.weak_min_length`、`hygiene.password_age`）在代码和本页里；方案第 9.3 节的表没有它们。
- **密钥这边的总闸**是封存（`beacon key seal`，`ErrSealed`）。没有单独的密钥 `Pause()`；SSH 通道的暂停开关仍在通道上。
- **高可用租约键**：真正跑的是 `config.yaml` 里的 `ha.renew` 和 `ha.expire`。设置里也登记了 `storage.lease.renew` 和 `storage.lease.expires`，数字相同。集成之前两套都在。
- **备份日程和失败**：真正跑的是 `config.yaml` 里的 `backup.schedule.files`、`backup.schedule.files_full` 和 `backup.on_failure`（只有 `alert`；配 `alert_and_pause` 会被拒绝）。设置里登记了 `backup.schedule.recordings`，以及 `backup.on_failure` 的 `alert` / `alert_pause_use`。
