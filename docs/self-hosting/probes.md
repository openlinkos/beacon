# Probe locations

A probe location is a small machine running `beacon-probe`, placed where your users are. This page says where locations are worth putting, what the machine needs, and how to install and update the agent.

Every minute the agent asks the centre what to check, checks it, and pushes the results. What the locations check, and how, is a different question: [Monitor a service](../guide/monitoring.md).

The examples assume the reference deployment from [On a server](install.md), where commands on the centre run through `docker exec beacon beacon …`.

## Why two, and why apart

Beacon tells an outage from a hiccup by agreement. With the reference rules:

- a location counts as failing a service once it has failed it for 3 minutes;
- the service is down only when at least two locations fail it, and they are at least half of the locations reporting.

One location's word is never an outage. So one location gives you data, and a second one gives you alerts. From there, each one added makes the verdict harder to dispute and the coverage wider.

Put the locations where your users are, on networks they use. Two locations on the same network share that network's failures, and agree about them. So spread them across networks and, if your users are in more than one country, across countries.

The catalogue gives every location a `region`: `overseas` or `mainland`. The rules compare the two. A service unreachable only from mainland China is a path problem, not an outage, and is alerted as one.

The centre itself can be a location, nearly free. The reference deployment publishes the gateway on its loopback interface, so `probe/install.sh -gateway http://127.0.0.1:17006` installs an agent beside the service. It is a fair second location, but a poor first one. When the centre is down, so is the location, so keep at least two locations that are not the centre.

Name locations by place and network, never by vendor, as in `Germany - Frankfurt` on network `BGP`. The name is public: it is shown on the status page and the cards.

## What the machine needs

- **The system**: any Linux with systemd, on x86_64 or arm64.
- **Memory**: 64 MB to spare. The agent is about 15 to 20 MB resident. Its unit file sets a soft limit of 40 MB and a hard limit of 64 MB. It also tells the kernel to sacrifice the agent first when memory runs short, because probes often live on small machines beside what they serve.
- **The network**: outbound HTTPS connections to the gateway. The agent opens no ports. It shows its own state, for `journalctl`-style checks, on `127.0.0.1:17009/metrics`.
- **A synchronised clock** (chrony, or systemd-timesyncd). Results carry timestamps, and the install script warns when NTP is off.

## Install a location

**Put the location in the catalogue first.** Enrolling an id the catalogue lacks fails with `location edge-2 is not in the catalogue`. The catalogue ships one location, `edge-1`. Add more in `catalog/locations.yaml` and import them, or add them through the API and MCP. [Monitor a service](../guide/monitoring.md#locations) describes both.

**Enrol the location**, on the centre. The token is printed once, and only what the gateway later receives matters:

```bash
docker exec beacon beacon enroll probe edge-1 > edge-1.token
```

You should see `enrolled probe-edge-1 (probe:write, expires …); the token above is not shown again`. `edge-1.token` holds a line starting with `lbt_`.

**Install the agent.** Copy `/opt/beacon/probe/` and `edge-1.token` to the probe machine, and run there, as root:

```bash
sh probe/install.sh -gateway https://metrics.example.com -token-file edge-1.token
```

The script installs the release of `beacon-probe` that matches this copy of the reference deployment. The probe and the service are released together. Two flags change that:

- `-version` picks another release;
- `-binary FILE` installs one you fetched yourself, for a machine that cannot reach GitHub.

Then the script:

1. creates the `beacon-probe` user;
2. downloads the binary for the machine's architecture, and checks it against the release's `SHA256SUMS`;
3. installs the token, readable by that user only;
4. installs the systemd unit `beacon-probe` and the `beacon-heartbeat` helper;
5. starts the agent, and waits until the gateway has accepted its first push.

That takes about 10 seconds. It ends with:

```
edge-1: beacon-probe beacon 0.3.0-rc.21 …; installed: token agent.env beacon-probe beacon-probe.service beacon-heartbeat; 13 MB resident
the gateway at https://metrics.example.com accepted the first push; the location's results arrive every minute
```

### When it does not end like that

If the script ends with `no push accepted by … within 150 seconds`, run `journalctl -u beacon-probe -n 50` on the probe machine:

- **A `401`**: the token file is not the one `enroll` printed. Enrol again and copy the new file.
- **A `403` with `location.inactive`**: the location is not in the catalogue, or is bounded by a `vps` resource whose date has passed. Add it and import.
- **A connection error**: the machine cannot reach the gateway's host on port 443.

A warning about the clock needs acting on too, as above.

## After the install

- **Targets come from the centre.** Every minute the agent fetches whatever the gateway hands it. Changing what is checked, or how, never means touching the probe again.
- **To update the agent**, copy a fresh `probe/` directory and run the same command again, with `-version vX.Y.Z` to pick a release. The script is idempotent and replaces only what changed. The service and the probe are released together, so the version in a fresh `deploy/` matches the centre's.
- **Slow or flaky paths** are a property of the location, not of every target. The catalogue's `probe.timeout` lengthens every module's timeout for that location alone. `probe.retries` has a probe that failed without an answer tried again at once ([Locations](../guide/monitoring.md#locations)).
- **Jobs on the machine** that must report in, backups say, end with `beacon-heartbeat <name>`. The install put it beside the agent. [Heartbeats and expiring resources](../guide/heartbeats.md) has the rest.
- **The agent's own state** is on `127.0.0.1:17009/metrics` of the probe machine: the last push, failures, and the queue on disk that holds results while the gateway is out of reach.
