---
sourceHash: sha256-623a74c1
---

# 从 Chrome 导入

读完这一页，Chrome 替你保存的密码就成了 Beacon 一个库里的条目，级别让它们仍然是你自己的。命令见[管理 Agent 的密钥](../guide/secrets.md)；本页只讲导入本身。

Chrome 的导出是明文，而且是你全部已存密码在一个文件里。`beacon secret import` 走管理 socket，在跑 Beacon 的那台主机上解析；控制台的导入向导在浏览器里收文件，只在内存里解析，从不落盘。没有 REST 或 MCP 的导入。导完删掉导出文件。

## 从 Chrome 导出 {#export-from-chrome}

Chrome 按配置保存密码，在 `chrome://settings/passwords` 导出为 CSV（提供保存密码 → 已保存的密码 → ⋮ → 导出密码；Edge 和 Brave 在各自的设置页里有同样的导出）。文件第一行是表头，Beacon 按表头识别、不按列的位置：

```csv
name,url,username,password,note
```

每个保存的站点一行；一个站点的「关联」域名各占一行。Chrome 不导出通行密钥——它们留在原地。

## 什么对应什么 {#what-maps-to-what}

| Chrome | Beacon |
| --- | --- |
| 整个文件 | 导入时由你命名的一个库——比如 `Browser`——与其他来源导入的库分开 |
| 一行 | 一个条目：`name` 作标题、`url` 作网址之一、`username` 作 `username` 字段、`password` 作 `password` 字段、`note` 作笔记 |
| 用户名与密码都相同、域名不同的几行 | 合并为一个带多个网址的条目，就像 Chrome 自己分组那样 |
| （没有标签） | 每条加 `source/chrome` |

库导入为你给的级别——默认 `person`，和 Chrome 本身一样：存在浏览器里，由你来读（见[设置](../reference/settings.md)）。

## 执行导入 {#run-the-import}

```bash
beacon secret import chrome --file "Chrome Passwords.csv" --collection Browser --dry-run
beacon secret import chrome --file "Chrome Passwords.csv" --collection Browser
```

dry-run 会报告与既有条目的冲突——网址规范化后相同、用户名相同即视为同一条——`import.on_conflict`（默认 `skip`；也可 `new_version` 或 `keep_both`）决定这些怎么办。没有 `ask`。导入过程中除了数量什么都不打印。

## 导入之后 {#after-the-import}

控制台会列出待办：确认这个库的级别，给 Agent 配资格——常见的做法是把浏览器库留在 `person`，只在你选择的地方放 Agent 进来。然后删掉 CSV，再清一清它所在的下载文件夹。

通行密钥，以及 Chrome 只放在自家同步里、不作为已存密码的东西，从没离开 Chrome；一个会把凭据分给 Agent 的密码管理是另一件事——[1Password](1password.md) 连附件和一次性验证码一起导入，它的服务凭据也更适合自动化。
