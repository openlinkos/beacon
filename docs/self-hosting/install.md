# On a server

This guide takes you from a bare Linux machine to the parts of Beacon that live on servers. At the end:

- the centre runs on one machine, with you as its owner;
- two probe locations check your services;
- one server is enrolled as a machine agents may work on through Beacon.

Connecting your agent and rehearsing an outage have their own pages, linked from steps 4 and 5.

The guide is written for someone who runs a few servers, has root on a machine with Docker, and uses an agent on a workstation. To see the whole loop before you commit a server, [try it on your laptop](../guide/quickstart.md) first.

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

Three words first:

- The **centre** is the machine Beacon runs on.
- A **probe location** is a small machine somewhere your users are. It runs `beacon-probe`, which checks your services every minute and sends the results to the centre.
- A **machine** is a server agents may work on through Beacon.

You need:

- **The centre**: Linux with 2 CPUs, 4 GB of memory and 40 GB of disk; [Docker Engine](https://docs.docker.com/engine/install/) with Docker Compose v2; the OpenSSH client (`ssh-keygen`).
- **Two DNS names** pointing at the centre:
  - one for people and agents: `status.example.com` below. It serves the API, MCP, the console, the status page, and the SSH channel on port 17002.
  - one for machines: `metrics.example.com`, the gateway the probes report to.
- **Open ports.** Ports 80 and 443 must be reachable from the Internet, for Let's Encrypt and for the traffic. Port 17002 must be reachable from wherever your agents run.
- **An e-mail address** Let's Encrypt can write to about the certificates. It must be a real one: the installer refuses `example.com` addresses.
- **A small machine for the first probe location**: any Linux with systemd, on x86_64 or arm64, with 64 MB of memory to spare. A VPS where your users are is best. The centre itself will do for the second location.
- **A machine agents may work on**, which the centre can reach on its SSH port. The probe machine will do.
- **An agent** on a workstation with a browser, to sign it in ([step 4](#4-the-first-agent)).

## 1. Install the centre

3 minutes.

**Fetch the reference deployment.** On the centre, fetch it and look at the release it names:

```bash
git clone https://github.com/openlinkos/beacon.git
sudo cp -r beacon/deploy /opt/beacon && cd /opt/beacon
grep BEACON_VERSION .env.example
```

`BEACON_VERSION` is the release Beacon will run. If it is not the [latest release](https://github.com/openlinkos/beacon/releases), change it in `.env.example` now. The installer copies `.env.example` to `.env` on its first run. Later, to update, change it in `.env` and run the installer again, which is safe at any time.

**Run the installer:**

```bash
sudo ./install.sh
```

The script asks for the two host names and the e-mail address. To run it unattended, give them in the environment: `sudo env STATUS_HOST=… METRICS_HOST=… ACME_EMAIL=… ./install.sh`.

Then it does the rest:

- copies `.env.example` to `.env`;
- generates the secrets: the token the rule engine uses, and the SSH channel's host and CA keys, which never leave the machine;
- puts your names into `config.yaml` and the probe targets;
- validates every file with its own image;
- pulls the four images (Beacon, VictoriaMetrics, vmalert and Caddy) and starts them;
- waits until all four are healthy and the rule engine's first alert has reached Beacon.

It took 69 seconds on a well-connected host. Allow one to two minutes on a 2-CPU machine with ordinary bandwidth, most of it pulling images.

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

After it come the next steps, which are the steps of this guide.

**Check both host names**, from anywhere:

```bash
curl https://status.example.com/healthz          # ok
curl https://metrics.example.com/api/v1/targets  # {"code":"auth.required",…}: refused without a token, as it should be
```

Then open `https://status.example.com/` in a browser. It is the example status page from `catalog/status_pages.yaml`, saying **No data yet** until the probes report.

Nobody is notified of anything yet. Without a chat configured, alerts are recorded and shown in the API, MCP and the console. That is all the next steps need.

If it does not go like this:

- **`install.sh: …` and it stops**: the message says what is missing (root, Docker, Compose v2, `ssh-keygen`, a real e-mail address, two different host names). Or it says which file does not validate, with the command that shows why.
- **`curl` fails on TLS**: the first certificate takes up to a minute after the start. If it still fails, check that both names resolve to the centre and that ports 80 and 443 are open. `docker compose logs caddy` says what Let's Encrypt answered.

[deploy/README.md](https://github.com/openlinkos/beacon/blob/main/deploy/README.md) says where each file of the deployment is.

## 2. The first probe

5 minutes for two locations.

Beacon tells an outage from a hiccup by agreement. A service is down only when at least two locations fail it, and they are at least half of the locations reporting. So the first location gives you data, and the second gives you alerts.

**Enrol the first location.** The catalogue already holds one location, `edge-1`. The catalogue is `catalog/locations.yaml`, imported into Beacon's database at the first start. On the centre, enrol the location. Save the one-time token into a private temporary file.

```bash
edge_1_token=$(mktemp)
docker exec beacon beacon enroll probe edge-1 > "$edge_1_token"
```

You should see `enrolled probe-edge-1 (probe:write, expires …); the token above is not shown again`. The file at `$edge_1_token` holds a line starting with `lbt_`.

**Install the probe.** Transfer `/opt/beacon/probe/` and the file at `$edge_1_token` to a private directory on the probe machine over SSH. Save the file as `edge-1.token` there. From that directory, run this as root.

```bash
sh probe/install.sh -gateway https://metrics.example.com -token-file edge-1.token
```

About 10 seconds later it ends with:

```
edge-1: beacon-probe beacon 0.3.0-rc.23 …; installed: token agent.env beacon-probe beacon-probe.service beacon-heartbeat; 13 MB resident
the gateway at https://metrics.example.com accepted the first push; the location's results arrive every minute
```

Once the first push succeeds, remove the temporary file on the centre and its transferred copy. The probe uses the copy installed at `/etc/beacon-probe/token`.

From then on the location probes every minute whatever the gateway hands it. Changing what is checked never means touching the probe again.

**Add the second location.** It can be the centre itself (nearly free) or another small machine. Add it to the catalogue file, `/opt/beacon/catalog/locations.yaml` on the centre, below `edge-1`:

```yaml
  - id: edge-2
    name: Germany - Frankfurt      # public: name places and networks, never vendors
    region: overseas
    network: BGP
```

**Import the files and enrol it.** Import the files into Beacon, first to see what would change, then for real. Then enrol the new location:

```bash
docker exec beacon beacon config import /etc/beacon -dry-run
docker exec beacon beacon config import /etc/beacon -note "second probe location"
edge_2_token=$(mktemp)
docker exec beacon beacon enroll probe edge-2 > "$edge_2_token"
```

The dry run should list one line, `+ locations edge-2`, under `would change 1 items (nothing changed):`. The import lists the same under `version 2: import of 1 items`.

**Install the second probe** as above using the token at `$edge_2_token`. If the second probe runs on the centre itself, install it in the same terminal using the loopback gateway instead.

```bash
sudo sh probe/install.sh -gateway http://127.0.0.1:17006 -token-file "$edge_2_token"
```

Once the second push succeeds, remove the temporary token file and any transferred copy.

An import makes the files the whole configuration. Anything changed through the API or MCP that the files do not hold is removed. That is why `-dry-run` comes first.

- [Monitor a service](../guide/monitoring.md) explains the two ways of changing the configuration, and how to keep them in step.
- [Probe locations](probes.md) has where to put them, what the machine needs, and what to do when the install does not end like the above.

**Check what arrives.** VictoriaMetrics' own UI is on the centre at `http://127.0.0.1:8428/vmui`. From your workstation, reach it through `ssh -N -L 8428:127.0.0.1:8428 centre`. `probe_success` has one series per location and target, 1 for success. The public page at `https://status.example.com/` now shows its components' state.

**Probe a service of yours.** The targets shipped are Beacon's own status host, the drill (step 5) and `https://example.com/`. Replace the last with a service of yours in `/opt/beacon/targets/all-web.yaml`, and import again.

## 3. The first machine

4 minutes, for a machine agents will work on.

**Add the machine.** On the centre:

```bash
docker exec beacon beacon machine add edge-1 edge-1.example.net -note "the first probe machine"
```

Beacon connects once and pins the machine's host key. From now on it will refuse any other key for this machine. Then it prints what the machine needs:

```
added edge-1 at edge-1.example.net:22 as beacon; its host key is SHA256:…
On the machine, as root, if not done yet:
  echo 'ssh-ed25519 AAAA… beacon-ca' > /etc/ssh/beacon_ca.pub
  echo 'TrustedUserCAKeys /etc/ssh/beacon_ca.pub' > /etc/ssh/sshd_config.d/beacon.conf
  id beacon >/dev/null 2>&1 || useradd -m -s /bin/bash -p '*' beacon
  systemctl reload ssh
(see docs/guide/machines.md for sudo and for PerSourcePenaltyExemptList)
```

**On the machine, as root**, run the first three lines printed by `machine add`. They configure the certificate authority (CA) that signs each short-lived connection and create the account those certificates name. Check that `sshd_config` includes `/etc/ssh/sshd_config.d/*.conf`, then run `sshd -t`. Once it passes, reload `ssh` on Debian or Ubuntu, or `sshd` on distributions such as RHEL.

If `machine add` fails with `the machine cannot be reached`, the centre cannot open a connection to the machine's SSH port. Allow it in the machine's firewall. For a machine behind NAT, add it the other way round, as [Machines behind NAT](../guide/machines.md#machines-behind-nat) describes.

Two more settings are worth making now rather than later:

- **What the `beacon` account may do is the real boundary.** Give it only what the work needs, for example in `/etc/sudoers.d/beacon`: `beacon ALL=(root) NOPASSWD: /usr/bin/systemctl restart beacon-probe, /usr/bin/journalctl`.
- **Exempt the centre from OpenSSH's penalties** if the machine supports `PerSourcePenaltyExemptList` (introduced in 9.8). Add `PerSourcePenaltyExemptList 203.0.113.7` to `beacon.conf` with the centre's source address, then validate and reload sshd.

[Give agents a machine](../guide/machines.md) has the rest.

## 4. The first agent

5 minutes to connect the agent. Make the first account on the centre with the link printed by `docker exec beacon beacon setup`. Open the link on your workstation and create the owner account.

**Check personal SSH.** Follow [Connect as yourself](../guide/machines.md#connect-as-yourself) to register your personal key and run `hostname` on `edge-1`. Replay the session in **Sessions** and check that it shows your username.

**Connect the agent's MCP client.** On the workstation, add the server and sign in through the browser.

```bash
claude mcp add --scope user --transport http beacon https://status.example.com/mcp
claude mcp login beacon
```

On the consent page, choose the **On call** preset and select `edge-1`. In Claude Code, ask the agent to call Beacon's `whoami` and then `run_command` on `edge-1` with the command `hostname`. Open its replay in **Sessions** and check that the actor is `person / agent`. This check uses Beacon's SSH channel without registering a key for the MCP client. For the agent's own `ssh` and `scp`, set up a [separate agent key](../integrations/claude-code.md#connect-with-your-own-ssh-scp-and-rsync).

- Roles and invitations are in [Invite your team](../guide/people.md).
- Codex, other clients and headless agents with tokens are in [Integrations](../integrations/index.md).

## 5. The first drill

10 minutes, most of it waiting, written out in [Rehearse an outage](../guide/drills.md).

In short, Beacon has a service of its own to rehearse on. With two locations:

- `docker exec beacon beacon drill fail 15m` makes both locations fail;
- `ServiceDown` fires about 4 to 5 minutes later;
- `docker exec beacon beacon drill off` restores the drill endpoint, then the alert resolves after the probes and rule engine check it again.

## What next

- [Set up the Feishu app](../integrations/feishu.md), so alerts reach people as cards. Each secret goes in a file under `/opt/beacon/secrets/`, and the `feishu:` section in `/opt/beacon/config.yaml` is uncommented. `sudo ./install.sh` followed by `docker compose restart beacon` applies it ([Configuration](configuration.md)).
- [Put an agent on call](../guide/on-call.md), so a new alert wakes an agent instead of waiting for someone to ask. [Using Beacon](../guide/using-beacon.md) has every other task, by capability.
- [Upgrades](upgrade.md), [Backup and restore](backup.md) and [Security model](security.md) keep it running.
- [Troubleshooting](troubleshooting.md) for when something above does not happen.
- [Goals](../about/goals.md): what Beacon is for, and what it will not do.
