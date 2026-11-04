# Agents woken by a webhook

A hook of `kind: webhook` is a signed POST to an address of yours; something at the other end runs the agent. List it in Beacon's configuration:

```yaml
agents:
  - name: claude-oncall
    kind: webhook
    url: https://agents.example.com/hooks/beacon
    secret_file: /etc/beacon/secrets/agent-hook-secret   # at least 24 random characters
```

When the hook is called, what the fields mean, the retries and where each call's outcome shows are in [Put an agent on call](../guide/on-call.md#wake-an-agent). For the other end, `beacon-oncall` takes the webhook, checks the signature and starts the agent ([Headless agents and CI](headless.md#beacon-oncall-the-reference-harness)).

## The call

Beacon POSTs JSON with these headers:

| Header | Value |
| --- | --- |
| `X-Beacon-Signature` | `sha256=` and the hex HMAC-SHA256 of the body under the secret |
| `X-Beacon-Event` | `firing` or `resolved` |
| `X-Beacon-Delivery` | The call's id, the same in every attempt |

```json
{
  "delivery": "9f3c2a1b7e4d5c60",
  "event": "firing",
  "at": "2026-11-02T02:00:31Z",
  "hook": "claude-oncall",
  "alert": {
    "id": "0cdfbada62dd", "status": "firing", "severity": "critical",
    "title": "git.example.com is down", "description": "git.example.com has failed for 3 minutes from at least half of the probe locations …",
    "service": "git.example.com",
    "labels": {"alertname": "ServiceDown", "service": "git.example.com", "severity": "critical", "failure_domain": "origin-1"},
    "alerts": 1, "starts_at": "2026-11-02T01:58:00Z"
  },
  "links": {
    "alert": "https://status.example.com/api/v1/alerts/0cdfbada62dd",
    "evidence": "https://status.example.com/api/v1/alerts/0cdfbada62dd/evidence",
    "timeline": "https://status.example.com/api/v1/alerts/0cdfbada62dd/timeline",
    "mcp": "https://status.example.com/mcp",
    "runbook": "https://github.com/example/ops/blob/main/runbooks/ServiceDown.md"
  },
  "hint": "Beacon woke you for this alert group. Get its evidence in one call: …",
  "untrusted": {"fields": ["alert.title", "alert.description", "alert.service", "alert.services", "alert.labels"], "note": "…"}
}
```

When the alert is about a heartbeat whose catalogue entry names its machine ([Heartbeats](../guide/monitoring.md#heartbeats)), the `hint` of a `firing` call says which machine that is and that `list_machines` shows how to reach it.

A `resolved` payload has the same fields, its `at` being when the group resolved (not when the call went out, `resolved_after` later), and one more: `firing_call`, the call that woke the agent for this episode, so that the run it started can be told apart, with whether that run may still be going:

```json
"firing_call": {"delivery": "9f3c2a1b7e4d5c60", "at": "2026-11-02T02:00:31Z", "status": "ok", "recent": true}
```

`recent` is true when the firing call was made less than ten minutes before the group resolved. The hint then says that the run that call started may still be finishing its notes, and to read `get_timeline`, the notes included, before leaving a closing note of its own.

## Check the signature

A receiver of your own checks the signature against the raw body before reading it, and refuses a delivery whose `at` is more than ten minutes old or whose `delivery` it has seen:

```python
import hashlib, hmac

def genuine(secret: bytes, body: bytes, signature: str) -> bool:
    return hmac.compare_digest("sha256=" + hmac.new(secret, body, hashlib.sha256).hexdigest(), signature)
```

The payload carries no credentials: the agent fetches the evidence with its own token, an agent's with the On call preset, which reads the evidence, acknowledges and leaves notes ([Headless agents and CI](headless.md#make-an-agent-and-its-token)).

## Trust

The harness checks the signature and starts the program; that is all it decides. What the agent then does is bounded by its preset, its owner's role and the machines it may reach. Every call, command and session goes through Beacon, which records it and shows it to people on the card and in the chat, and the pause switch stops it. The payload carries no credentials; the harness and the wrappers read secrets only from files, and log neither them nor the payload. The prompt tells the agent to read what comes from outside Beacon as data, and Beacon marks those fields `untrusted`.
