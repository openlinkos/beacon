# Watchdog: the alert that is always firing

## What it means

This alert **fires all the time**, and that is right: vmalert sends it every 30 seconds, and while Beacon receives it and can deliver alerts (the chat connection is up, the quota is not used up) Beacon pings healthchecks.io every minute (`healthchecks.bridge_url_file` in `config.yaml`); when something is wrong it pings `/fail` instead. If healthchecks.io stops hearing from Beacon, it raises the alarm from outside: the monitoring centre itself is in trouble. No card is made for the Watchdog, and it must never be silenced.

## What to check

When healthchecks.io says the check is late or failed:

1. Can you reach the monitoring host? If not, the machine or its network (your provider's console).
2. On it: `docker compose ps` in the deployment directory: all four containers up and healthy?
3. `curl -s 127.0.0.1:17001/readyz`: the reasons it gives. "Watchdog" means the rule engine's alerts are not arriving (`docker compose logs vmalert`; `curl -s 127.0.0.1:17001/metrics | grep 'route="alerts"'` shows what the gateway received). A chat reason means the chat connection or quota (`docker compose logs beacon`).
4. All fine, yet no pings: the host's outbound path to healthchecks.io, or the URL in the secret file.

## How to fix

Start what stopped (`docker compose up -d`), or fix the chat's credentials. Nothing is lost meanwhile: alerts that fired are in Beacon, and probes queue their results.

## How to verify

`/readyz` answers 200, healthchecks.io shows a fresh ping, and the Watchdog is in `GET /api/v1/alerts?state=all` with a recent time.

Planned downtime of the centre: pause the check on healthchecks.io for the duration, and resume it afterwards. Do not silence the Watchdog.
