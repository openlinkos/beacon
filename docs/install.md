# Up in 30 minutes

This guide takes you from a bare Linux machine to a working on-call loop. At the end, Beacon runs on one machine with you as its owner, two probe locations check your services, an AI agent you already use (Claude Code here; Codex and others work the same way) signs in as one of your agents and runs commands on one of your servers without holding a key for it, and you have watched a rehearsed outage go from alert to recovery. It is written for someone who runs a few servers, has root on a machine with Docker, and uses an agent on a workstation.

Each step says how long it takes, what to run, what you should see, and what to do when you see something else.

| Step | Time |
| --- | --- |
| [0. What you need](#0-what-you-need) | not counted: a machine, two DNS names, your agent's SSH key |
| [1. Install the centre](#1-install-the-centre) | 3 minutes, of which the script runs for 1 to 2 |
| [2. The first probe](#2-the-first-probe) | 5 minutes for two locations |
| [3. The first machine](#3-the-first-machine) | 4 minutes |
| [4. The first agent](#4-the-first-agent) | 5 minutes, your own account included |
| [5. The first drill](#5-the-first-drill) | 10 minutes, most of it waiting for the rules |
| [6. The chat](#6-the-chat) | not counted: creating a Feishu app takes an administrator's approval |

Steps 1 to 5 take under 30 minutes once step 0 is done. DNS propagation and the chat app's approval are outside your hands, and outside the count.

## 0. What you need

Three words first. The **centre** is the machine Beacon runs on. A **probe location** is a small machine somewhere your users are, running `beacon-probe`, which checks your services every minute and sends the results to the centre. A **machine** is a server agents may work on through Beacon.

- **The centre**: Linux with 2 CPUs, 4 GB of memory and 40 GB of disk; [Docker Engine](https://docs.docker.com/engine/install/) with Docker Compose v2; the OpenSSH client (`ssh-keygen`). The whole installation uses about 200 MB of memory when idle.
- **Two DNS names** pointing at the centre: one for people and agents (`status.example.com` below: the API, MCP, the console, the status page, and the SSH channel on port 17002) and one for machines (`metrics.example.com`: the gateway the probes report to). Ports 80 and 443 must be reachable from the Internet, for Let's Encrypt and for the traffic; port 17002 from wherever your agents run.
- **An e-mail address** Let's Encrypt can write to about the certificates. It must be a real one: the installer refuses `example.com` addresses.
- **A small machine for the first probe location**: any Linux with systemd, on x86_64 or arm64, with 64 MB of memory to spare. A VPS where your users are is best. The centre itself will do for the second location.
- **A machine agents may work on**, which the centre can reach on its SSH port. The probe machine will do.
- **Your agent**: Claude Code, or anything that speaks MCP over HTTP, on a workstation with a browser, to sign it in; and its SSH public key (`~/.ssh/id_ed25519.pub` on that workstation) if you want the agent to use its own `ssh` too.

## 1. Install the centre

3 minutes. On the centre, fetch the reference deployment and look at the release it names:

```bash
git clone https://github.com/openlinkos/beacon.git
sudo cp -r beacon/deploy /opt/beacon && cd /opt/beacon
grep BEACON_VERSION .env.example
```

`BEACON_VERSION` is the release Beacon will run; the installer copies `.env.example` to `.env` on its first run. If it is not the [latest release](https://github.com/openlinkos/beacon/releases), change it in `.env.example` now. Later, to update, change it in `.env` and run the installer again, which is safe at any time. Then run it:

```bash
sudo ./install.sh
```

The script asks for the two host names and the e-mail address. To run it unattended, give them in the environment: `STATUS_HOST=… METRICS_HOST=… ACME_EMAIL=… sudo ./install.sh`. It then copies `.env.example` to `.env`, generates the secrets (the token the rule engine uses, and the SSH channel's host and CA keys, which never leave the machine), puts your names into `config.yaml` and the probe targets, validates every file with its own image, pulls the four images (Beacon, VictoriaMetrics, vmalert and Caddy), starts them, and waits until all four are healthy and the rule engine's first alert has reached Beacon. It took 69 seconds on a well-connected host; allow one to two minutes on a 2-CPU machine with ordinary bandwidth, most of it pulling images.

You should see it end like this:

```
configuration OK: Feishu app false, Pushover false, Bark false; gateway ":17006"; SSH channel :17002; 0 agent hooks
files: 1 probe locations, 0 heartbeats, 0 resources, 3 probe targets in 2 sets, 5 modules, 1 status pages
database: none yet at /var/lib/beacon/status.db; the service imports the files when it first starts
Waiting for the containers to be healthy …

Beacon is up (69s); made the secrets: rules-token ssh-host-key ssh-ca-key.
  https://status.example.com          the API (/api/v1), MCP (/mcp) and pages; the first certificate takes about a minute
  https://metrics.example.com         the gateway for machines
  status.example.com:17002            the SSH channel
```

followed by the next steps, which are the steps of this guide. Then check both host names from anywhere:

```bash
curl https://status.example.com/healthz          # ok
curl https://metrics.example.com/api/v1/targets  # {"code":"auth.required",…}: refused without a token, as it should be
```

Open `https://status.example.com/` in a browser: it is the example status page from `catalog/status_pages.yaml`, saying **No data yet** until the probes report. Nobody is notified of anything yet: without a chat configured, alerts are recorded and shown in the API, MCP and the console, which is all the next steps need.

If it does not go like this:

- **`install.sh: …` and it stops**: the message says what is missing (root, Docker, Compose v2, `ssh-keygen`, a real e-mail address, two different host names) or which file does not validate, with the command that shows why.
- **`curl` fails on TLS**: the first certificate takes up to a minute after the start. If it still fails, check that both names resolve to the centre and that ports 80 and 443 are open; `docker compose logs caddy` says what Let's Encrypt answered.

Everything else in this guide runs through `docker exec beacon beacon …`, from `/opt/beacon`. [deploy/README.md](https://github.com/openlinkos/beacon/blob/main/deploy/README.md) says where each file is.

## 2. The first probe

5 minutes for two locations. Beacon tells an outage from a hiccup by agreement: a service is down only when at least two locations fail it, and they are at least half of the locations reporting. So the first location gives you data, and the second gives you alerts.

**The first location.** The catalogue (`catalog/locations.yaml`, imported into Beacon's database at the first start) already holds one location, `edge-1`. On the centre, enrol it. The token is printed once:

```bash
docker exec beacon beacon enroll probe edge-1 > edge-1.token
```

You should see `enrolled probe-edge-1 (probe:write, expires …); the token above is not shown again`, and `edge-1.token` holds a line starting with `lbt_`. Copy `/opt/beacon/probe/` and `edge-1.token` to the probe machine and run there, as root:

```bash
sh probe/install.sh -gateway https://metrics.example.com -token-file edge-1.token
```

The script installs the release of `beacon-probe` that matches this copy of the reference deployment (the probe and the service are released together; `-version` picks another). It creates the `beacon-probe` user, downloads that release of `beacon-probe` for the machine's architecture and checks it against the release's `SHA256SUMS` (or installs the file you give with `-binary FILE`, for a machine that cannot reach GitHub), installs the token readable by that user only, the systemd unit `beacon-probe` and the `beacon-heartbeat` helper, starts the agent, and waits until the gateway has accepted its first push. That takes about 10 seconds; it ends with:

```
edge-1: beacon-probe beacon 0.3.0-rc.13 …; installed: token agent.env beacon-probe beacon-probe.service beacon-heartbeat; 13 MB resident
the gateway at https://metrics.example.com accepted the first push; the location's results arrive every minute
```

From then on the location probes every minute whatever the gateway hands it, so changing what is checked never means touching the probe again. If it says `no push accepted by … within 150 seconds`, run `journalctl -u beacon-probe -n 50` on the probe machine: a `401` means the token file is not the one `enroll` printed, a `403` with `location.inactive` that the location is not in the catalogue, and a connection error that the machine cannot reach `metrics.example.com` on port 443. A warning that the clock is not synchronised matters too: results carry timestamps, so install chrony or turn on systemd-timesyncd.

**The second location.** It can be the centre itself (nearly free) or another small machine. Add it to the catalogue file, `/opt/beacon/catalog/locations.yaml` on the centre, below `edge-1`:

```yaml
  - id: edge-2
    name: Germany - Frankfurt      # public: name places and networks, never vendors
    region: overseas
    network: BGP
```

Import the files into Beacon, first to see what would change, then for real, and enrol the new location:

```bash
docker exec beacon beacon config import /etc/beacon -dry-run
docker exec beacon beacon config import /etc/beacon -note "second probe location"
docker exec beacon beacon enroll probe edge-2 > edge-2.token
```

The dry run should list one line, `+ locations edge-2`, under `would change 1 items (nothing changed):`, and the import the same under `version 2: import of 1 items`. Then install the probe as above on the second machine. On the centre itself, point it at the gateway's own port, which the reference deployment publishes on `127.0.0.1`:

```bash
sudo sh probe/install.sh -gateway http://127.0.0.1:17006 -token-file edge-2.token
```

An import makes the files the whole configuration: anything changed through the API or MCP that the files do not hold is removed, which is why `-dry-run` comes first. [What is checked, and from where](catalog.md) explains the two ways of changing the configuration and how to keep them in step.

**Check what arrives.** VictoriaMetrics' own UI is on the centre at `http://127.0.0.1:8428/vmui` (from your workstation through `ssh -N -L 8428:127.0.0.1:8428 centre`). `probe_success` has one series per location and target, 1 for success. The public page at `https://status.example.com/` now shows its components' state. The targets shipped are Beacon's own status host, the drill (step 5) and `https://example.com/`; replace the last with a service of yours in `/opt/beacon/targets/all-web.yaml` and import again.

## 3. The first machine

4 minutes. A machine agents will work on. On the centre:

```bash
docker exec beacon beacon machine add edge-1 edge-1.example.net -note "the first probe machine"
```

Beacon connects once, pins the machine's host key (it will refuse any other key for this machine from now on), and prints what the machine needs:

```
added edge-1 at edge-1.example.net:22 as beacon; its host key is SHA256:…
On the machine, as root, if not done yet:
  echo 'ssh-ed25519 AAAA… beacon-ca' > /etc/ssh/beacon_ca.pub
  echo 'TrustedUserCAKeys /etc/ssh/beacon_ca.pub' > /etc/ssh/sshd_config.d/beacon.conf
  id beacon >/dev/null 2>&1 || useradd -m -s /bin/bash -p '*' beacon
  systemctl reload ssh
(see docs/ssh.md for sudo and for PerSourcePenaltyExemptList)
```

Run those four lines on the machine, as root. They make sshd trust Beacon's certificate authority (CA), the key that signs a short-lived certificate for each connection, and create the account those certificates name. If `machine add` fails with `the machine cannot be reached`, the centre cannot open a connection to the machine's SSH port: allow it in the machine's firewall, or, for a machine behind NAT, add it the other way round as [Machines behind NAT](ssh.md#machines-behind-nat) describes.

Two more settings are worth making now rather than later:

- **What the `beacon` account may do is the real boundary.** Give it only what the work needs, for example in `/etc/sudoers.d/beacon`: `beacon ALL=(root) NOPASSWD: /usr/bin/systemctl restart beacon-probe, /usr/bin/journalctl`.
- **Exempt the centre from OpenSSH's penalties** for failed connections: every agent arrives from the centre's address, so add `PerSourcePenaltyExemptList 203.0.113.7` (the centre's address) to `beacon.conf`.

[The SSH channel](ssh.md) has the rest.

## 4. The first agent

5 minutes. First your own account on Beacon, then Claude Code as one of your agents: it signs in through the browser, so no token is copied anywhere, and it never does more than your role allows.

**Your account.** Beacon has no account yet. When it started, it wrote a one-time setup link to its log; on the centre, print a fresh one:

```bash
docker exec beacon beacon setup
```

```
Open this link within 30 minutes (until …) to make the first account, an owner. It works once:
https://status.example.com/console/setup?code=…
```

Open the link in a browser and choose a username (`alice` below), a display name and a password of at least 10 characters. You are the first owner, signed in to the console at `https://status.example.com/console`. The link works once; everyone else joins with an invitation ([People and their agents](accounts.md#invite-people)).

**Claude Code.** On the workstation, add Beacon's MCP server, without a token:

```bash
claude mcp add --scope user --transport http beacon https://status.example.com/mcp
```

Start Claude Code, run `/mcp`, choose `beacon` and authenticate. The browser opens Beacon's consent page, **Claude Code wants to use Beacon as you**. Keep the agent name `claude-code`, choose **On call** under **What it may do** (it may work alerts and reach machines), all machines, and 30 days, then **Allow**. Back in Claude Code, `/mcp` shows `beacon` connected; `claude mcp list` says `beacon: … Connected`. Claude Code now holds an access token valid for an hour and refreshes it by itself for the 30 days you allowed. In the audit log and the chat it is `alice / claude-code`.

**Its own key.** Ask Claude Code: *"Register my SSH public key ~/.ssh/id_ed25519.pub with Beacon, list the machines Beacon reaches and run `uptime` on edge-1."* It calls `add_ssh_key`, `list_machines`, then `run_command`, and gets back the exit code, stdout and stderr as plain text, without terminal codes. Beacon signed a certificate valid for five minutes for that one connection; the agent holds no key for the machine.

The same `list_machines` answer has an `ssh_config` block and a `known_hosts` line for the agent's own ssh:

```
Host edge-1
  HostName status.example.com
  Port 17002
  User edge-1
  HostKeyAlias beacon
```

Put the block in `~/.ssh/config` and the line in `~/.ssh/known_hosts` on the workstation, and `ssh edge-1 uptime`, `scp`, `rsync -e ssh` and `ssh -L` work as usual: the user name names the machine, and on the machine the session runs as `beacon`. If ssh answers `Permission denied (publickey)`, the key it offers is not one Beacon knows: have Claude Code register it with `add_ssh_key`, or add it on the console, under **Agents**, `claude-code`, **Add an SSH key**.

Every session is on record. The console's **Agents** page shows `claude-code` with its sign-in and its key; `list_sessions` (or `GET /api/v1/sessions`) lists the sessions with each command and its exit status; and `docker exec beacon beacon ssh pause` closes them all and refuses new ones until `ssh resume`.

**An agent without a browser**, such as a CI job or the agent a hook wakes, gets a token instead: on the console under **Agents**, or on the centre with `docker exec beacon beacon agent add oncall-bot -owner alice -preset oncall`, which prints the token once with the commands that use it ([Headless agents and CI](accounts.md#headless-agents-and-ci)).

## 5. The first drill

10 minutes, most of it waiting. Beacon has a service of its own to rehearse on: `/drill/healthz` answers 200 until you say otherwise, and every location probes it as the critical service `drill` (`targets/all-drill.yaml`). The public status page does not show it.

```bash
docker exec beacon beacon drill fail 15m    # drill until …: /drill/healthz answers 503
```

What happens, with two locations:

1. **Within a minute** both locations see 503, and `probe_success{service="drill"}` is 0 for each.
2. **After 3 minutes** of failure the rules count each location as failing. Two of two is at least two and at least half, so **`ServiceDown` fires 1 minute later**, about 4 to 5 minutes after the command. It appears in `GET /api/v1/alerts` and in the MCP's `list_alerts`.
3. **Ask your agent** what is wrong. It calls `get_evidence` and gets, in one answer, each location's result and reason (`http_status`, 503), the alerts in the same failure domain, recent changes and what has been done. With `ack_alert` and a note, or `add_note`, its work goes on the alert's timeline; `silence_alert` quiets the alert.
4. **End the drill**: `docker exec beacon beacon drill off`. The probes succeed on their next round, within a minute; the rule engine, which evaluates every 30 seconds, then reports the alert resolved, and Beacon resolves it at once.

With one location nothing fires, by design: one location's word is never an outage.

**Watch it in the console** while you wait: open `https://status.example.com/console`, where you signed in at step 4 (a sign-in lasts 12 hours). The alert's page shows what each location sees, the timeline, the notes and every session on the alert's machines with a replay. [The console](pages.md) has the details.

On the centre, `beacon silence list` shows the agent's silence, and `beacon silence create -matchers 'alertname=ServiceDown,service=drill' -duration 1h -comment 'drill'` makes one from the command line, recorded in the audit log as such.

With the chat configured (step 6), the card arrives in the group about 30 seconds after the alert (`group_wait`), its buttons acknowledge and silence, and it turns green on recovery; with Pushover, an unacknowledged critical alert reaches a phone after 15 minutes.

If no alert fires after 6 minutes, check that both locations report (`probe_success{service="drill"}` in vmui has two series) and that `curl -i https://status.example.com/drill/healthz` answers `503` with `drill`. [Troubleshooting](troubleshooting.md#an-alert-never-arrives) goes further.

## 6. The chat

Not counted in the 30 minutes: a Feishu (or Lark) self-built app needs an administrator's approval, which takes as long as your organisation does. [Set up the Feishu app](feishu.md) walks through the app, its permissions, the long connection, the group and the custom bot. Then, on the centre:

1. Put each secret in its file under `/opt/beacon/secrets/`: `feishu-app-id`, `feishu-app-secret` and `feishu-webhook-url`; `pushover-user-key` and `pushover-app-token` for phones; `healthchecks-bridge-url` for the dead man's switch: Beacon pings healthchecks.io every minute while alerts can reach people, so healthchecks.io tells you when the centre itself goes quiet.
2. Uncomment the `feishu:` section (and `pushover:`, `healthchecks:`) in `/opt/beacon/config.yaml`, with your `chat_id` and the people on call.
3. Run `sudo ./install.sh` again: it gives the secret files Beacon's owner and mode and validates everything. Then restart Beacon so that it reads the new configuration: `docker compose restart beacon`. The log then says `feishu long connection ready` (`docker compose logs beacon`).

Run the drill once more and watch the card. To have a new alert wake an agent by itself, rather than wait for someone to ask it, add an `agents:` hook as [Put an agent on call](agents.md) describes.

## What to read next

- [People and their agents](accounts.md): inviting your team, roles, Codex, headless agents and their tokens, moving from tokens.
- [Put an agent on call](agents.md): waking an agent, the evidence, notes, the timeline, probing again after a fix.
- [The SSH channel](ssh.md): accounts on machines, recordings, pausing, machines behind NAT, what the channel does not do.
- [What is checked, and from where](catalog.md): locations, targets and modules, and how to change them through Beacon.
- [Troubleshooting](troubleshooting.md): what to do when something above does not happen.
- [deploy/README.md](https://github.com/openlinkos/beacon/blob/main/deploy/README.md) and [deploy/runbooks/](https://github.com/openlinkos/beacon/tree/main/deploy/runbooks): the files, and one page per alert.
- [Goals](goals.md): what Beacon is for, and what it will not do.
