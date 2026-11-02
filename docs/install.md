# Up in 30 minutes

English | [简体中文](zh/install.md)

At the end of this page Beacon runs on one machine of yours, a probe reports from a second one, an agent you already use (Claude Code here; Codex or another works the same) reaches a machine through Beacon without holding a key, and you have watched one rehearsed outage go from alert to recovery. Each step says how long it takes; the budget below is honest about what is in it and what is not.

| Step | Time |
| --- | --- |
| [0. What you need](#0-what-you-need) | not counted: a machine, two DNS names, your agent's SSH key |
| [1. Install the centre](#1-install-the-centre) | 3 minutes, of which the script runs for 1 to 2 |
| [2. The first probe](#2-the-first-probe) | 5 minutes for two locations |
| [3. The first machine](#3-the-first-machine) | 4 minutes |
| [4. The first agent](#4-the-first-agent) | 5 minutes |
| [5. The first drill](#5-the-first-drill) | 10 minutes, most of it waiting for the rules |
| [6. The chat](#6-the-chat) | not counted: creating a Feishu app takes an administrator's approval |

Steps 1 to 5 come to under 30 minutes once step 0 is done. DNS propagation and the chat app are outside your hands and outside the count.

## 0. What you need

- **The centre**: a Linux machine with 2 CPUs, 4 GB of memory and 40 GB of disk, with [Docker Engine](https://docs.docker.com/engine/install/) and Docker Compose v2 installed, and the OpenSSH client (`ssh-keygen`). The whole installation uses about 200 MB of memory when idle.
- **Two DNS names** pointing at it: one for people and agents (`status.example.com` below: the API, MCP, the pages, and the SSH channel on port 17002) and one for machines (`metrics.example.com`: the gateway). Ports 80 and 443 must be reachable from the Internet, for Let's Encrypt and for the traffic; port 17002 from wherever your agents run.
- **An address** Let's Encrypt can write to about certificates.
- **A small second machine** for the first probe location: any Linux with systemd on x86_64 or arm64, with 64 MB of memory to spare. A VPS where your users are is best; the centre itself will do for a second location.
- **A machine agents may work on**, reachable by SSH from the centre; the probe machine will do.
- **Your agent program** with an SSH key (`~/.ssh/id_ed25519.pub`) and, for the MCP, Claude Code or anything that speaks MCP over HTTP.

## 1. Install the centre

On the centre, 3 minutes:

```bash
git clone https://github.com/openlinkos/beacon.git
sudo cp -r beacon/deploy /opt/beacon && cd /opt/beacon
sudo ./install.sh
```

The script asks for the two host names and the address (or takes them from the environment: `STATUS_HOST=… METRICS_HOST=… ACME_EMAIL=… sudo ./install.sh`), then copies `.env.example` to `.env`, generates the secrets (the token the rule engine uses, and the SSH channel's host and CA keys, which never leave the machine), puts your names into `config.yaml` and the probe targets, validates every file with its own image, pulls the four images, starts them, waits until all four are healthy and the rule engine's first alert has reached the gateway, and prints the next steps. Measured at 69 seconds on a well-connected host, pulls included; allow one to two minutes on a 2-CPU machine with ordinary bandwidth, most of it the pulls.

Check: `curl https://status.example.com/healthz` answers `ok` (the first certificate takes up to a minute after the start), and `curl https://metrics.example.com/api/v1/targets` answers 401, as it should without a token. Nothing notifies anyone yet: without a chat configured, alerts are recorded and shown in the API and MCP, which is enough for the steps below.

Everything else happens through `docker exec linkos-beacon linkos-beacon …`; [deploy/README.md](../deploy/README.md) says where each file is.

## 2. The first probe

Beacon tells an outage from a hiccup by agreement: a service is down only when at least two locations, and at least half of those reporting, fail it. So the first probe gives you data, and the second gives you alerts. 5 minutes for both.

The catalogue (`catalog/locations.yaml`, imported at the first start) holds one location, `edge-1`. On the centre, enrol it:

```bash
docker exec linkos-beacon linkos-beacon enroll probe edge-1 > edge-1.token     # printed once, never again
```

Copy `/opt/beacon/probe/` and the token to the probe machine, and there, as root:

```bash
sh probe/install.sh -gateway https://metrics.example.com -token-file edge-1.token
```

It creates the `linkos-agent` user, downloads the `linkos-probe` release for the machine's architecture and checks it against the release's `SHA256SUMS` (or takes `-binary FILE`), installs the token readable by that user only, the systemd unit `linkos-probe` and the `linkos-heartbeat` helper, starts the agent and waits until the gateway has accepted its first push: about 10 seconds, 13 MB resident. From then on the location probes every minute whatever the gateway hands it, so changing targets never means touching the probe again.

The second location can be the centre itself (nearly free) or another small machine. Add it to the catalogue, with a token for the API that may change the configuration, then enrol and install as above:

```bash
docker exec linkos-beacon linkos-beacon token create setup -scopes config:write -expires 1d > setup.token
curl -X PUT -H "Authorization: Bearer $(cat setup.token)" -H "Content-Type: application/json" \
  https://status.example.com/api/v1/config/locations/centre \
  -d '{"name": "Germany - Frankfurt", "region": "overseas", "network": "BGP"}'
docker exec linkos-beacon linkos-beacon enroll probe centre > centre.token
sudo sh probe/install.sh -gateway http://127.0.0.1:17006 -token-file centre.token   # on the centre: the gateway's own port
docker exec linkos-beacon linkos-beacon token revoke setup
```

Name locations by place and network, never by vendor: the name is public. Check what arrives: VictoriaMetrics' own UI is on the centre at `http://127.0.0.1:8428/vmui` (from your workstation through `ssh -N -L 8428:127.0.0.1:8428 centre`); `probe_success` has one series per location and target. The two targets shipped are Beacon's own status host and `https://example.com/`; replace the latter with a service of yours in `targets/all-web.yaml` and `linkos-beacon config import /etc/linkos-beacon`, or `PUT /api/v1/config/targets/all-web` as in [docs/catalog.md](catalog.md).

## 3. The first machine

A machine agents will work on, 4 minutes. On the centre:

```bash
docker exec linkos-beacon linkos-beacon machine add edge-1 edge-1.example.net -note "the first probe machine"
```

Beacon connects once, pins the machine's host key, and prints what the machine needs: its sshd must trust Beacon's certificate authority, and the account the certificates name must exist. On the machine, as root, the four lines it printed:

```bash
echo 'ssh-ed25519 AAAA… beacon-ca' > /etc/ssh/beacon_ca.pub
echo 'TrustedUserCAKeys /etc/ssh/beacon_ca.pub' > /etc/ssh/sshd_config.d/beacon.conf
useradd -m -s /bin/bash -p '*' beacon
systemctl reload ssh
```

Two more lines are worth it now rather than later: what the `beacon` account may do is the real boundary, so give it only what the work needs (`/etc/sudoers.d/beacon`: `beacon ALL=(root) NOPASSWD: /usr/bin/systemctl restart linkos-probe, /usr/bin/journalctl`), and exempt the centre's address from OpenSSH's penalties for failed connections (`PerSourcePenaltyExemptList 203.0.113.7` in `beacon.conf`), since every agent arrives from it. [docs/ssh.md](ssh.md) has the rest.

## 4. The first agent

5 minutes. One token works for the API, MCP and the SSH channel; give it the agent's public key so its own `ssh` works too:

```bash
docker exec linkos-beacon linkos-beacon token create claude-code \
  -scopes status:read,alerts:write,machines:ssh,sessions:read -expires 90d \
  -ssh-key "$(cat ~/.ssh/id_ed25519.pub)"
```

The secret is printed once. On the workstation where Claude Code runs:

```bash
claude mcp add --transport http linkos-beacon https://status.example.com/mcp --header "Authorization: Bearer lbt_…"
```

Then, in Claude Code, ask it to list the machines and run `uptime` on `edge-1`. It calls `list_machines`, then `run_command`; the result comes back as exit code, stdout and stderr, without terminal codes. The same `list_machines` answer carries an `ssh_config` block and a `known_hosts` line: put them in `~/.ssh/config` and `~/.ssh/known_hosts`, and `ssh edge-1 uptime`, `scp`, `rsync -e ssh` and `ssh -L` work as usual, the user name naming the machine. Beacon signed a certificate valid for five minutes for that connection; the agent holds no key for the machine.

Every session is on record: `docker exec linkos-beacon linkos-beacon token list` shows the token, `list_sessions` (or `GET /api/v1/sessions`) the sessions, with each command and its exit status. `linkos-beacon ssh pause` closes them all until `ssh resume`.

## 5. The first drill

10 minutes, most of it waiting. Beacon has an internal service to rehearse on: `/drill/healthz` answers 200 until you say otherwise, and every location probes it (`targets/all-drill.yaml`, service `drill`, critical).

```bash
docker exec linkos-beacon linkos-beacon drill fail 15m
```

What to expect, with two locations:

- within 1 minute both probes see 503, and `probe_success{service="drill"}` is 0 for each;
- after 3 minutes of failure the rules count each location as failing; both failing is at least two and at least half, so `ServiceDown` fires 1 minute later: about 4 to 5 minutes after the command;
- the alert shows up in `GET /api/v1/alerts` and in the MCP's `list_alerts`. Ask your agent what is wrong: `get_evidence` returns each location's result and reason (`http_status`), the alerts in the same failure domain, recent changes and what has been done; `ack_alert` with a note, and `add_note`, put its work on the alert's timeline; `silence_alert` quiets it. On the centre, `linkos-beacon silence list` shows the silence, and `linkos-beacon silence create -matchers 'alertname=ServiceDown,service=drill' -duration 1h -comment 'drill'` would make one from the command line, audited as such;
- `docker exec linkos-beacon linkos-beacon drill off`; the probes succeed on their next round, and the alert resolves within `resolve_timeout` (5 minutes) after the rule engine stops sending it.

With one location nothing fires, by design: one location's word is never an outage. With the chat configured (step 6), the card arrives in the group about 4 minutes after the command, its buttons acknowledge and silence, and it turns green on recovery; with Pushover, an unacknowledged critical alert reaches a phone after 15 minutes.

## 6. The chat

Not counted in the 30 minutes: a Feishu (or Lark) self-built app needs an administrator's approval, which takes as long as your organisation does. [docs/feishu.md](feishu.md) walks through the app, its permissions, the long connection, the group and the custom bot. Then, on the centre:

1. Put each secret in its file under `/opt/beacon/secrets/` (`feishu-app-id`, `feishu-app-secret`, `feishu-webhook-url`; `pushover-user-key` and `pushover-app-token` for phones; `healthchecks-bridge-url` for the dead man's switch).
2. Uncomment the `feishu:` section (and `pushover:`, `healthchecks:`) in `config.yaml`, with your `chat_id` and the people on call.
3. `sudo ./install.sh` again: it fixes the files' owner and mode, validates and restarts.

Then run the drill once more and watch the card. To wake an agent on a new alert rather than wait for someone to ask it, add an `agents:` hook as [docs/agents.md](agents.md) describes.

## What to read next

- [docs/agents.md](agents.md): waking agents, the evidence, notes and the timeline.
- [docs/ssh.md](ssh.md): the SSH channel in full: accounts, recordings, pausing, what it does not do.
- [docs/catalog.md](catalog.md): the catalogue, targets and modules, and how to change them through Beacon.
- [deploy/README.md](../deploy/README.md) and [deploy/runbooks/](../deploy/runbooks/): the files, and one page per alert.
- [docs/goals.md](goals.md): what Beacon is for, and what it will not do.
