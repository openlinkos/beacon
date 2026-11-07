# Agents woken by a webhook

A hook of `kind: webhook` is a signed POST to an address of yours. Something at the other end runs the agent. List the hook in Beacon's configuration:

```yaml
agents:
  - name: claude-oncall
    kind: webhook
    url: https://agents.example.com/hooks/beacon
    secret_file: /etc/beacon/secrets/agent-hook-secret   # at least 24 random characters
```

[Put an agent on call](../guide/on-call.md#wake-an-agent) says when the hook is called, what the fields mean, the retries, and where each call's outcome shows. For the other end, `beacon-oncall` takes the webhook, checks the signature and starts the agent ([Headless agents and CI](headless.md#beacon-oncall-the-reference-harness)).

## The call

Beacon POSTs JSON to the hook's `url`. The body holds:

- the alert group with its labels;
- links to its evidence, its timeline and the MCP endpoint;
- a `hint` that says how to start.

Three headers come with it:

| Header | What it holds |
| --- | --- |
| `X-Beacon-Event` | `firing` or `resolved` |
| `X-Beacon-Delivery` | The call's id, the same in every attempt |
| `X-Beacon-Signature` | The body's HMAC-SHA256 under the secret |

A `resolved` call also carries `firing_call`: the call that woke the agent for this episode. With it, a run still finishing its notes can be told apart. Every header and field, with a whole payload, is in the [Webhooks reference](../reference/webhooks.md).

## Check the signature

A receiver of your own checks the signature against the raw body before reading it. It refuses a delivery whose `at` is more than 15 minutes old, or whose `delivery` it has seen:

```python
import hashlib, hmac

def genuine(secret: bytes, body: bytes, signature: str) -> bool:
    return hmac.compare_digest("sha256=" + hmac.new(secret, body, hashlib.sha256).hexdigest(), signature)
```

The payload carries no credentials. The agent fetches the evidence with its own token: an agent's with the On call preset, which reads the evidence, acknowledges and leaves notes ([Headless agents and CI](headless.md#make-an-agent-and-its-token)).

## Trust

The harness checks the signature and starts the program. That is all it decides.

- **Bounded.** What the agent then does is bounded by its preset, its owner's role and the machines it may reach.
- **On record.** Every call, command and session goes through Beacon. Beacon records it and shows it to people on the card and in the chat, and the pause switch stops it.
- **No credentials in the payload.** The harness and the wrappers read secrets only from files, and log neither them nor the payload.
- **Outside text is data.** The prompt tells the agent to read what comes from outside Beacon as data, and Beacon marks those fields `untrusted`.
