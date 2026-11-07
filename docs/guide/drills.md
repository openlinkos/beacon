# Rehearse an outage

By the end of this page you have watched the whole on-call loop happen on purpose, without touching a real service:

- the probes see a failure, and the rules fire;
- the card arrives;
- your agent reads the evidence, works a machine, has its fix checked and leaves a note;
- the alert resolves on what the probes then see.

A drill is how you find out that a phone is not reachable or a hook is misconfigured. You find out while everything still works, rather than at 3 a.m.

You need:

- Beacon running, with at least two probe locations reporting. One location's word is never an outage.
- An agent connected ([Put an agent on call](on-call.md)).
- For the card, the chat ([Feishu](../integrations/feishu.md)).

From nothing, follow [On a server](../self-hosting/install.md), steps 1 to 4. Or use [Try it on your laptop](quickstart.md) for the whole loop on one machine. The commands run on the centre, as `docker exec beacon beacon …` in the reference deployment.

## The drill service {#the-drill-service}

Beacon has a service of its own to rehearse on. `/drill/healthz` answers 200 until you say otherwise. Every location probes it as the critical service `drill` (`targets/all-drill.yaml`). It is not on the shipped status page, so the public never sees a drill.

## Break it {#break-it}

```bash
docker exec beacon beacon drill fail 15m
```

You will see `drill until …: /drill/healthz answers 503`.

- `beacon drill status` says where the drill stands.
- `beacon drill off` ends it.

Starting a drill and ending it are both change events. So the alert's evidence says a drill is running, and when it ends by itself. An agent working the alert cannot end it, but can check after that time with `probe_now`.

What happens, with two locations:

1. **Within a minute** both locations see 503, and `probe_success{service="drill"}` is 0 for each.
2. **After 3 minutes** of failure the rules count each location as failing. Two of two is at least two and at least half, so **`ServiceDown` fires 1 minute later**, about 4 to 5 minutes after the command. It appears in `GET /api/v1/alerts` and in the MCP's `list_alerts`.
3. **With the chat configured**, the card arrives in the group about 30 seconds after the alert (`group_wait`). It turns green on recovery. With Pushover, an unacknowledged critical alert reaches a phone after 15 minutes.

With one location nothing fires, by design.

## Walk the loop, you in the hook's place {#walk-the-loop}

Before wiring anything, be the hook yourself. Ask your agent: *"Is anything failing right now? Look at the evidence, check the machine, and tell me what you would do."*

It finds the alert with `list_alerts` and reads `get_evidence`. That one answer holds each location's result and reason (`http_status`, 503). The agent answers from what the probes see. A woken agent works the same way: the hook only takes the place of the person who asks.

Then let it act, one step of the loop at a time:

- **`ack_alert`** with a note, and **`add_note`** for what it finds. Both go on the alert's timeline, which people read on the card and in the console.
- **`silence_alert`** quiets the alert. On the centre, `beacon silence create -matchers 'alertname=ServiceDown,service=drill' -duration 1h -comment 'drill'` makes one from the command line. The audit log records it as made from the command line, and `beacon silence list` shows it.
- **A command on the machine.** If the drill's machine is one the [SSH channel](machines.md) reaches, have the agent run a command on it. The session lands on the alert's page with its replay.
- **`probe_now`** has every location probe the target again at once. It does not change the drill's answer: the service is still 503. But it exercises the call an agent makes after a real fix.

## End it and watch it recover {#end-it-and-watch-it-recover}

```bash
docker exec beacon beacon drill off
```

The probes succeed on their next round, within a minute. The rule engine, which evaluates every 30 seconds, then reports the alert resolved, and Beacon resolves it at once. The card turns green. The alert stays in the console for 7 days, with its timeline and its sessions.

## Check it

While it fires and after it recovers:

- **In VictoriaMetrics** on the centre (`http://127.0.0.1:8428/vmui`), `probe_success{service="drill"}` has two series, both 0 while the drill runs.
- **The drill service** answers `503` to `curl -i https://status.example.com/drill/healthz`.
- **On the console**, the alert's page shows each location's result, the note and the silence. It also shows any session the agent opened, with its replay ([Review what agents did](console.md)).
- **If no alert fires after 6 minutes**, check that both locations report and that the healthz answers 503. [An alert never arrives](../self-hosting/troubleshooting.md#an-alert-never-arrives) goes further.

## Next

- [Put an agent on call](on-call.md): a hook wakes the agent for real, without a person asking.
- [Monitor a service](monitoring.md): the targets and rules a drill borrows, for your own services.
- [Publish a status page](status-pages.md): add the drill's service to a page and watch it tell your users.
