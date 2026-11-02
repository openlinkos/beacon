# ServiceDown: the service is down

## What it means

At least two probe locations, and at least half of those reporting the service, have failed it for 3 minutes, and that has held for 1 minute more. The severity is the target's own (`severity` in `targets/*.yaml`), critical when it has none. One location's hiccup never raises this: that is what the agreement is for.

## What to check

1. The card (or `get_evidence` for an agent, `GET /api/v1/alerts/{id}/evidence` for anyone) lists each location's result and why it failed: DNS, connect, timeout, TLS, status code, body. The reason narrows it: a wrong status code is the service, a timeout from everywhere is the network or the origin, TLS is the certificate or the proxy.
2. Every location failing: the origin or whatever sits in front of it. Alerts in the same `failure_domain` fire together when one origin takes several services down; the evidence lists them.
3. Only some locations failing, but half or more: one network path or one CDN node. Compare the regions on the card.
4. A change just before: the evidence lists change events (deployments, Flux) around the alert's start.

## How to fix

Whatever the reason points at, on the machine: an agent reaches it through the SSH channel (`run_command`, or its own `ssh`); a person the same way. A planned outage should have been silenced beforehand; silence it now if it is one.

## How to verify

The probes keep going: the card turns green when every location succeeds again, and `linkos:service_up` for the service goes back to 1. `probe_now`-style re-probing is not needed; the next minute's results tell.

To silence: the card's buttons; `silence_alert` over MCP; or `linkos-beacon silence create -matchers 'alertname=ServiceDown,service=<service>' -duration 2h -comment '…'` on the monitoring host.
