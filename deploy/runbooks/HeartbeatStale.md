# HeartbeatStale, HeartbeatMissing: a scheduled job did not report

## What it means

A heartbeat in the catalogue (`catalog/heartbeats.yaml`) has not reported success within its `max_age` (`HeartbeatStale`, at the heartbeat's own severity), or has never reported at all in 31 days although it is listed (`HeartbeatMissing`, after a day's grace). The job behind it, a backup or a scheduled task, did not run, failed, or ran without telling Beacon.

## What to check

1. On the machine that runs the job: its own log (cron's mail, `systemctl status <timer>`, `journalctl`), and whether it ran at all.
2. If the job succeeded but nothing arrived: does it end with `beacon-heartbeat <name>` (a machine with the probe agent), or push one sample with its own token (`beacon enroll heartbeat <name>`, `docs/guide/monitoring.md`)? Is the name the same as in the catalogue?
3. For a pushed heartbeat: the token's expiry (`beacon token list`), and the gateway's log for a 401.

## How to fix

Run the job again, fix what broke it, or fix the reporting. A job that no longer exists: remove the heartbeat from the catalogue.

## How to verify

`beacon_heartbeat_last_success_timestamp_seconds{heartbeat="<name>"}` has a fresh sample; the alert resolves within a few minutes.

To silence: the card's buttons; `silence_alert` over MCP; or `beacon silence create -matchers 'alertname=HeartbeatStale,heartbeat=<name>' -duration 24h -comment '…'`.
