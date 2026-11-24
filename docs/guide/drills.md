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

- **`add_note`** records the agent's findings on the timeline and card. The reference agent leaves acknowledgement and silencing to the person on call. Acknowledging stops escalation while the acknowledgement is active. Use the console or the card to test taking charge yourself.
- **A command on the machine.** If the drill's machine is one the [SSH channel](machines.md) reaches, have the agent run a command on it. The session lands on the alert's page with its replay.
- **`probe_now`** has every location probe the target again at once. It does not change the drill's answer: the service is still 503. But it exercises the call an agent makes after a real fix.

## End it and watch it recover {#end-it-and-watch-it-recover}

```bash
docker exec beacon beacon drill off
```

The probes succeed on their next round, within a minute. The rule engine, which evaluates every 30 seconds, then reports the alert resolved, and Beacon resolves it at once. The card turns green. The alert stays in the console for 7 days, with its timeline and its sessions.

## Verify phone delivery and handover {#verify-phone-delivery-and-handover}

Use a test instance or approved drill destinations and two separate on-call accounts. Check **Critical alert chat** and **Scheduled phone escalation** on the Overview. These show the configuration. Record receipt from the chat and the phone during the drill.

1. Use a drill-scoped hook whose receiver accepts the alert and does no further work. In a separate run, return a failure from the receiver to check the other takeover path.
2. Start the drill for long enough to exceed both rule evaluation and the phone delay. With the reference rules and a 15-minute phone delay, use `docker exec beacon beacon drill fail 30m`. Leave it firing and unacknowledged until the expected phone notification is received.
3. Open the alert from its chat card on the phone. Sign in as the first operator, take ownership in the console and write a handover note. Record which route arrived, the time and what you still need to check.
4. Open the same alert as the second operator. Check the previous owner's note, the per-location failures and the hook outcome. End the drill with `docker exec beacon beacon drill off` and run **Probe again now**. Check that the probes succeed and that recovery appears on the alert and the chat card.

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
