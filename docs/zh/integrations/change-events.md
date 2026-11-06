---
sourceHash: sha256-2f9b83a1
---

# 部署与 Flux

部署、配置修改和 Flux 的同步以变更事件的形式送到 Beacon 之后，告警的证据就能说出故障前刚改了什么。发送方用带 `changes:write` 作用域的令牌，把事件发到网关（17006 端口）：

```bash
curl -fsS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"source": "github-actions", "kind": "deploy", "service": "hub-example-com", "summary": "hub v2.1",
       "url": "https://github.com/example/hub/actions/runs/1"}' \
  https://metrics.example.com/api/v1/changes
```

在中心上创建令牌：

```bash
docker exec beacon beacon token create ci-deploy -scopes changes:write -expires 365d
```

| 字段 | 含义 |
| --- | --- |
| `source` | 必填：发送方，比如 `github-actions` 或 `deploy-script` |
| `kind` | `deploy`、`config`、`rollback` 或其他词；默认 `change` |
| `summary` | 必填：改了什么 |
| `service`、`machine` | 和什么有关，写法与告警的 `service` 标签、机器名一致 |
| `url`、`revision` | 详情链接；提交号或版本号 |
| `time` | RFC 3339；默认为当前时间；最多比现在晚一小时，也不能早于变更的保留期限 |

不认识的字段会被拒绝，拼错了不会悄悄被忽略。Beacon 会把令牌的名字和每个变更一起记下。

Beacon 自己也记两类变更，来源都是 `beacon`：启动时的版本与上次运行的不同，记一条 `deploy`；[演练](../guide/drills.md)开始（摘要写明它到点自己结束的时间）或被提前结束，记一条 `drill`，服务是 `drill`。

## Flux {#flux}

Flux 的 notification-controller 把事件发给类型为 `generic` 的 Provider。让它指向 `/api/v1/changes/flux`，令牌放在请求头里：

```yaml
apiVersion: v1
kind: Secret
metadata: {name: beacon-changes, namespace: flux-system}
stringData:
  address: https://metrics.example.com/api/v1/changes/flux
  headers: |
    Authorization: Bearer lbt_…
---
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Provider
metadata: {name: beacon-changes, namespace: flux-system}
spec:
  type: generic
  secretRef: {name: beacon-changes}
---
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Alert
metadata: {name: beacon-changes, namespace: flux-system}
spec:
  providerRef: {name: beacon-changes}
  eventSeverity: info
  eventSources:
    - {kind: Kustomization, name: "*"}
    - {kind: HelmRelease, name: "*"}
  eventMetadata:
    service: hub-example-com    # 这些对象运行的是什么服务
```

| Flux 事件 | 变更事件 |
| --- | --- |
| `reason` | `kind`，比如 `ReconciliationSucceeded` 或 `UpgradeFailed` |
| `involvedObject` | `object`：`Kustomization/flux-system/apps` |
| `message`（有 Alert 的 `summary` 时接在它后面） | `summary` |
| `metadata`：`…/revision` | `revision` |
| `severity`、`timestamp` | `severity`、`time` |
| `metadata`：`service`、`machine`（Alert 的 `eventMetadata`），或地址里的 `?service=…&machine=…` | `service`、`machine` |

`source` 为 `flux`。需要通知人的 Flux 告警，仍然照旧发到 `POST /api/v2/alerts`；变更事件从不通知任何人，也不回显到告警群。

**保留和查询。** 变更事件保留 `changes.keep`（默认 `720h`，即 30 天），最多 50,000 条。`GET /api/v1/changes` 和 MCP 工具 `list_changes`（作用域 `status:read`）按从新到旧列出，可以按 `service`、`machine`、`source` 和 `since`（`2h`、`7d` 或一个时间点；默认最近一天）筛选。

**频率限制。** 每个令牌每分钟最多发 60 个变更事件；超出的返回 `429` 和 `changes.too_many`，直到这一分钟结束，并计入 `beacon_gateway_changes_limited_total{token}`。批量发送或者分散发送，就不会碰到这个限制。
