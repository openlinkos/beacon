# MainlandPathDegraded: unreachable from mainland China

## What it means

Every probe location in the `mainland` region has failed the service for 3 minutes while the overseas locations succeed, for 5 minutes. The service is up; the path from mainland China to it is not: an address blocked, a route congested, a CDN node for that region out. A warning; `ServiceDown` inhibits it.

## What to check

1. The failure reason from the mainland locations: a timeout or connect failure is the path; a TLS failure can be interference with the handshake; a wrong status code is the CDN node serving mainland users.
2. Whether other services behind the same address or CDN cluster fail too (the card groups them when they do).
3. From a mainland location, by hand through the SSH channel: `curl -sv --max-time 20 https://<service>/`, `traceroute`.

## How to fix

If the service is meant to work from mainland China: a different address or CDN node for that region, or wait out a transient. If it is not, lower the stakes: give the target no mainland probing (an `overseas-` target set) so this never fires for it.

## How to verify

The mainland locations succeed again; the alert resolves after `resolve_timeout`.

To silence: the card's buttons; `silence_alert` over MCP; or `linkos-beacon silence create -matchers 'alertname=MainlandPathDegraded,service=<service>' -duration 4h -comment '…'`.
