---
sourceHash: sha256-79ce154e
---

# 使用团队凭据

将服务的访问令牌保存在 Beacon 中，所有者可以用它发出固定的 HTTPS 请求，也可以授权 Agent 发出同样的请求。服务收到令牌，人和 Agent 只收到 HTTP 状态码。每次使用先写入审计日志，再发出请求。

## 启用凭据库 {#enable-the-store}

管理员生成一个包含 64 个十六进制字符的专用密钥文件，让 Beacon 进程具有读取权限。更新 `config.yaml` 后重启：

```yaml
credentials:
  enabled: true
  key_file: /etc/beacon/secrets/team-credential-key
  allowed_hosts: [api.example.com]
```

在使用 SQLite 或 PostgreSQL 的单个 Beacon 中心启用此功能。只批准受团队信任、可以接收访问令牌的主机，使用精确的主机名，并在端点写了端口时列出端口。密钥和最新且可靠的防回滚记录与数据库备份分开保管。依赖凭据库前先按[备份与恢复](../self-hosting/backup.md#restore)验收恢复流程。

Beacon 无法验证密钥或策略时，凭据库不可用，监控和 SSH 仍照常运行。只写入数据库的变更会显示为等待验证。修复防回滚记录并重新加载控制台，再决定是否重新提交。

## 保存和使用令牌 {#store-and-use-a-token}

1. 用所有者账号登录，打开**团队凭据**。
2. 填写名称、已批准的 HTTPS 端点（例如 `https://api.example.com/ready`）和访问令牌。直接在控制台录入令牌。端点应省略查询参数、片段和 URL 内的凭据。
3. 打开凭据，点击**发送测试请求**，以你自己的身份发出经过认证的 GET 请求。返回的状态码说明服务如何响应。点击**查看审计记录**可找到以你的身份发出的请求。
4. 服务签发新令牌时，点击**替换令牌**。端点保持固定，原有授权继续生效。

## 授权和撤销 {#delegate-and-revoke}

1. 给一名所有者的自定义 Agent 添加 `secrets:use` 作用域，并为其签发无头令牌。标准 OAuth 登录等已有预设的权限照旧。
2. 在凭据的**授权使用**区选择 Agent 并设定时长。同一页面可以缩短或撤销授权。
3. Agent 可调用 `list_team_credentials` 和 `use_team_credential`，或对应的 [REST 接口](../reference/api.md#team-credentials)。授权立即生效。到期、撤销或所有者角色变化后，新的使用会被拒绝。已获准的请求可以继续结束。

部署的所有者都能以本人身份使用团队凭据。首版支持对固定 HTTPS 请求使用服务的访问令牌。向成员授权、查看原始凭据、导入、浏览器自动填充等团队密码管理能力继续单独推进。
