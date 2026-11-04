# VMAlertRuleErrors: a rule group fails to evaluate

## What it means

vmalert has met errors evaluating a rule group (`vmalert_execution_errors_total`) for 5 minutes: a query that no longer parses, a label a rule uses that VictoriaMetrics rejects, or the datasource not answering. Alerts in that group may not fire while this lasts, so the eyes are half shut.

## What to check

1. `docker compose logs --since 30m vmalert` on the monitoring host: the error and the rule.
2. `http://127.0.0.1:8880` (through an SSH tunnel) lists the groups and their last errors.
3. If every group fails: VictoriaMetrics itself (`curl -s 127.0.0.1:8428/health`).

## How to fix

Fix the rule in `rules/*.yml` and check it before reloading: `docker compose run --rm --no-deps vmalert -dryRun -rule='/etc/vmalert/rules/*.yml'`, and the unit tests in `rules/tests/`. Then `curl -s -X POST 127.0.0.1:8880/-/reload` (or `docker compose restart vmalert`).

## How to verify

The counter stops rising; the alert resolves after 10 minutes.

To silence: `beacon silence create -matchers 'alertname=VMAlertRuleErrors' -duration 2h -comment '…'`.
