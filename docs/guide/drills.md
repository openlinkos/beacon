# Rehearse an outage

By the end of this page you have watched the whole on-call loop happen on purpose, without touching a real service:

- the probes see a failure, and the rules fire;
- the test destination receives a notification;
- your agent reads the evidence, works a machine, has its fix checked and leaves a note;
- the alert resolves on what the probes then see.

A drill is how you find out that a phone is not reachable or a hook is misconfigured. You find out while everything still works, rather than at 3 a.m.

You need:

- Beacon running, with at least two probe locations reporting. One location's word is never an outage.
- An agent connected for the agent steps ([Put an agent on call](on-call.md)). For automatic wake, start its hook receiver before the drill.
- For delivery checks, a test [Slack channel](../integrations/slack.md), [notification receiver](../integrations/notification-webhook.md) or [Feishu group](../integrations/feishu.md). With no destination configured, follow the incident in the console.

From nothing, follow [On a server](../self-hosting/install.md), steps 1 to 4. Or use [Try it on your laptop](quickstart.md) for the whole loop on one machine. The commands run on the centre, as `docker exec beacon beacon …` in the reference deployment.

## The drill service {#the-drill-service}

Beacon has a service of its own to rehearse on. `/drill/healthz` answers 200 until you say otherwise. Every location probes it as the critical service `drill` (`targets/all-drill.yaml`). Keep `drill` out of the components on any page users can see.

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
2. **After 3 minutes** of failure the rules count each location as failing. Two of two is at least two and at least half, so **`ServiceDown` fires once that quorum holds for 40 seconds**.
3. **After the route's group wait** (5 seconds for critical `ServiceDown` in the reference deployment), Beacon records the group, wakes matching agent hooks and submits notifications to the configured channel. Allow about five minutes from the command to see the incident in **Alerts**, `GET /api/v1/alerts` or MCP's `list_alerts`. Phone escalation follows your configured delay.

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

The probes succeed on their next round, usually within a minute. After the rule engine sees those results, Beacon records recovery. Slack and the signed notification webhook send a separate recovery message. A Feishu app card updates in place. The alert stays in the console for 7 days, with its timeline and its sessions.

## Verify phone delivery and handover {#verify-phone-delivery-and-handover}

An owner prepares a separate Beacon test instance with two reporting locations and a `public_url` reachable from the test phone. Use only test destinations and credentials, [invite](people.md#invite-people) two people with the `oncall` role, and scope the test hook with `service: '^drill$'`. For a phone-only drill, set `escalation.critical.channel: none` in `config.yaml` and configure the phone route and its delay. This example uses a test [Bark endpoint](../reference/config.md#pushover-bark-and-healthchecksio) in `bark.url_file`.

```yaml
escalation:
  critical: {channel: none, bark: 20s}
```

If the file already has an `escalation` block, update its `critical` entry. Run `sudo ./install.sh` from `/opt/beacon` to apply the settings. In **Overview → Critical notifications**, confirm the expected initial route and phone delay.

1. **Find the initial incident.** Start with a healthy drill and a test hook that accepts the request (for example, HTTP 202) without acting. Allow enough time for the rules and the longest phone delay. For a 15-minute delay, run `docker exec beacon beacon drill fail 30m`. Within about five minutes, look for the critical `ServiceDown` for `drill` in **Alerts**, or open the notification if a test chat is also configured. Record the alert ID and start time, and check the timeline for hook acceptance. To rehearse a failure after the hook accepts the request, use a harness [with execution reporting](../integrations/headless.md#report-programme-execution) and a test programme that exits with a nonzero code. Check for both hook acceptance and the failed execution result.
2. **Wait for the scheduled push.** Leave the incident firing and unacknowledged until the expected pushes arrive. Record the provider acceptance in the timeline and, when testing with a phone, the time the notification appeared on the device. Sign in from the Pushover or Bark link and compare the alert ID and start time. A test receiver returning `2xx` verifies Beacon's submission and the incident link. A phone delivery passes when the intended device displays the notification while the incident is still firing.
3. **Hand over and verify recovery.** The first person uses **Acknowledge with note** and writes what they saw and what still needs checking. The second person signs in separately, opens the same alert, checks the first person's note and the location failures, then uses **Take over with note**. Both names and notes should appear in the timeline. Run `docker exec beacon beacon drill off`, use **Probe again now**, and check that the probes and incident show recovery. If you configured a chat route, check recovery there as well.

### Verify Feishu fallback {#verify-feishu-fallback}

Use a separate run with the test [Feishu app and custom group bot](../integrations/feishu.md) configured. After recovery, remove the test app from the test group and keep its custom bot. Confirm the critical route is still `app` and repeat the drill. Check for the failed app submission in the timeline and the fallback message in the group. An immediate Pushover submission from this failure is fallback and has its own timeline entry. Follow the bot's link, sign in, and check that the alert's start time belongs to this run. End the drill and restore the test app. To check a hook rejecting the wake itself, run a separate drill with the test app in place and the test hook returning HTTP 503. Check the failed attempt and human delivery.

## Check it

While it fires and after it recovers:

- **In VictoriaMetrics** on the centre (`http://127.0.0.1:8428/vmui`), `probe_success{service="drill"}` has two series, both 0 while the drill runs.
- **The drill service** answers `503` to `curl -i https://status.example.com/drill/healthz`.
- **On the console**, the alert's page shows each location's result, the note and any silence. Open the replay for the SSH check in **Sessions** ([Review what agents did](console.md)).
- **If no alert fires after 6 minutes**, check that both locations report and that the healthz answers 503. [An alert never arrives](../self-hosting/troubleshooting.md#an-alert-never-arrives) goes further.

## Next

- [Put an agent on call](on-call.md): a hook wakes the agent for real, without a person asking.
- [Monitor a service](monitoring.md): the targets and rules a drill borrows, for your own services.
- [Publish a status page](status-pages.md): choose which monitored services your users see.
