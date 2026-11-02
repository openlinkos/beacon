# GatewayErrors: the gateway fails requests

## What it means

Beacon's gateway (port 17006, behind `metrics.example.com`) has answered requests with a server error (5xx) throughout the last 10 minutes, by route: `import` and `write` (pushes), `targets` and `modules` (what probes fetch), `alerts` (the rule engine), `read` (agents' queries). Almost always VictoriaMetrics refusing or failing writes. A wrong token or an expired machine is a 4xx, the sender's problem, and does not count here. Probes keep their results queued meanwhile.

## What to check

1. `docker compose logs --since 30m linkos-beacon | grep -i gateway` on the monitoring host: the error behind the 5xx.
2. VictoriaMetrics: `docker compose ps`, `docker compose logs --since 30m victoriametrics`, `curl -s 127.0.0.1:8428/health`.
3. Disk: `VictoriaMetricsDiskLow`; a full data volume stops writes.
4. Which route and how many: `curl -s 127.0.0.1:17001/metrics | grep linkos_gateway_requests_total` (direct connections only, so from the host).

## How to fix

Fix VictoriaMetrics (restart it, free disk), or whatever the log names. The probes push their queues once writes work again; nothing needs to be redone on their side.

## How to verify

`linkos_gateway_requests_total{code=~"5.."}` stops rising; the alert resolves after 10 quiet minutes.

To silence: `linkos-beacon silence create -matchers 'alertname=GatewayErrors' -duration 2h -comment '…'`.
