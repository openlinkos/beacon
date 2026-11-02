# CDNNodeOutOfRotation and the other rotation alerts

## What it means

For every probe location in the catalogue with an `address` and a `rotation` (a CDN node and its cluster's public DNS name), Beacon resolves both every minute and compares. Four alerts come out of it:

- **CDNNodeOutOfRotation** (warning): a primary node's address has not been in its rotation's answers for 15 minutes. The node is up and probing, yet serves no users through that name.
- **CDNStandbyInRotation** (info): a node marked `standby: true` has been in the answers for 5 minutes: a primary failed or was taken out, and the standby stepped in.
- **CDNRotationUnknownAddress** (warning): the rotation has answered with an address no location claims, for 30 minutes. A node nobody watches.
- **CDNRotationResolveFailing** (warning): the rotation's name itself has not resolved from the monitoring host for 10 minutes.

## What to check

1. The CDN's own console: the node's state in its cluster (taken out by hand, dropped by a health check, out of quota, expired).
2. A standby serving: find the primary that is out (`CDNNodeOutOfRotation` fires beside it, usually).
3. An unknown address: a node added to the cluster without a probe location, or a DNS record someone else changed. `dig +short <rotation>` from several places.
4. A rotation that does not resolve: `dig <rotation>` from elsewhere. Everywhere broken is the DNS zone or the authoritative servers, and the sites on that cluster fail for everyone; only the monitoring host failing is its resolver.

## How to fix

Put the node back in rotation or fix what dropped it; once a primary is back, take the standby out again. A new node: add it to the catalogue as a location with its address (and enrol a probe on it). A node retired for good: remove its location. A broken zone: fix the records at the authoritative DNS.

## How to verify

`linkos_location_in_rotation{location="<id>"}` is 1 again (0 for a standby), `linkos_rotation_unknown_address` is gone, `linkos_rotation_resolve_success` is 1.

To silence a node taken out on purpose: `linkos-beacon silence create -matchers 'alertname=CDNNodeOutOfRotation,location=<id>' -duration 24h -comment '…'`.
