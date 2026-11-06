# On a server

This guide takes you from a bare Linux machine to the parts of Beacon that live on servers: at the end, the centre runs on one machine with you as its owner, two probe locations check your services, and one server is enrolled as a machine agents may work on through Beacon. Connecting your agent and rehearsing an outage have their own pages, linked from steps 4 and 5. It is written for someone who runs a few servers, has root on a machine with Docker, and uses an agent on a workstation. To see the whole loop before you commit a server, [try it on your laptop](../guide/quickstart.md) first.

Each step says how long it takes, what to run, what you should see, and what to do when you see something else. Commands on the centre run from `/opt/beacon`, through `docker exec beacon beacon …`.

| Step | Time |
| --- | --- |
| [0. What you need](#0-what-you-need) | not counted: a machine, two DNS names |
| [1. Install the centre](#1-install-the-centre) | 3 minutes, of which the script runs for 1 to 2 |
| [2. The first probe](#2-the-first-probe) | 5 minutes for two locations |
| [3. The first machine](#3-the-first-machine) | 4 minutes |
| [4. The first agent](#4-the-first-agent) | 5 minutes, in [Try it on your laptop](../guide/quickstart.md) |
| [5. The first drill](#5-the-first-drill) | 10 minutes, in [Rehearse an outage](../guide/drills.md) |

Steps 1 to 3 take about 12 minutes once step 0 is done. DNS propagation is outside your hands, and outside the count.

## 0. What you need

Three words first. The **centre** is the machine Beacon runs on. A **probe location** is a small machine somewhere your users are, running `beacon-probe`, which checks your services every minute and sends the results to the centre. A **machine** is a server agents may work on through Beacon.

- **The centre**: Linux with 2 CPUs, 4 GB of memory and 40 GB of disk; [Docker Engine](https://docs.docker.com/engine/install/) with Docker Compose v2; the OpenSSH client (`ssh-keygen`).
- **Two DNS names** pointing at the centre: one for people and agents (`status.example.com` below: the API, MCP, the console, the status page, and the SSH channel on port 17002) and one for machines (`metrics.example.com`: the gateway the probes report to). Ports 80 and 443 must be reachable from the Internet, for Let's Encrypt and for the traffic; port 17002 from wherever your agents run.
- **An e-mail address** Let's Encrypt can write to about the certificates. It must be a real one: the installer refuses `example.com` addresses.
- **A small machine for the first probe location**: any Linux with systemd, on x86_64 or arm64, with 64 MB of memory to spare. A VPS where your users are is best. The centre itself will do for the second location.
- **A machine agents may work on**, which the centre can reach on its SSH port. The probe machine will do.
- **An agent** on a workstation with a browser, to sign it in ([step 4](#4-the-first-agent)).

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
database: none yet at /var/lib/beacon/beacon.db; the service imports the files when it first starts
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

[deploy/README.md](https://github.com/openlinkos/beacon/blob/main/deploy/README.md) says where each file of the deployment is.

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

About 10 seconds later it ends with:

```
edge-1: beacon-probe beacon 0.3.0-rc.18 …; installed: token agent.env beacon-probe beacon-probe.service beacon-heartbeat; 13 MB resident
the gateway at https://metrics.example.com accepted the first push; the location's results arrive every minute
```

From then on the location probes every minute whatever the gateway hands it, so changing what is checked never means touching the probe again.

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

An import makes the files the whole configuration: anything changed through the API or MCP that the files do not hold is removed, which is why `-dry-run` comes first. [What is checked, and from where](../guide/monitoring.md) explains the two ways of changing the configuration and how to keep them in step; [Probe locations](probes.md) has where to put them, what the machine needs, and what to do when the install does not end like the above.

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
(see docs/guide/machines.md for sudo and for PerSourcePenaltyExemptList)
```

Run those four lines on the machine, as root. They make sshd trust Beacon's certificate authority (CA), the key that signs a short-lived certificate for each connection, and create the account those certificates name. If `machine add` fails with `the machine cannot be reached`, the centre cannot open a connection to the machine's SSH port: allow it in the machine's firewall, or, for a machine behind NAT, add it the other way round as [Machines behind NAT](../guide/machines.md#machines-behind-nat) describes.

Two more settings are worth making now rather than later:

- **What the `beacon` account may do is the real boundary.** Give it only what the work needs, for example in `/etc/sudoers.d/beacon`: `beacon ALL=(root) NOPASSWD: /usr/bin/systemctl restart beacon-probe, /usr/bin/journalctl`.
- **Exempt the centre from OpenSSH's penalties** for failed connections: every agent arrives from the centre's address, so add `PerSourcePenaltyExemptList 203.0.113.7` (the centre's address) to `beacon.conf`.

[Give agents a machine](../guide/machines.md) has the rest.

## 4. The first agent

5 minutes, and every step of it is written out in [Try it on your laptop](../guide/quickstart.md): the same commands against your server. In short, make the first account with the one-time link the service prints, then connect your agent over MCP and sign it in through the browser:

```bash
docker exec beacon beacon setup
claude mcp add --scope user --transport http beacon https://status.example.com/mcp
```

The agent never does more than your role allows, and every command it runs on a machine is on record with a replay. Roles, Codex, invitations and headless agents with tokens are in [People and their agents](../guide/people.md).

## 5. The first drill

10 minutes, most of it waiting, written out in [Rehearse an outage](../guide/drills.md). In short, Beacon has a service of its own to rehearse on; with two locations, `docker exec beacon beacon drill fail 15m` makes both locations fail, `ServiceDown` fires about 4 to 5 minutes later, and `beacon drill off` ends it and the alert resolves at once.

## What next

- [Set up the Feishu app](../integrations/feishu.md), so alerts reach people as cards. Each secret goes in a file under `/opt/beacon/secrets/`, the `feishu:` section in `/opt/beacon/config.yaml` is uncommented, and `sudo ./install.sh` followed by `docker compose restart beacon` applies it ([Configuration](configuration.md)).
- [Put an agent on call](../guide/on-call.md), so a new alert wakes an agent instead of waiting for someone to ask.
- [Upgrades](upgrade.md), [Backup and restore](backup.md) and [Security model](security.md) keep it running.
- [Troubleshooting](troubleshooting.md) for when something above does not happen.
- [Goals](../about/goals.md): what Beacon is for, and what it will not do.
