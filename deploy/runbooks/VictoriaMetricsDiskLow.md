# VictoriaMetricsDiskLow: the metrics store is running out of disk

## What it means

The volume VictoriaMetrics stores metrics on has had less than 5 GB free for 10 minutes. Below its minimum it switches to read-only, writes fail, and `GatewayErrors` follows.

## What to check

1. `df -h` on the monitoring host, and `docker system df` (images and logs take space too).
2. What grew: VictoriaMetrics' own UI through an SSH tunnel (`ssh -N -L 8428:127.0.0.1:8428 <host>`, then http://127.0.0.1:8428/vmui, the Cardinality page): the series with the most samples are usually new hosts or targets, or a label with many values.
3. The retention: `-retentionPeriod` in `compose.yaml` (400 days by default).

## How to fix

Free disk (old images, logs), shorten the retention, or grow the volume. VictoriaMetrics frees space from expired data in the background; `docker compose restart victoriametrics` after changing the retention.

## How to verify

`vm_free_disk_space_bytes` is above 5 GB again; the alert resolves after 10 minutes.

To silence: `beacon silence create -matchers 'alertname=VictoriaMetricsDiskLow' -duration 24h -comment '…'`.
