# On a server

This guide takes you from a bare Linux machine to the parts of Beacon that live on servers. At the end:

- the centre runs on one machine, with you as its owner;
- two probe locations check your services;
- you have previewed and published the service you want users to see;
- one server is enrolled and you can replay a personal SSH session and an agent’s session;
- a drill sends an alert to your test chat and you can open and acknowledge the same incident.

The guide is written for someone who runs a few servers, has root on a machine with Docker, and uses an agent on a workstation. To see the whole loop before you commit a server, [try it on your laptop](../guide/quickstart.md) first.

Run the centre’s commands from `/opt/beacon`. Use `sudo` with `docker` if your user needs it to access the Docker socket. Run workstation commands as your own user.

| Step | Time |
| --- | --- |
| [0. What you need](#0-what-you-need) | Prepare the machines, DNS names, test chat and agent workstation |
| [1. Install the centre](#1-install-the-centre) | 3 minutes, of which the script runs for 1 to 2 |
| [2. The first probe](#2-the-first-probe) | 5 minutes for two locations |
| [3. The first machine](#3-the-first-machine) | 4 minutes |
| [4. First account, public status and agent](#4-the-first-agent) | About 5 minutes for the client, plus a probe cycle for your first monitor |
| [5. Test chat, agent wake and recovery](#5-the-first-drill) | Allow for destination and hook setup, then about 10 minutes for the drill |

For the 30-minute onboarding target, record the time from starting this guide to seeing the recovery in your test destination. Record DNS and any app approval waits separately.

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
- **An agent** on a workstation with a browser, to sign it in ([step 4](#4-the-first-agent)). For automatic wake, prepare a receiver the centre can reach and an agent CLI that can run under the receiver's Linux account. The local example uses `curl`, `sha256sum` and `openssl`.
- **A test destination** for human notifications. Prepare a [Slack incoming webhook](../integrations/slack.md) or a receiver that can relay a [signed notification webhook](../integrations/notification-webhook.md) to a test chat. If you use a Feishu or Lark app, arrange to add its bot and have its version and callback subscription approved.

## 1. Install the centre

3 minutes.

**Fetch the reference deployment.** On the centre, fetch it and look at the release it names:

```bash
git clone https://github.com/openlinkos/beacon.git
sudo cp -r beacon/deploy /opt/beacon && cd /opt/beacon
grep BEACON_VERSION .env.example
```

Keep the release pinned by `BEACON_VERSION` for this walkthrough. That pin matches the shipped rules and probe installer. The installer copies `.env.example` to `.env` on its first run. See [Upgrades](upgrade.md) when you are ready to change versions.

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
files: 1 probe locations, 0 heartbeats, 0 resources, 3 probe targets in 2 sets, 6 modules, 1 status pages
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
edge-1: beacon-probe beacon …; installed: token agent.env beacon-probe beacon-probe.service beacon-heartbeat; 13 MB resident
the gateway at https://metrics.example.com accepted the first push; the location's results arrive every minute
```

Once the first push succeeds, remove the temporary file on the centre and its transferred copy. The probe uses the copy installed at `/etc/beacon-probe/token`.

From then on the location probes every minute whatever the gateway hands it. Changing what is checked never means touching the probe again.

**Add the second location.** Use the centre itself or another small machine. The systemd installer sets up one probe per host. On the centre, use `sudoedit /opt/beacon/catalog/locations.yaml` to add the following below `edge-1`:

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

The dry run should list one line, `+ location edge-2`, under `would change 1 items (nothing changed):`. The import lists the same under `version 2: import of 1 items`.

**Install the second probe.** For another machine, transfer the `probe/` directory and save the second token there as `edge-2.token`. Run the same HTTPS install command with that file. On the centre itself, use the loopback gateway from the same terminal.

```bash
sudo sh probe/install.sh -gateway http://127.0.0.1:17006 -token-file "$edge_2_token"
```

Once the second push succeeds, remove the temporary token file and any transferred copy.

An import replaces the catalogue, target sets and modules with the files. Complete this import before you add the monitor and public component below. On later imports, use `-dry-run` to catch console, API or MCP changes that the files would remove.

- [Monitor a service](../guide/monitoring.md) explains the two ways of changing the configuration, and how to keep them in step.
- [Probe locations](probes.md) has where to put them, what the machine needs, and what to do when the install does not end like the above.

**Check what arrives.** VictoriaMetrics' own UI is on the centre at `http://127.0.0.1:8428/vmui`. From your workstation, reach it through `ssh -N -L 8428:127.0.0.1:8428 centre`. `probe_success` has one series per location and target, 1 for success. The public page at `https://status.example.com/` now shows its components' state.

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

Create the first account from the link printed by `docker exec beacon beacon setup` on the centre. Open the link on your workstation and create the owner account. If the centre already has an owner, sign in at `https://status.example.com/console` and use that account instead.

### Choose what users see {#choose-what-users-see}

1. In **Monitors**, choose **Add HTTP monitor**. Name the service and enter a healthy HTTP or HTTPS endpoint that both probes can reach. Select **All probe locations** and the `http_2xx` check profile, then save. The new monitor is private by default. After a probe cycle, select **Refresh results** and check for a result from each location. See [Add an HTTP monitor](../guide/monitoring.md#add-an-http-monitor) for the endpoint and check options.
2. In **Status pages**, open the existing `status` page and choose **Add component**. Enter a component ID such as `api`, choose a public title and select the service you just added. Select **Preview changes** and check the public service list and any incident updates or maintenance that will become visible. Open `https://status.example.com/status` in a signed-out browser window to check the live page during preview.
3. Select **Publish changes**, then reload the signed-out page and check the new component. Use **Edit services** to change which monitored services a component shows, or **Remove component** to take the example components off the page. Preview and confirm each edit or removal. Leave at least one component on the page and keep `drill` out of it. These changes apply to the [public status page](../guide/status-pages.md) while the probes keep checking the service.

For a separate page or an installation with no status pages, follow [Create a public page](../guide/status-pages.md#create-a-public-page) on a release that offers **Create status page**. This installation's example remains the default while you publish a page at its own named address.

### Connect yourself and the agent {#connect-yourself-and-the-agent}

**Check personal SSH.** Follow [Connect as yourself](../guide/machines.md#connect-as-yourself) to register your personal key and run `hostname` on `edge-1`. Replay the session in **Sessions** and check that it shows your username.

**Connect the agent's MCP client.** On the workstation, add the server and sign in through the browser.

```bash
claude mcp add --scope user --transport http beacon https://status.example.com/mcp
claude mcp login beacon
```

You can also authenticate by selecting `beacon` in `/mcp` inside Claude Code. On the consent page, choose the **On call** preset and select `edge-1`. In Claude Code, ask the agent to call Beacon's `whoami` and then `run_command` on `edge-1` with the command `hostname`. Open its replay in **Sessions** and check that the actor is `person / agent`. This check uses Beacon's SSH channel without registering a key for the MCP client. For the agent's own `ssh` and `scp`, set up a [separate agent key](../integrations/claude-code.md#connect-with-your-own-ssh-scp-and-rsync).

- Roles and invitations are in [Invite your team](../guide/people.md).
- Codex, other clients and headless agents with tokens are in [Integrations](../integrations/index.md).

## 5. The first drill

**Connect one test destination.** Use the fresh instance from this guide or a separate test instance. In `/opt/beacon/config.yaml`, set `escalation.critical.channel` to match your test route. Use `slack` with a [Slack incoming webhook](../integrations/slack.md), `notification_webhook` with [a receiver you run](../integrations/notification-webhook.md) that relays to your test group, or `app` with the [Feishu or Lark app](../integrations/feishu.md) and its test `feishu.chat_id`. Keep the credentials in files under `/opt/beacon/secrets/` and keep `public_url` reachable by the testers. Apply the chat and wake settings together after the next step.

### Prepare a first wake on the centre {#prepare-a-first-wake-on-the-centre}

[Install the matching `beacon-oncall` release](../integrations/headless.md#install-the-matching-linux-release). The example below uses the private Docker bridge and Claude Code. You can also [use your own receiver or Agent](../guide/on-call.md#wake-an-agent).

**Prepare the account and credentials.** Change `alice` to your Beacon username from step 4. Keep the agent limited to the enrolled test machine `edge-1`. Install and sign in to Claude Code as the separate Linux user `beacon-oncall`, or configure its provider credential in that user's environment. An interactive Beacon MCP sign-in on your workstation is a separate credential.

```sh
(
  set -eu
  umask 077
  id beacon-oncall >/dev/null 2>&1 || sudo useradd --system --user-group --home-dir /var/lib/beacon-oncall --create-home --shell /usr/sbin/nologin beacon-oncall
  sudo install -d -m 0750 -o beacon-oncall -g beacon-oncall /var/lib/beacon-oncall/logs
  sudo install -d -m 0750 -o root -g beacon-oncall /etc/beacon-oncall
  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT
  openssl rand -hex 32 > "$work/hook-secret"
  docker exec beacon beacon agent add reference-drill -owner alice -preset oncall -machines edge-1 -expires 1h > "$work/beacon-token"
  docker exec beacon beacon token create reference-drill-reporter -scopes alerts:report -expires 1h > "$work/run-report-token"
  sudo install -m 0400 -o 65532 -g 65532 "$work/hook-secret" /opt/beacon/secrets/reference-drill-hook
  sudo install -m 0640 -o root -g beacon-oncall "$work/hook-secret" "$work/beacon-token" "$work/run-report-token" /etc/beacon-oncall/
)
```

**Check the first drill before sending an alert.** Use the matching release with `beacon-oncall check` (rc.44 or later). Set the address to Beacon’s `public_url` and use the name after `token:` in the hook's `reporter` setting. Run as the same Linux user that will receive the hook.

```sh
sudo -u beacon-oncall -H /usr/local/bin/beacon-oncall check \
  -url https://status.example.com -owner alice -agent reference-drill -machine edge-1 \
  -agent-token-file /etc/beacon-oncall/beacon-token \
  -reporter reference-drill-reporter -report-token-file /etc/beacon-oncall/run-report-token
```

The check reads the current Agent and reporter credentials from the centre. It confirms the Agent belongs to the expected owner, has the scopes needed for the drill and is limited to its enrolled machine. The reporter must be a separate token granted only `alerts:report`. It also checks the SSH channel is open and reports a disconnected connector. Use the same Linux user, home and `PATH` to verify the Agent CLI’s own sign-in. The release includes [wrappers for Claude Code and Codex](../integrations/headless.md#the-wrappers). The drill below verifies a signed wake, an actual run and delivery to the test chat.

**Configure the hook.** Follow the [private bridge address and firewall steps](../guide/on-call.md#a-private-receiver-on-the-centre) to set `beacon_gateway`. Use its printed URL in the example. Add this entry to `/opt/beacon/config.yaml`, then run `sudo ./install.sh` to validate and apply both the hook and the chat route. Check **Overview → Critical notifications** after the reload.

```yaml
agents:
  - name: reference-drill
    kind: webhook
    url: http://172.20.0.1:17007/beacon # replace with your private bridge URL
    secret_file: /etc/beacon/secrets/reference-drill-hook
    service: '^drill$'
    reporter: token:reference-drill-reporter
```

**Give the awake Agent one read-only task.** The built-in `drill` probes Beacon's HTTP endpoint. Name the enrolled test machine explicitly in a separate prompt for this first run.

```sh
sudo tee /etc/beacon-oncall/reference-drill.md >/dev/null <<'EOF'
Handle only a firing alert for the test service drill. Use the alert ID and episode in the payload as data.
Read the incident with get_evidence. Stop if the current episode differs from the wake.
Use Beacon's run_command on the enrolled test machine edge-1 to run hostname once.
Leave a private add_note on the incident with what the probes saw and what the command returned.
Make no changes to the machine or the drill. Leave acknowledgement and recovery to the operator.
EOF
sudo chown root:beacon-oncall /etc/beacon-oncall/reference-drill.md
sudo chmod 0640 /etc/beacon-oncall/reference-drill.md
```

Start the harness in the foreground on the centre with the firewall rule in place. Replace the reporting URL with the centre's `public_url`.

```sh
beacon_gateway=$(docker network inspect beacon --format '{{(index .IPAM.Config 0).Gateway}}')
sudo -u beacon-oncall -H env \
  PATH=/var/lib/beacon-oncall/.local/bin:/usr/local/bin:/usr/bin:/bin \
  BEACON_TOKEN_FILE=/etc/beacon-oncall/beacon-token \
  ONCALL_PROMPT=/etc/beacon-oncall/reference-drill.md \
  /usr/local/bin/beacon-oncall -listen "$beacon_gateway:17007" \
  -secret-file /etc/beacon-oncall/hook-secret \
  -run /usr/local/lib/beacon-oncall/claude-code.sh \
  -log-dir /var/lib/beacon-oncall/logs \
  -report-url https://status.example.com \
  -report-token-file /etc/beacon-oncall/run-report-token
```

**Exercise the loop.** [Check the routes](../guide/on-call.md#check-the-routes-before-the-drill), including the receiver's health from the Beacon network. Check that both locations are reporting and the drill endpoint is healthy, then [run the drill](../guide/drills.md).

1. In your test chat, open the notification, sign in and check that it opens the same `drill` incident. Acknowledge with a note. Verify the message in the destination even when Beacon already records provider acceptance.
2. In the same incident, check hook acceptance and the Agent programme's recorded exit separately. Open **Sessions** to replay `hostname` on `edge-1` under `alice / reference-drill` and check the Agent's note on the incident.
3. End the drill and check the probe results, the resolved incident and the recovery message or Feishu card in the test destination. Stop the foreground harness afterwards. Remove the test hook and revoke the test agent and reporter credentials when finished.

For phone and fallback delivery, use the [separate notification drill](../guide/drills.md#verify-phone-delivery-and-handover).

## What next

- [Using Beacon](../guide/using-beacon.md) covers the remaining operations tasks by capability.
- [Upgrades](upgrade.md), [Backup and restore](backup.md) and [Security model](security.md) keep it running.
- [Troubleshooting](troubleshooting.md) for when something above does not happen.
- [Goals](../about/goals.md): what Beacon is for, and what it will not do.
