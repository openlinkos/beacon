# Deployments and Flux

When deployments, configuration changes and Flux's reconciliations reach Beacon as change events, the evidence for an alert says what changed just before it. A sender posts one to the gateway (port 17006) with a token that has the `changes:write` scope:

```bash
curl -fsS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"source": "github-actions", "kind": "deploy", "service": "hub-example-com", "summary": "hub v2.1",
       "url": "https://github.com/example/hub/actions/runs/1"}' \
  https://metrics.example.com/api/v1/changes
```

Make the token on the centre:

```bash
docker exec beacon beacon token create ci-deploy -scopes changes:write -expires 365d
```

| Field | Meaning |
| --- | --- |
| `source` | Required: the sender, such as `github-actions` or `deploy-script` |
| `kind` | `deploy`, `config`, `rollback` or another word; `change` by default |
| `summary` | Required: what changed |
| `service`, `machine` | What it concerns, as the alerts' `service` label and the machines' names say it |
| `url`, `revision` | Where to read more; the commit or version |
| `time` | RFC 3339; now by default; at most an hour ahead, and no older than changes are kept |

Unknown fields are refused, so a typo does not go unnoticed. Beacon keeps the token's name with each change.

Beacon records two kinds of its own, with the source `beacon`: `deploy` when it starts on a version other than the one it last ran, and `drill` when a [drill](../guide/drills.md) starts (its summary says when it ends by itself) or is ended early, on the service `drill`.

## Flux

Flux's notification-controller posts its events to a Provider of type `generic`. Point one at `/api/v1/changes/flux`, with the token in a header:

```yaml
apiVersion: v1
kind: Secret
metadata: {name: beacon-changes, namespace: flux-system}
stringData:
  address: https://metrics.example.com/api/v1/changes/flux
  headers: |
    Authorization: Bearer lbt_…
---
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Provider
metadata: {name: beacon-changes, namespace: flux-system}
spec:
  type: generic
  secretRef: {name: beacon-changes}
---
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Alert
metadata: {name: beacon-changes, namespace: flux-system}
spec:
  providerRef: {name: beacon-changes}
  eventSeverity: info
  eventSources:
    - {kind: Kustomization, name: "*"}
    - {kind: HelmRelease, name: "*"}
  eventMetadata:
    service: hub-example-com    # what these objects run
```

| Flux event | Change |
| --- | --- |
| `reason` | `kind`, such as `ReconciliationSucceeded` or `UpgradeFailed` |
| `involvedObject` | `object`: `Kustomization/flux-system/apps` |
| `message` (after the Alert's `summary`, if any) | `summary` |
| `metadata`: `…/revision` | `revision` |
| `severity`, `timestamp` | `severity`, `time` |
| `metadata`: `service`, `machine` (the Alert's `eventMetadata`), or `?service=…&machine=…` in the address | `service`, `machine` |

`source` is `flux`. Flux alerts that should page people still go to `POST /api/v2/alerts` as before; change events never page anyone and are not echoed to the chat.

**Keeping and reading them.** Change events are kept for `changes.keep` (`720h`, 30 days, by default), at most 50,000. `GET /api/v1/changes` and the MCP tool `list_changes` (scope `status:read`) list them, newest first, by `service`, `machine`, `source` and `since` (`2h`, `7d` or a time; the last day by default).

**The limit.** A token may post 60 change events a minute; the ones beyond that are refused with `429` and `changes.too_many` until the minute is over, and counted in `beacon_gateway_changes_limited_total{token}`. A sender that batches its events, or spreads them out, never meets the limit.
