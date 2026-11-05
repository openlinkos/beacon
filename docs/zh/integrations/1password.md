---
sourceHash: sha256-e235833c
---

# 从 1Password 导入

读完这一页，你的 1Password 各个 vault 就是 Beacon 的库，条目、字段、附件和一次性验证码都在，每条都有一个说明哪个 Agent 能申请它的级别。命令见[管理 Agent 的密钥](../guide/secrets.md)；本页只讲导入本身。

1Password 的导出是明文：里面的每一样东西都不加遮掩。`beacon secret import` 走数据目录里的管理 socket，文件在跑 Beacon 的那台主机上解析——把导出拷到那里，在容器里跑命令，然后删掉。控制台的导入向导在浏览器里收文件，只在内存里解析，从不落盘。没有 REST 或 MCP 的导入：Agent 看不到导出文件。导完删掉导出文件——两条路都会提醒你。

## 导出之前 {#before-you-export}

1Password 提供三条出来的路，三条都通向 Beacon：

| 来源 | 里面有什么 | 什么时候用 |
| --- | --- | --- |
| **1PUX**（推荐） | 全部：账号、vault、条目、各类型的小节和字段、笔记、附件、密码历史、归档状态 | 任何时候，条目超过约 300 条时务必 |
| **CSV** | 只有 Login 和 Password 条目 | 拿一小部分先试试 |
| **`op` 命令行** | 账号能读到的，实时 | vault 很大但只要其中一些条目；注意下面的速率限制 |

1PUX 从桌面应用导出（账户设置 → 导出，一次一个账户），CSV 从条目列表导出；`op` 需要已登录的会话。每种导出的详细做法见 1Password 自己的文档。

## 什么对应什么 {#what-maps-to-what}

| 1Password | Beacon |
| --- | --- |
| Vault（`P`、`E`、`U` 一视同仁） | 同名的库——`Shared`、`Engineering`、`Personal` 原名保留 |
| 条目 | 同标题同网址的条目，来源记为 `1password:<uuid>` |
| 类别（`categoryUuid`） | 同类别：Login、Password、API Credential、Database、Server、SSH Key、Secure Note、Email Account、Wireless Router、Software License |
| 登录字段 | 按用途拆成 `username` 和 `password` |
| 小节与字段 | 小节保留；字段类型一一对应——`concealed`、`string`、`email`、`url`、`totp`、`date`、`monthYear`、`phone`、`sshKey` |
| 笔记、`passwordHistory`、`files/` | 笔记照搬；历史成为更早的版本；文件成为附件 |
| 标签 | 原样保留（本来就用 `/` 分层），统一小写，另加 `source/1password` |
| `state: archived` | 归档 |

导入过程中不打印任何东西——没有值，没有笔记——只有数量。

## 导入后的级别 {#the-levels-after-import}

每条条目到位时都带一个级别，默认值是斟酌过的（见[设置](../reference/settings.md)）：

- 名为 **Personal** 或 **Private** 的 vault 导入为 `person`：这些是你自己的，Agent 要用得问你。
- 其余 vault 一律导入为 `review`。
- 证件和金融类——Credit Card、Bank Account、Identity、Passport、Driver License、Social Security Number、Medical Record、Membership、Reward Program——导入为 `never`：无论在哪个 vault，它们都不该到 Agent 手里。

dry-run 会列出每个库和类别将得到什么，你可以在正式提交前按库改级别；冲突规则（`import.on_conflict`，默认 `skip`；也可 `new_version` 或 `keep_both`）决定「看起来已有一条」的条目怎么办——网址规范化后相同、用户名相同即视为同一条。没有 `ask`：无头导入不会等人拍板。

## 执行导入 {#run-the-import}

```bash
beacon secret import 1pux --file export.1pux --dry-run
# 每个库的条数、类别分布、将要设定的级别、冲突
beacon secret import 1pux --file export.1pux
```

从 `op` 命令行导入时，Beacon 自己去读 `op vault list`、`op item list --vault` 和 `op item get --reveal --format json`；每个条目约一到三次请求，个人账号每天约限 1,000 次读取，所以超过约 300 条请改走 1PUX。`beacon secret import csv --file export.csv` 读取 CSV 导出。

导入结束后，控制台会列出待办：逐库确认级别、核对 `never` 的建议、给 Agent 配资格。然后删掉导出文件。

## 引用 {#references}

按 1Password 引用语法写的工作流或 `.env`，换个前缀就搬过来：

```text
op://Engineering/api-token/credential        →   beacon://Engineering/api-token/credential
op://Shared/dns example.com/password         →   beacon://Shared/dns example.com/password
```

`beacon://` 保持同样的形状——库、条目、可选小节、字段——所以读 `op://` 引用的脚本只需要换前缀（见[管理 Agent 的密钥](../guide/secrets.md#what-a-secret-is)）。
