# ServiceDegraded: the service fails from some locations

## What it means

At least two probe locations have failed the service for 3 minutes, but fewer than half of those reporting it, and that has held for 5 minutes. The service works for most users; some path to it, or one CDN node, does not. Always a warning. When a whole service is down, `ServiceDown` fires instead and this one is inhibited.

## What to check

1. Which locations fail, on the card or in the evidence, and what they have in common: the same region, the same network, the same CDN node.
2. The failure reason there: a timeout or a connect error from one network is routing or a blocked address; a wrong status code from one location is a CDN node serving something stale or broken.
3. For CDN services, `CDNNodeOutOfRotation` and friends: a node that fails probes may also have dropped out of its rotation, which is the CDN doing its job.

## How to fix

A network path is mostly out of your hands: wait, or route around it (another CDN node, another address). A broken node: fix or drain it. A location that is itself the problem (its machine, its uplink) is a probe problem, not a service problem: fix the machine or let the location go.

## How to verify

The failing locations succeed again and the alert resolves on its own after `resolve_timeout`.

To silence: the card's buttons; `silence_alert` over MCP; or `linkos-beacon silence create -matchers 'alertname=ServiceDegraded,service=<service>' -duration 4h -comment '…'`.
