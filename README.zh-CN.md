<p>
  <img alt="LinkOS Status" src="https://openlinkos.com/brand/linkos-status/github/readme.svg" height="56">
</p>

# LinkOS Status

[English](README.md) | 简体中文

LinkOS 的状态与告警服务：接收 Alertmanager 的通知，和值班的人形成闭环；人和 AI Agent 都可以通过 REST API 和内置的 MCP 查看、处理告警。公开的状态页是下一步。

- **飞书告警卡片。**新告警会变成告警群里的一张交互卡片：出了什么问题、从什么时候开始、哪些检测点在失败，附处理手册和面板的链接。卡片上的按钮可以认领（可写备注），或者在 Alertmanager 里静默 1、4、24 小时。卡片原地更新，告警恢复后变绿。
- **升级到手机。**严重告警没人认领时，10 分钟后在飞书里加急，15 分钟后发 Pushover 紧急通知：手机会一直响，直到有人在 Pushover 里确认，这也算在这里认领。告警还在触发时，认领 4 小时后失效，重新开始升级。
- **不会悄悄丢失。**低级别的告警，以及飞书应用当月 API 额度用到一定比例后的告警，改由群自定义机器人发送，不占额度。卡片发不出去时，严重告警直接发 Pushover。告警桥能投递时每分钟 ping 一次 healthchecks.io，Alertmanager 的 Watchdog 也会 ping，所以监控机挂了、飞书连接断了，手机也能收到。
- **给 Agent 的 API 和 MCP。**列出告警、认领、静默、取消静默；令牌分作用域（`status:read`、`alerts:write`），会过期。每次写操作都记审计，并回显到告警群，Agent 做了什么大家都看得到。
- **一个小程序。**Go，状态存在一个 bbolt 文件里，distroless 镜像，以非 root 运行。检测、指标和规则仍由 VictoriaMetrics、vmalert、Alertmanager 负责。

## 在整体里的位置

```
检测点（blackbox，每地一套）─▶ VictoriaMetrics ─▶ vmalert ─▶ Alertmanager ─webhook─▶ linkos-status
                                                                   │                      │  卡片、按钮（长连接）
                                     Watchdog ─▶ healthchecks.io ◀─┘                      ├─▶ 飞书应用 ◀─▶ 值班的人
                                                                                          ├─▶ 飞书群机器人（不占额度）
                                                                                          └─▶ Pushover / Bark（升级）
Agent ─▶ /api/v1、/mcp（bearer 令牌）─▶ linkos-status ─▶ Alertmanager 静默
```

告警需要带 `severity`（`critical`、`warning` 或 `info`）和 `service` 标签，注解里写 `summary`（可选 `description`、`runbook`、`dashboard`）。名为 `Watchdog` 的告警只用来证明链路通畅，不会显示。

## 运行

```bash
docker run -d --name linkos-status -p 127.0.0.1:17001:17001 \
  -v ./config.yaml:/etc/linkos-status/config.yaml:ro \
  -v ./catalog:/etc/linkos-status/catalog:ro \
  -v ./secrets:/etc/linkos-status/secrets:ro \
  -v linkos-status:/var/lib/linkos-status \
  ghcr.io/openlinkos/status:latest
```

配置从 [config.example.yaml](config.example.yaml) 改起，`linkos-status check-config` 可以校验。把 Alertmanager 的默认接收方指向本服务：

```yaml
receivers:
  - name: bridge
    webhook_configs:
      - url: http://linkos-status:17001/hooks/alertmanager
        send_resolved: true
        http_config:
          authorization:
            credentials_file: /etc/alertmanager/secrets/linkos-status-webhook-token
```

飞书应用按 [docs/zh/feishu.md](docs/zh/feishu.md) 配置。Pushover 需要在 iPhone 上装它的应用并允许「紧急通知」（Critical Alerts），另外准备应用令牌和你的 User Key。

### 令牌和演练

改动运行中服务的命令，经它数据目录里的 socket 执行：

```bash
docker exec linkos-status linkos-status token create claude-code -scopes status:read,alerts:write -expires 90d
docker exec linkos-status linkos-status token list
docker exec linkos-status linkos-status drill fail 15m    # /drill/healthz 返回 503：让检测点检测它，演练整条链路
docker exec linkos-status linkos-status drill off
```

## API 和 MCP

- REST：`/api/v1`，说明见 [internal/api/openapi.yaml](internal/api/openapi.yaml)（也在 `/api/v1/openapi.yaml` 提供）。
- MCP：`/mcp`，streamable HTTP，用同样的 bearer 令牌。工具：`status_summary`、`list_alerts`、`get_alert`、`list_silences`、`list_audit`、`ack_alert`、`silence_alert`、`expire_silence`。

```bash
claude mcp add --transport http linkos-status https://status.example.com/mcp --header "Authorization: Bearer lst_…"
```

| 路由 | 用途 |
| --- | --- |
| `GET /healthz`、`GET /readyz` | 存活；就绪（收到过 Watchdog、飞书连接正常、额度未用完） |
| `GET /metrics` | Prometheus 指标，只对直连开放 |
| `POST /hooks/alertmanager` | Alertmanager 的 webhook（bearer 令牌） |
| `POST /hooks/feishu` | `feishu.callback: http` 时接收飞书卡片回调 |
| `GET /drill/healthz` | 平时 200，演练期间 503 |

## 安全

令牌只存 SHA-256，并且会过期；同一来源地址的失败尝试有次数限制。只有 `feishu.on_call` 里的人能操作卡片。密钥从文件读取，从不写进日志。安全问题请发邮件至 security@linkos.io。

## 许可

[Apache License 2.0](LICENSE)。LinkOS 与 OpenLinkOS 的名称和标志不在该许可范围内。
