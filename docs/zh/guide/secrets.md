---
sourceHash: sha256-3f46e641
---

# 管理 Agent 的密钥

读完这一页，你的一个 Agent 能找到某个凭据、拿到一次授权，此后工作需要时随时取用——每次取用都有记录——而你也已经定好它到底能申请什么。本页写给运行 Beacon 的人和被放进来的 Agent；背后的设计见[密钥的设计](../about/secrets.md)，每个配置项见[设置](../reference/settings.md)，级别、资格与 scope 也收录在[权限参考](../reference/permissions.md#the-secrets-levels-entitlements-and-actions)里。

你需要 Beacon 在运行，并用一个带 `secrets:admin` 的账号（默认是 owner）来定级别和资格；Agent 需要 secrets scope 之一和一份资格。

## 密钥是什么 {#what-a-secret-is}

密钥放在**库**（对应 1Password 的 vault）里，库里有**条目**；条目有带类型的**字段**——`concealed`、`string`、`email`、`url`、`otp`、`date`、`file`——以及 `username`、`password`、`notes` 等用途。值的每次修改都是一个新**版本**，按保留期留存，所以某个时刻的值可以回看。条目带**标签**，分层、小写（`env/prod`、`service/dns`、`source/1password`）；标签会改变权限的含义，所以改标签需要 `manage`。

Agent 用引用来指名一个密钥，方式与 `op://` 相同：

```text
beacon://Infra/dns-example-com/password
beacon://Shared/api tokens/example-token/credential
```

各段依次是库、条目、可选的小节、字段；名字里可以是字母、数字、`-_.` 和空格，其余情况用 ID 引用。带空格的字段名照原样写。

概要——标题、类别、标签、网址、字段名——在数据库里可读；详情用库密钥加密（见[设计](../about/secrets.md#encryption-and-signatures)）。

## 谁能申请什么 {#who-may-ask-for-what}

两件事共同决定，取更严的：

| 级别 | 申请这个条目要什么 |
| --- | --- |
| `auto` | 有资格即可：申请即批准 |
| `review` | 走审核链——规则、审核 Agent、人，按配置来 |
| `person` | 只能由人批准 |
| `never` | 不交给任何 Agent；人仍可在控制台查看 |

级别放在库、标签或条目上；没写的时候，取库默认值和条目所有标签中最严的；条目上明确写的级别是管理者的决定，控制台会标出「覆盖了默认值」。

**资格**决定 Agent 连能申请什么。Agent 接入时没有任何资格：默认拒绝。一份资格写明它的主体、资源和动作：

```yaml
name: oncall-dns
description: 值班 Agent 可以申请 DNS 凭据
principals:
  agents: [dns-bot]
  agent_tags: [role/on-call]
  agents_of: [alice]
resources:
  collections: [Infra]
  tags: ["service/dns/*", "provider/example"]
actions: [describe, read]
approval: review
settings:
  request.default_duration: 30d
  request.max_uses: unlimited
reviewers: [rule:dns-on-alert, agent:review-bot, people:operator]
```

`deny` 资格先于一切允许；资格里的 `approval` 是下限不是上限；Agent 能拿到的不超过其所有者在该库的权限。创建 Agent 时——控制台或 OAuth 授权页——所有者从资格模板（无、值班、脚本、审核、自定义）里选，并预览它能触到什么：*N 条：auto X、review Y、person Z*。

## 申请，然后拿到授权 {#ask-and-be-granted}

Agent 调用 `request_secret`，写明要什么、为什么——原因必填，默认至少十个字符：

```
request_secret(items: ["beacon://Infra/dns-example-com"], actions: [read],
               reason: "renew the DNS record for status.example.com", uses: 3, duration: 30d)
```

一次申请跨了几个级别就被拆开：`auto` 的部分立即生效，其余进审核链，一次应答说清谁是谁。Agent 可以在调用里 `wait`，也可以在自己的 hook 上听结果（`secret.granted`、`secret.denied`），或者稍后用 `get_request` 查。审核来自三类审核者，顺序由配置定——Beacon 内置的规则（告警中的服务匹配条目的 `service/` 标签、申请人所在分组、时间窗、原因模式）、Beacon 之外像值班 Agent 一样被唤醒的审核 Agent、在卡片或控制台上答复的人——一个级别需要几个批准、某层超时后怎么办，全是配置。一个拒绝就结束申请；申请人永远不能审自己。

之后生成**授权**：条目、字段、动作、次数和期限——次数和期限都可以不限——批准人，以及原因。授权活到用完、到期、被吊销，或（绑定告警时）告警恢复且宽限期过去。

## 取值 {#take-a-value}

`get_secret` 凭授权返回某个字段的值，每次都要原因——默认至少两个字符：

```
get_secret(ref: "beacon://Infra/dns-example-com/password", reason: "renewing the record")
```

`otp` 字段返回当前验证码和剩余秒数；种子只在配置允许时才给 Agent。取用先写记录——记到 Agent、它所用的人和授权、以及上下文上——然后才返回值；记录写不进去，值就不回来。每个 Agent 有限流（默认每分钟 600 次）；不寻常的取用——第一次用某个库、超过一周均值五倍、被拒次数突增——默认会告警。

`list_grants` 列出 Agent 持有的授权，`renew_grant` 在到期前把它送回审核链，`release_grant` 提前释放。条目改值时，持有者收到 `secret.changed`，凭同一份授权取新版本；策略收紧时，既有授权怎么办由配置决定。

## 不见值的用法 {#using-a-secret-without-seeing-it}

对于根本不该经过 Agent 上下文的值，`use` 动作在不暴露值的前提下完成：

- **`beacon run -agent 名称 -reason "…" -env-file 文件 -- <命令>`** 读取写满引用的 `.env`，把值注入子进程，就像 `op run`；值进的是进程，不是对话。没有覆盖某条引用的授权时，`-wait` 会申请一份并等到那么久。REST `POST /api/v1/secrets/use` 是这条路径给 CLI 用的；MCP 没有回值的 use 工具。
- **带 `secrets` 的 `run_command`** 把值作为环境变量传入机器上的命令，并在输出、录像和文字稿中打码——值本身和它的 base64、URL 编码、十六进制形式都盖住。只在条目绑定的机器上可用。
- **`http_request`** 把密钥引用换成值后由 Beacon 自己发出请求，只发往条目绑定的 `https://` 主机，返回的响应已打码：

  ```text
  Authorization: Bearer {{secret.beacon://Engineering/api-token/credential}}
  ```
- **文件下发**把条目的值放到它指名的机器路径上，带属主和权限，值变化后重新下发；`read_file` 读到的是打码后的内容。

打码防的是无意泄露，不是存心：命令是 Agent 自己写的，所以这些路径用于本来就持有该凭据的机器。

## 保持密钥健康 {#keeping-secrets-healthy}

条目可以设轮换周期；Beacon 也能按规格在服务端生成新值——作为新版本，返回版本号，绝不返回值——于是 Agent 可以全程不见值地完成一次轮换。控制台报告弱口令、重复口令和口令年龄，全部本地计算；HIBP 泄露库查询默认关闭，因为它会向外发请求。长期没人用的授权会被标出，之后怎么办（标出、通知、释放）是配置。

## 命令 {#the-commands}

| 命令 | 做什么 |
| --- | --- |
| `beacon secret import` | 从 1Password、Chrome、Bitwarden 或 CSV 导入（见 [1Password](../integrations/1password.md)、[Chrome](../integrations/chrome.md)）。在 Beacon 主机上经管理套接字解析；没有 REST 或 MCP 导入入口 |
| `beacon secret ls` \| `show` \| `set` | 列出库和条目；看条目概要；写入条目 |
| `beacon secret reveal` | 读值，带人查看值时的重新验证窗口 |
| `beacon secret tag` \| `level` | 改条目的标签或级别（需要 `manage`） |
| `beacon secret archive` \| `rm` | 归档条目（回收站保留 30 天）或删除 |
| `beacon secret generate` | 按规格生成新值，作为新版本 |
| `beacon run` | `-agent`、`-reason`、`-env-file`、`-wait`：把引用注入子进程 |
| `beacon settings get` \| `set` \| `reset` \| `history` \| `revert` VERSION | 读写[设置](../reference/settings.md)页的配置 |
| `beacon key rotate` \| `seal` \| `unseal` | 轮换根密钥；封存与启封密钥库（封存是密钥这边的总闸） |
| `beacon backup now` \| `ls` \| `restore` \| `drill` | 见[备份与恢复](../self-hosting/backup.md) |

资格和授权没有单独的 CLI：`put_entitlement`、`list_entitlements`、`delete_entitlement`、`list_grants` 和 `revoke_grant` 走 REST 和 MCP。

`beacon audit verify` 校验审计链，`beacon storage check` 查数据库健康（见[命令行](../reference/cli.md)）。

## 接下来 {#what-next}

- [密钥的设计](../about/secrets.md)：级别与授权背后的道理。
- [从 1Password 导入](../integrations/1password.md)与[从 Chrome 导入](../integrations/chrome.md)：把库填起来。
- [设置](../reference/settings.md)：本页点到的每个默认值。
- [安全模型](../self-hosting/security.md)：威胁模型与根密钥的启封。
