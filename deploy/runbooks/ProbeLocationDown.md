# ProbeLocationDown: a probe location has gone silent

## What it means

A location in the catalogue, whose machine has not expired, has sent no probe results for 10 minutes, and that has held for 5 more. The machine is down, `linkos-probe` has stopped, or its path to the gateway is cut. Nothing is lost yet: the agent queues results on disk (up to 64 MB) and pushes them when it is back. While a location is silent it does not count towards outages, so one silent location weakens the agreement rule for every service.

## What to check

1. Can you reach the machine? Through the SSH channel if it is enrolled as a machine too, else your usual way.
2. On it: `systemctl status linkos-probe`, `journalctl -u linkos-probe --since -1h`, and `curl -s 127.0.0.1:17009/metrics | grep linkos_probe_` (the last push, push errors, the queue).
3. A token refused (401 in the log): the location was re-enrolled with `-rotate` and the machine still has the old token, or the location's `vps` resource in the catalogue has expired.
4. Pushes failing with 5xx: the gateway (see `GatewayErrors`).

## How to fix

Start the agent, fix the network, or install the new token (`linkos-beacon enroll probe <id> -rotate` on the centre, then `probe/install.sh -token-file …` on the machine). A machine that is gone for good: remove the location from the catalogue (`config`, or `DELETE /api/v1/config/locations/<id>`), or give it a `vps` resource with its end date, and revoke its token (`linkos-beacon token revoke probe-<id>`).

## How to verify

Results arrive again (`probe_success{location="<id>"}` has fresh samples), the queue drains, and the alert resolves.

To silence: the card's buttons; `silence_alert` over MCP; or `linkos-beacon silence create -matchers 'alertname=ProbeLocationDown,location=<id>' -duration 4h -comment '…'`.
