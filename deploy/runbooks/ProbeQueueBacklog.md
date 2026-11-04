# ProbeQueueBacklog: a probe cannot push fast enough

## What it means

A probe's queue on disk (`beacon_probe_queue_bytes`) has held more than 8 MB for 15 minutes. Some pushes get through (or this could not be reported), but not enough: a lossy path to the gateway, or the gateway refusing writes now and then. At 64 MB the agent drops its oldest batches.

## What to check

1. On the machine: `journalctl -u beacon-probe --since -1h` says which pushes fail and why (timeouts, 5xx, 401).
2. `curl -s 127.0.0.1:17009/metrics | grep beacon_probe_push_errors_total`, and whether it still rises.
3. On the centre: `GatewayErrors`, VictoriaMetrics' health (`docker compose logs victoriametrics`), and `VictoriaMetricsDiskLow`.

## How to fix

A lossy path: little to do on the probe; the queue is there for that. A gateway or store problem: fix that, and the queue drains on its own. A machine that is simply too far from the centre: consider a gateway address closer to it.

## How to verify

`beacon_probe_queue_bytes` for the host falls to zero and stays there.

To silence: `beacon silence create -matchers 'alertname=ProbeQueueBacklog,host=<host>' -duration 4h -comment '…'`.
