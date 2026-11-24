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
2. **After 3 minutes** of failure the rules count each location as failing. Two of two is at least two and at least half, so **`ServiceDown` fires once that quorum holds for 1 minute**.
3. **After the route's group wait** (5 seconds for critical `ServiceDown` in the reference deployment), Beacon records the group, wakes matching agent hooks and sends its chat card if configured. Allow about five minutes from the command to see it in `GET /api/v1/alerts` and MCP's `list_alerts`. The card turns green on recovery. The phone escalation delay depends on your notification policy.

With one location nothing fires, by design.

## Walk the loop, you in the hook's place {#walk-the-loop}

Before wiring anything, be the hook yourself. Ask your agent: *"Is anything failing right now? Look at the evidence, check the machine, and tell me what you would do."*

It finds the alert with `list_alerts` and reads `get_evidence`. That one answer holds each location's result and reason (`http_status`, 503). The agent answers from what the probes see. A woken agent works the same way: the hook only takes the place of the person who asks.

Then let it act, one step of the loop at a time:

- **`add_note`** records the agent's findings on the timeline and card. The reference agent leaves acknowledgement and silencing to the person on call. A person's acknowledgement stops escalation while it is active. Use the console or the card to test taking charge yourself.
- **A command on a machine.** Ask the agent to run `hostname` on an [enrolled machine](machines.md) and replay it in **Sessions**. The reference drill exercises Beacon's own health endpoint and has no linked machine. For an alert with a linked machine, its page also shows related sessions.
- **`probe_now`** has every location probe the target again at once. It does not change the drill's answer: the service is still 503. But it exercises the call an agent makes after a real fix.

## End it and watch it recover {#end-it-and-watch-it-recover}

```bash
docker exec beacon beacon drill off
```

The probes succeed on their next round, usually within a minute. After the rule engine sees those results, Beacon records the recovery and the card turns green. The alert stays in the console for 7 days, with its timeline and its sessions.

## Verify phone delivery and handover {#verify-phone-delivery-and-handover}

An owner prepares a separate Beacon test instance with two reporting locations and a `public_url` reachable from the test phone. Use a dedicated [Feishu app, group and custom bot](../integrations/feishu.md), test phone destinations and credentials, and a hook scoped to `drill`. [Invite](people.md#invite-people) two people with the `oncall` role. On the Overview, confirm **Critical alert chat** and **Scheduled phone escalation** match the routes you intend to exercise. Use `escalation.critical` to check each route's delay.

1. **Receive the initial alert.** Start with a healthy drill and a test hook that accepts the request (for example, HTTP 202) without acting. Allow enough time for rule evaluation and the longest phone delay. With a 15-minute delay, run `docker exec beacon beacon drill fail 30m`. The critical `ServiceDown` card for `drill` should appear in the test group within five minutes. Open it on the phone and record the alert ID, start time and chat receipt time. Check the timeline for the hook acceptance.
2. **Receive the scheduled push.** Leave the alert firing, without silencing or acknowledging it, until all expected pushes arrive. Measure each route's delay from the initial notification. Record both the provider acceptance time in Beacon and the receipt time observed on the device. For Pushover or Bark, sign in from the push link and compare the alert ID and start time. The release's test hook, mock provider responses and provider acceptance entries each prove their own step. Count scheduled delivery when the intended device shows the notification at or after the configured delay and while the drill is still firing.
3. **Hand over and verify recovery.** In the console, the first person uses **Acknowledge with note** and writes what they saw and what still needs checking. The second person signs in separately, opens the same alert, checks the first person's note and the location failures, then uses **Take over with note**. Both names and notes should appear in the timeline. Run `docker exec beacon beacon drill off`, use **Probe again now**, and check that the probes succeed and the alert and chat card show recovery.
4. **Exercise fallback in a fresh drill.** After recovery, remove only the test app from the test Feishu group and leave its custom bot configured. With critical chat still set to the app, repeat the drill. The timeline should show the failed app submission and the group should actually receive the fallback message. If Pushover is configured, an immediate push from this failure is fallback, so record it separately from step 2. Follow the bot's link, sign in and check the alert's start time against this run. End the drill and restore the test app. After recovery, check the failed-hook path in a separate run with the app in place and the test hook returning HTTP 503. Verify the failed attempt in the timeline and actual human delivery as above.

## Check it

While it fires and after it recovers:

- **In VictoriaMetrics** on the centre (`http://127.0.0.1:8428/vmui`), `probe_success{service="drill"}` has two series, both 0 while the drill runs.
- **The drill service** answers `503` to `curl -i https://status.example.com/drill/healthz`.
- **On the console**, the alert's page shows each location's result, the note and any silence. Open the replay for the SSH check in **Sessions** ([Review what agents did](console.md)).
- **If no alert fires after 6 minutes**, check that both locations report and that the healthz answers 503. [An alert never arrives](../self-hosting/troubleshooting.md#an-alert-never-arrives) goes further.

## Next

- [Put an agent on call](on-call.md): a hook wakes the agent for real, without a person asking.
- [Monitor a service](monitoring.md): the targets and rules a drill borrows, for your own services.
- [Publish a status page](status-pages.md): add the drill's service to a page and watch it tell your users.
