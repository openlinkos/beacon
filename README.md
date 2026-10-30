<p>
  <img alt="LinkOS Status" src="https://openlinkos.com/brand/linkos-status/github/readme.svg" height="56">
</p>

# LinkOS Status

English | [简体中文](README.zh-CN.md)

The status and alerting service of LinkOS. It takes Alertmanager's notifications and closes the loop with the people on call, and it lets people and AI agents see and act on alerts through a REST API and a built-in MCP server. Public status pages are next.

- **Alert cards in Feishu.** A new alert becomes an interactive card in the alert chat: what is wrong, since when, which probe locations see it failing, and links to the runbook and the dashboard. Buttons on the card acknowledge it (with a note) or silence it in Alertmanager for 1, 4 or 24 hours. The card changes in place, and turns green when the alert resolves.
- **Escalation to phones.** A critical alert that nobody acknowledges is buzzed in the Feishu app after 10 minutes and sent to Pushover's emergency priority after 15: the phone keeps ringing until someone acknowledges it there, which also acknowledges it here. An acknowledgement lapses after 4 hours if the alert is still firing.
- **Nothing silently lost.** Low-severity alerts and anything over the Feishu app's monthly API budget go to the chat's custom bot, which needs no quota. If the card cannot be sent, a critical alert goes straight to Pushover. The bridge pings healthchecks.io every minute while it can deliver alerts, and Alertmanager's own Watchdog pings it too, so a dead monitoring host or a broken Feishu connection still reaches a phone.
- **An API and MCP for agents.** List alerts, acknowledge, silence and end silences, with scoped tokens (`status:read`, `alerts:write`) that expire. Every write is audited and echoed to the alert chat, so what an agent did is visible to everyone.
- **One small binary.** Go, one bbolt file for state, a distroless image that runs as non-root. Probes, metrics and rules stay in VictoriaMetrics, vmalert and Alertmanager.

## How it fits

```
probes (blackbox, per location) ─▶ VictoriaMetrics ─▶ vmalert ─▶ Alertmanager ─webhook─▶ linkos-status
                                                                       │                     │  cards, buttons (long connection)
                                         Watchdog ─▶ healthchecks.io ◀─┘                     ├─▶ Feishu app ◀─▶ people on call
                                                                                             ├─▶ Feishu group bot (no quota)
                                                                                             └─▶ Pushover / Bark (escalation)
agents ─▶ /api/v1, /mcp (bearer tokens) ─▶ linkos-status ─▶ Alertmanager silences
```

The service expects alerts labelled with `severity` (`critical`, `warning` or `info`) and `service`, and annotated with `summary` (and optionally `description`, `runbook`, `dashboard`). An alert named `Watchdog` proves the pipeline works and is never shown.

## Running it

```bash
docker run -d --name linkos-status -p 127.0.0.1:17001:17001 \
  -v ./config.yaml:/etc/linkos-status/config.yaml:ro \
  -v ./catalog:/etc/linkos-status/catalog:ro \
  -v ./secrets:/etc/linkos-status/secrets:ro \
  -v linkos-status:/var/lib/linkos-status \
  ghcr.io/openlinkos/status:latest
```

Start from [config.example.yaml](config.example.yaml); `linkos-status check-config` validates it. Point Alertmanager's default receiver at the service:

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

Set up the Feishu app as in [docs/feishu.md](docs/feishu.md). Pushover needs its iOS app with Critical Alerts allowed, an application token and your user key.

### Tokens and drills

Commands that change the running service talk to it over a socket in its data directory:

```bash
docker exec linkos-status linkos-status token create claude-code -scopes status:read,alerts:write -expires 90d
docker exec linkos-status linkos-status token list
docker exec linkos-status linkos-status drill fail 15m    # /drill/healthz answers 503: probe it to rehearse the whole chain
docker exec linkos-status linkos-status drill off
```

## API and MCP

- REST: `/api/v1`, described in [internal/api/openapi.yaml](internal/api/openapi.yaml) (also served at `/api/v1/openapi.yaml`).
- MCP: streamable HTTP at `/mcp` with the same bearer tokens. Tools: `status_summary`, `list_alerts`, `get_alert`, `list_silences`, `list_audit`, `ack_alert`, `silence_alert`, `expire_silence`.

```bash
claude mcp add --transport http linkos-status https://status.example.com/mcp --header "Authorization: Bearer lst_…"
```

| Route | Purpose |
| --- | --- |
| `GET /healthz`, `GET /readyz` | Liveness; readiness (Watchdog seen, Feishu connection up, budget left) |
| `GET /metrics` | Prometheus metrics, direct connections only |
| `POST /hooks/alertmanager` | Alertmanager's webhook (bearer token) |
| `POST /hooks/feishu` | Feishu card callbacks when `feishu.callback: http` |
| `GET /drill/healthz` | 200, or 503 during a drill |

## Security

Tokens are stored only as SHA-256 hashes and expire; failed attempts are limited per client address. Only the people listed in `feishu.on_call` can act on cards. Secrets are read from files and never logged. Report vulnerabilities to security@linkos.io.

## Licence

[Apache License 2.0](LICENSE). The LinkOS and OpenLinkOS names and marks are not covered by it.
