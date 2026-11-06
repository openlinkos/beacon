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

Beacon POSTs JSON to the hook's `url`: the alert group with its labels, links to its evidence, its timeline and the MCP endpoint, and a `hint` that says how to start. Three headers come with it: `X-Beacon-Event` (`firing` or `resolved`), `X-Beacon-Delivery` (the call's id, the same in every attempt) and `X-Beacon-Signature` (the body's HMAC-SHA256 under the secret). A `resolved` call also carries `firing_call`, the call that woke the agent for this episode, so that a run still finishing its notes can be told apart. Every header and field, with a whole payload, is in the [Webhooks reference](../reference/webhooks.md).

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
