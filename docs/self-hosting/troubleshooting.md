# Troubleshooting

What you see when something does not work, why, and what to do. Beacon's messages say what went wrong and usually how to put it right; the quotes below are those messages, as the command line, the logs, the API and an agent's ssh print them. The commands assume the reference deployment, run from `/opt/beacon` on the centre; elsewhere, drop `docker exec beacon` and run `beacon` itself.

Three places answer most questions:

- `docker compose logs beacon` (the service's log, in JSON lines), `docker compose logs vmalert` and, on a probe machine, `journalctl -u beacon-probe -n 50`.
- `GET /api/v1/bridge`, or the MCP tool `status_summary`: whether alerts can reach people now, and why not. On the centre, `curl http://127.0.0.1:17001/readyz` gives the same reasons without a token (the reference deployment does not publish `/readyz` through Caddy).
- VictoriaMetrics' UI, `http://127.0.0.1:8428/vmui` on the centre: what the probes have actually sent.

## Installing

### install.sh stops with a message

`install.sh` checks what it needs before it starts anything, and stops at the first problem with `install.sh: …` and the reason: `run it as root`, `Docker is not installed`, `Docker Compose v2 is missing`, `ssh-keygen is missing`, `Let's Encrypt refuses example.com addresses; use a real one`, `the two host names must differ: Caddy routes by name`. For a file that does not validate, it names the file and the command that shows why, such as `the rules do not validate: docker compose run --rm --no-deps vmalert -dryRun -rule='/etc/vmalert/rules/*.yml'`. Fix what it names and run it again; it is safe to run any number of times.

`not every container is healthy after 3 minutes` means one of the four did not start: `docker compose ps` shows which, and `docker compose logs <name>` why. `the rule engine's alerts have not reached the gateway in 2 minutes` points at vmalert: `docker compose logs vmalert`.

### The first certificate is not issued

`curl https://status.example.com/healthz` fails on TLS. The first certificate takes up to a minute after the start. After that, check that both names resolve to the centre (`dig +short status.example.com`), that ports 80 and 443 are open from the Internet, and what Let's Encrypt answered: `docker compose logs caddy`.

### A beacon command cannot reach the running service

A command such as `beacon token list` fails with `cannot reach the running service at /var/lib/beacon/ctl.sock: …` and `(run this inside the container: docker exec beacon beacon …)`. The command line talks to the service through an admin socket in the data directory, which exists only where the service runs. In the reference deployment that is inside the container: run `docker exec beacon beacon token list`. Elsewhere, run it as the service's user or as root (the socket refuses everyone else), and set `BEACON_DATA_DIR` when the data directory is not `/var/lib/beacon`.

### A change to config.yaml does not take effect

Beacon reads `config.yaml` once, when it starts. After changing it (the chat, the hooks, the SSH channel), validate and restart: `sudo ./install.sh` gives new secret files their owner and mode and validates everything, then `docker compose restart beacon`. A `SIGHUP` does not reread `config.yaml`: it only imports the catalogue, targets and module files ([below](#an-import-or-sighup-did-not-change-the-configuration)).

## Probes

### A probe location shows no data

The location has no `probe_success` series in vmui, the console's overview counts it as expected but not reporting, or `ProbeLocationDown` fired (a location that has sent nothing for 10 minutes). On the probe machine, `journalctl -u beacon-probe -n 50` says why:

| In the log | Why | What to do |
| --- | --- | --- |
| `401`, `auth.invalid` | The token file is not the one `beacon enroll probe` printed, or it was replaced with `-rotate` | Enrol again with `-rotate` and run `probe/install.sh -token-file FILE` with the new token |
| `403`, `location.inactive`, `location edge-2 is not in the catalogue` | The location's id is not in the catalogue, or was removed by an import of files that lack it | Add it to the catalogue and import, or put it through the API ([What is checked, and from where](../guide/monitoring.md)) |
| `403`, `the machine of location edge-2 has expired` | A resource of kind `vps` with the location's name has a date in the past | Renew the resource with a new date |
| `cannot fetch from the gateway; using the cached copy` with a connection error | The machine cannot reach `metrics.example.com` on 443 (or the gateway's own port) | Check DNS, the firewall and the address in `/etc/beacon-probe/agent.env` |

Enrolling also checks the catalogue: `beacon enroll probe edge-2` fails with `location edge-2 is not in the catalogue` until it is there, and with `probe-edge-2 is enrolled already; enroll it with -rotate to replace its token` the second time.

A location reports but one of your targets is missing from it: the target set's name decides who probes it. `all-…` is every location, `overseas-…` and `mainland-…` those of that region, `<location id>-…` that location only ([Targets](../guide/monitoring.md#targets)). A probe picks up a change within a minute.

If results arrive late or in bursts, check the probe machine's clock: results carry timestamps, and `probe/install.sh` warns when the clock is not synchronised with NTP.

### A target always fails

`probe_success` is 0 for a target that works in your browser. The reason is in `probe_failure_info{reason}` and in the evidence: `dns`, `connect`, `timeout`, `tls`, `not_ssl`, `http_status`, `body`, `banner`, `protocol`, `auth`, `manifest` or `config`. The probe also logs each failure as `probe failed`, with the module, the target, the reason and a detail.

To see it for yourself, run the probe once by hand on the probe machine, with the module file it was handed:

```bash
sudo -u beacon-probe /opt/beacon-probe/bin/beacon-probe run -modules /var/lib/beacon-probe/state/modules.yml http_head https://example.com/
```

It prints the result's metrics and, when it fails, `failed: <reason>: <detail>`. Common causes: a module that expects 200 where the service redirects (`http_status`; `follow_redirects` and `valid_status_codes`), a `HEAD` the service refuses, a module that names a `password_file` the probe's secrets directory does not hold (`config`), and a path that is slow from that location only (`timeout`; the location's `probe.timeout` and `probe.retries`).

## Alerts

### An alert never arrives

You expected an alert, and `GET /api/v1/alerts` (or `list_alerts`) has none. Follow the alert's path:

1. **Do enough locations fail?** A service is down only when at least two locations fail it, and they are at least half of those reporting; one location's failure never fires anything, by design. In vmui, `probe_success{service="…"}` shows each location's results.
2. **Long enough?** A location counts as failing after 3 minutes of failures, and `ServiceDown` fires after 1 more minute: 4 to 5 minutes after the service broke. `ServiceDegraded` (at least two locations but fewer than half) and `MainlandPathDegraded` wait 5 minutes.
3. **Does the rule engine reach Beacon?** `GET /api/v1/bridge` says `no Watchdog from the rule engine since start` or `the Watchdog from the rule engine stopped` when vmalert's alerts do not arrive. Then `docker compose logs vmalert`: its notifier must point at the gateway (`-notifier.url=http://beacon:17006`) with the rules token (`rules.token_file`), or a token with `alerts:send`.
4. **Is it muted?** A silence matching the alert keeps it from notifying: `beacon silence list`. A maintenance window makes such silences, with comments that begin with `maintenance:`. An inhibition rule mutes an alert while another fires; in the reference deployment, `ServiceDown` mutes `ServiceDegraded` and `MainlandPathDegraded` for the same service.

For the drill, `curl -i https://status.example.com/drill/healthz` must answer `503` with `drill` while it runs; `beacon drill status` says until when.

### Nobody is told of an alert

The alert is in `list_alerts`, but no card, no message and no phone call came.

- **No channel configured.** Without a `feishu:` section or `pushover:`, alerts are recorded and shown, and nothing else: the log says `alert recorded; no notification channel is configured`. [Set up the Feishu app](../integrations/feishu.md), then restart Beacon.
- **A channel failed.** The log says `feishu card failed; falling back` or `alert could not be delivered by any channel`, and `GET /api/v1/bridge` lists `the Feishu long connection is down`, `a Feishu API call failed in the last 10 minutes` or `the Feishu API budget for this month is used up`.
- **It went to the custom bot.** `info` alerts go to the group's custom bot, as plain messages, not cards; so do warnings once the app has used 70% of `feishu.monthly_budget`.
- **No phone call yet.** A critical alert reaches Pushover after 15 minutes without an acknowledgement, and buzzes the people on call in the Feishu app after 10. An acknowledgement stops both for `ack_ttl` (4 hours by default).

### The agent is not woken

An alert fired, and the hook's agent did nothing. Look at the alert's calls first: `agents` in `get_alert` gives each call's status, HTTP status, attempts and error, and the console's **Hooks** page lists every call.

- **No call at all.** A hook is called only for the severities it lists (`[critical]` by default) and, with `service`, only when the regular expression matches the whole `service` label. It is called when a new alert group is first notified, so a group that was already firing when you added the hook does not wake it. Hooks are read from `config.yaml`, so a new or changed hook needs a restart.
- **401 from `beacon-oncall`.** Its log says `refused` with `reason` `bad signature` (the harness's `-secret-file` and the hook's `secret_file` differ), `no signature`, or `stale delivery` (the payload's `at` is more than 15 minutes older than the harness's clock: check both clocks).
- **404 or a connection error.** The hook's `url` does not reach the harness: check the reverse proxy in front of it, and `-listen` (`:17007` by default). On the harness's machine, `curl http://127.0.0.1:17007/healthz` answers `ok` while it runs.
- **202 and still nothing.** The harness took the call; what happened next is in the run's log under `-log-dir`, one file per delivery, ending with the program's exit status. A program that cannot start, or an agent that is not signed in as the service's user, shows there.

## Agents and machines

### The agent cannot reach a machine

The agent's ssh fails, or a tool such as `run_command` returns an error. The channel says why: on ssh, a line starting with `beacon:` and exit status 1; on MCP and the API, an error code.

| The agent sees | Why | What to do |
| --- | --- | --- |
| `Permission denied (publickey)` | The key ssh offers belongs to no agent or token that may use the channel now: it was never registered, the agent has no credential that works, its preset gives no `machines:ssh`, or its owner is disabled | Have the agent register it with `add_ssh_key`, or add it on the agent's page (**Add an SSH key**); check the agent's page for a working credential and its preset |
| `beacon: this agent may not reach the machine: the agent lacks the machines:ssh scope` (`machine.forbidden`) | The agent's preset, or its owner's role, gives no `machines:ssh` | Give the agent the On call or Operator preset on its page, within its owner's role; for a token of its own, create one with that scope |
| `beacon: no such machine called "edge-3"; machines this agent reaches: edge-1, edge-2` (`machine.not_found`) | The user name is not a machine's name | `beacon machine list`; the user name in ssh is the machine's name |
| `beacon: this agent may not reach the machine edge-2; machines this agent reaches: edge-1` (`machine.forbidden`) | The agent, or the OAuth sign-in it uses, is limited to other machines | Add the machine on the agent's page; for an OAuth sign-in, sign in again from the client and tick it; for a token of its own, `beacon token machines NAME edge-1,edge-2`, or `all` |
| `beacon: agents' access to machines is paused since …` (`ssh.paused`) | Someone paused the channel | `beacon ssh resume`, once whoever paused it agrees |
| `beacon: the machine cannot be reached: edge-1: dial tcp …` (`machine.unreachable`) | Beacon cannot open a connection to the machine's SSH port | The machine is down, or its firewall does not let the centre in |
| `… ssh: handshake failed: ssh: unable to authenticate …` (`machine.unreachable`) | The machine's sshd does not accept Beacon's certificate | Run the lines `machine add` printed: `/etc/ssh/beacon_ca.pub`, `TrustedUserCAKeys`, the `beacon` account, `systemctl reload ssh` |
| `… ssh: handshake failed: ssh: host key mismatch` (`machine.unreachable`) | The machine's host key is not the one Beacon pinned, after a reinstall for instance | Check why it changed, then `beacon machine add NAME HOST -replace` |
| `beacon: the machine's connector is not connected; …` (`machine.disconnected`) | A machine behind NAT is not connected to the channel | On the machine, `systemctl status beacon-connector`; `beacon machine list` shows `disconnected` |
| `beacon: the SSH channel is not configured` (`ssh.disabled`) | `ssh.ca_key_file` is not set | [Turn it on](../guide/machines.md#turn-it-on) |

Two more cases. When connections to one machine start failing for every agent at once after a run of failed attempts, OpenSSH's penalties (9.8 and later) may have caught the centre's address, from which every agent arrives: add `PerSourcePenaltyExemptList` with the centre's address to the machine's `beacon.conf` ([Add a machine](../guide/machines.md#add-a-machine)). And after 20 failed sign-ins within 10 minutes from one address, the channel closes that address's connections without an answer for the rest of those 10 minutes.

### The agent's credential is refused

The API or MCP answers 401 or 403, or the MCP client says it cannot connect, or asks to sign in again.

- **`auth.invalid`, `the token is unknown or has expired`.** The token expired, was revoked, or is mistyped; or the agent was deleted, or its owner disabled. `beacon token list` shows each token's expiry and last renewal, and the agent's page its credentials. Make a new token with **New token** on the agent's page (`beacon token create` for a token of its own), and give the agent a way to renew it ([Tokens that renew themselves](../guide/on-call.md#tokens-that-renew-themselves)).
- **An OAuth client asks to sign in again.** Its sign-in was revoked, or the time allowed on the consent page is up: sign in again from the client (`/mcp` in Claude Code, `codex mcp login beacon`).
- **A renewed token that the client no longer has.** A client registered with a fixed header, as `claude mcp add --header` does, keeps the old secret, which stops working ten minutes after a renewal: add it again with the new secret. The wrappers in `contrib/oncall` read the token file on every connection instead.
- **`auth.scope`.** The route or tool needs a scope the credential does not have. The message names it: `the token lacks the … scope` for a token of its own, and for an agent what its preset within its owner's role allows. Change the agent's preset on its page, or ask an owner for a role that allows it ([Scopes](../reference/api.md#scopes)).
- **`auth.too_many_failures`.** 20 failed attempts from one address within 10 minutes block it for the rest of those 10 minutes. The message says how long is left and the response carries `Retry-After`. The REST API and the gateway both answer this. `POST /oauth/token` answers 429 `too_many_requests` (the OAuth error code) with the same wait in `error_description` and the same header. A client retrying with a wrong token causes it. MCP answers the same lockout with a 401, because its library has no 429: the text names the wait, and the response carries `Retry-After`.
- **`token.too_soon` (429) on a renewal.** The token renewed itself less than 24 hours ago and is still good.

### probe_now names locations as missing

`probe_now` returns with locations under `missing`, or `complete` is false. A location is missing when it did not answer within `wait`: its probe is down (see [A probe location shows no data](#a-probe-location-shows-no-data)), or it runs a `beacon-probe` older than probing on demand. `probe.busy` (429) means the target was probed on demand less than 15 seconds ago or the token has three requests in flight; wait and ask again. `probe.not_found` (404) means no active location probes that target: check the target's spelling and its set's name.

## The console and status pages

### Signing in to the console fails

The sign-in form at `/console/sign-in` says why:

- `Wrong username or password.`: one of the two is wrong; the answer is the same for both. A lost password takes a reset link from an owner (**People**, or `beacon user reset NAME`).
- `This account is disabled; an owner can enable it.`: an owner enables it on **People**, or with `beacon user enable NAME`.
- `Too many failed sign-ins; try again in N minutes.`: 10 wrong passwords for this username within 10 minutes, from anywhere. N is how many minutes of that window are left, rounded up, and at least one (`a minute` when only one is left).
- `Too many failed sign-ins from here; try again in N minutes.`: 20 failures from your address within 10 minutes. N is named the same way.
- `The form has expired; try again.`, again and again: the browser did not keep the sign-in form's cookie. The console's cookies are `Secure`, so it must be opened over HTTPS (or on `localhost`); over plain HTTP the form never works. Open it through the TLS proxy at `public_url`.
- With a token instead: `The token is unknown or has expired.` (the token is mistyped, expired or revoked), or `Token NAME lacks status:read, which the console needs to show anything.` (make a token with `status:read`).

Nobody has an account yet? The sign-in page then offers only a token, and says so: the setup link is in Beacon's log, and `beacon setup` prints a new one ([The first owner](../guide/people.md#the-first-owner)). A setup link that says `This link has expired. Run beacon setup to make a new one.` has run out of its 30 minutes: run `beacon setup` again. There is no owner to ask yet. An invitation or a reset link that says `This link has expired; ask an owner for a new one.` has run out of its time (24 hours, or 30 minutes for a reset); an owner makes another. One that says `This link does not work: it was used already, or it is wrong.` was spent or mistyped. An expired link still says so for 7 days after a newer one is made; after that it is forgotten and reads as used or wrong. [The console](../guide/console.md#signing-in) has the details.

### The console says 404 on an old link

A link from the chat, a card or an agent's note opens a page that says it was not found. Every failure of a signed link is the same plain 404, so that a wrong link reveals nothing; the usual reasons:

- **The link has expired.** Links last `pages.link_ttl`, 30 days by default. Sign in and find the alert under `/console/alerts`, or the session under `/console/sessions`.
- **The alert is gone.** Beacon keeps alert groups for 7 days after they resolve; after that their page is gone, signed in or not. Its sessions stay for `ssh.keep`, its notes are in the audit log, and an incident on a status page stays for 91 days.
- **The link was cut.** The signature is the `?s=…` part at the end; a link copied without it, or shortened, fails.
- **It was opened on a status page's own host name.** Only the status page answers there; the console is at `public_url`.
- **The database was replaced.** Links are signed with a key Beacon keeps in its database; a new database signs with a new key, and older links fail.

Links from before 0.3.0, which name `/alerts/{id}` or `/sessions/{id}` at the site's root, still work until they expire: they are sent on (`308`) to the same page under `/console` ([Signed links](../guide/console.md#signed-links)).

### A status page shows no data

A component, or the whole page, says **No data yet**, and its bar has grey days. The page shows what the probes agree on, minute by minute, so no data means no probe result for the component's services:

- **The component names a service no target has.** `beacon check-config`, and the change that added the page, warn: `status page "public": component "website" names service "example.com", which no probe target has; it shows no data until one does`. The names must match the targets' `service` label exactly.
- **The component names a `page` label that only internal targets carry.** Targets labelled `visibility: internal` never back a component through `page`; the warning says `… which no public probe target carries …`.
- **No location reports.** The probes are down, or cannot reach the gateway: see [A probe location shows no data](#a-probe-location-shows-no-data).
- **VictoriaMetrics does not answer.** The log says `status pages: reading the probes failed`.

Days before the probes started reporting stay grey; a past day is computed again until six hours after it ended, for results that probes queued while their path was down. [Status pages](../guide/status-pages.md) has the rules.

## Configuration

### An import or SIGHUP did not change the configuration

You edited the catalogue or target files and the change is not in force.

- **The files are not read by themselves.** After the first start, the configuration lives in Beacon's database. Import the files: `docker exec beacon beacon config import /etc/beacon -dry-run`, then without `-dry-run`.
- **SIGHUP refused.** A `SIGHUP` imports the files only when nothing was changed through Beacon since the last import, so that files cannot silently undo what people and agents changed. The log then says `SIGHUP: the configuration files were not imported; keeping the configuration in force`, with `… changed through Beacon since the last import (version 5: put by claude-code via mcp); keep it with beacon config export DIR, or replace it with beacon config import DIR`. Do one of the two.
- **The files fail their checks.** Then nothing changes, and the message says why; `beacon check-config` checks the files and the database.

`nothing to change: version N holds this already` means the files and the database already agree.

### A change is refused with config.conflict or config.invalid

`config.conflict` (409): the change was based on an older version (`if_version`) and a later one is in force, or a revert touches an item a later change touched. Read the item again (`GET /api/v1/config`, `get_config`) and decide afresh. `config.invalid` (422): the change would not pass the checks, and the message says which; nothing changed. A common one: a location cannot go while a target set is named after it alone. [What is checked, and from where](../guide/monitoring.md) has the rules.
