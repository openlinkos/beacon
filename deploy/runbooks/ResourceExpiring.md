# ResourceExpiring: something in the catalogue runs out

## What it means

A resource in the catalogue (`catalog/resources.yaml`: a machine, a domain, a token, a service paid by the year) expires within 30 days (warning) or 7 days (critical). A `vps` named like a probe location also bounds that location: past the date, the location is no longer expected to report and its token stops writing.

## What to check

1. Is it meant to go on? Then renew it where it lives (the provider, the registrar, the issuer).
2. Is it meant to lapse? Then plan what depends on it: a probe location on that machine, services on that domain.

## How to fix

Renew, then put the new date in the catalogue, so the alert ends: edit `resources.yaml` and `linkos-beacon config import`, or `PUT /api/v1/config/resources/<name>` with the new `expires`. Letting it lapse: remove the resource (and, for a machine, its location) from the catalogue once it is gone.

## How to verify

`linkos_resource_expiry_timestamp_seconds{resource="<name>"}` shows the new date; the alert resolves within minutes.

To silence: `linkos-beacon silence create -matchers 'alertname=ResourceExpiring,resource=<name>' -duration 24h -comment '…'`.
