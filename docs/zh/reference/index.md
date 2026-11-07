---
sourceHash: sha256-b68792f8
---

# 参考

Beacon 接收和返回的一切，列出来供查阅。怎么用，见[使用 Beacon](../guide/using-beacon.md)。

网站构建时会拿这些页面和代码核对。每个 REST 操作、网关路由、错误码、作用域、MCP 工具、命令、参数和 `config.yaml` 配置项都必须出现在这里，否则构建失败。

## 给 Agent 和脚本 {#for-agents-and-scripts}

1. [REST API](api.md)：怎样拿到凭据、每个接口及其所需作用域、网关的路由，以及每个错误码。
2. [MCP 工具](mcp.md)：怎样连接，以及每个工具和它的作用域。
3. [Webhook](webhooks.md)：钩子发出什么：事件、请求头、负载、签名和重试。

## 给运维的人 {#for-operators}

1. [命令行](cli.md)：`beacon`、`beacon-probe` 和 `beacon-oncall` 的每个命令和参数。
2. [config.yaml](config.md)：服务自身配置的每个配置项。
3. [目录文件格式](catalogue.md)：目录、检测目标和模块文件的每个字段，以及每次修改都要通过的检查。

## Beacon 怎样判断 {#how-beacon-decides}

1. [角色、预设与权限](permissions.md)：人、他们的 Agent 和机器的令牌各能做什么。
2. [指标与告警规则](metrics.md)：每个指标，以及参考部署自带的每条告警规则。
3. [数据来源](sources.md)：谁给 Beacon 发什么、发到哪个路由、用哪个令牌。
