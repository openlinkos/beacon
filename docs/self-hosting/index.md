# Deployment options

Beacon is self-hosted: one **centre** that people, agents and machines talk to, **probe locations** placed where your users are, and the **machines** agents work on. There are three shapes to run it in, and they build on each other: try the loop on your laptop, move the same thing onto one server, then extend it with probe locations and machines.

| Shape | What it is for | How |
| --- | --- | --- |
| [A trial on your laptop](../guide/quickstart.md) | Seeing the whole loop within 10 minutes: a service breaks, the alert wakes an agent, it fixes the service through the SSH channel, every location checks again | `deploy/try/`: one compose file with Beacon, VictoriaMetrics, vmalert, a probe and a demo machine with sshd and a small web service. No domain, no certificates, `http://localhost:17001` |
| [One server](install.md) | Running it for real | The reference deployment in `deploy/`: `install.sh` starts Beacon, VictoriaMetrics, vmalert and Caddy behind two DNS names, with certificates from Let's Encrypt |
| [Probe locations and machines](probes.md) | Extending it | `deploy/probe/install.sh` on any small Linux where your users are; machines are enrolled from the centre and are reached directly, or from behind NAT through a connection out |

The rest of this page says what each machine needs and which ports are involved; the pages below it carry the steps.

## What each part needs

| Part | Needs |
| --- | --- |
| The centre | Linux with 2 CPUs, 4 GB of memory and 40 GB of disk; Docker Engine with Compose v2; the OpenSSH client (`ssh-keygen`). The whole installation uses about 200 MB of memory when idle; the reference compose file caps its four containers at 256 MB (Beacon), 1 GB (VictoriaMetrics), 256 MB (vmalert) and 128 MB (Caddy) |
| A probe location | Any Linux with systemd on x86_64 or arm64, with 64 MB of memory to spare. The agent is about 15 to 20 MB resident and its unit file caps it at 64 MB. It listens on nothing: it makes outbound HTTPS connections to the gateway and shows its own state on `127.0.0.1:17009` |
| A machine agents work on | Any server the centre can reach on its SSH port. Four lines from `beacon machine add` make its sshd trust Beacon's certificate authority; a machine behind NAT connects outwards instead ([Machines behind NAT](../guide/machines.md#machines-behind-nat)) |
| A trial | A laptop with Docker; see [Try it on your laptop](../guide/quickstart.md) |

## Ports

The ports the reference deployment opens on the centre. All three of Beacon's own addresses can be changed in `config.yaml` (`listen`, `gateway.listen`, `ssh.listen`; see [Configuration](configuration.md)), and the compose file decides which of them leave the machine at all.

| Port | What answers there | Reachable from | In the reference deployment |
| --- | --- | --- | --- |
| 80 and 443, TCP (443 also UDP, for HTTP/3) | Caddy: the certificates, and HTTPS for both host names | the Internet | published |
| 17001 | Beacon itself: the public status pages, the console at `/console`, the API `/api/v1`, MCP `/mcp`, `/healthz` | people and agents, through Caddy on the people's host name | bound to 127.0.0.1 |
| 17002 | The SSH channel: agents' `ssh`, `scp`, `rsync` | wherever your agents run | published as it is, plain SSH |
| 17006 | The gateway for machines: probes push results and fetch targets, heartbeats report, the rule engine and Flux send alerts and change events | machines, through Caddy on the machines' host name; a probe running on the centre itself uses `127.0.0.1:17006` | bound to 127.0.0.1 |
| 8428 | VictoriaMetrics, its own UI at `/vmui` | the centre only | bound to 127.0.0.1 |
| 8880 | vmalert, the rule engine's UI | the centre only | bound to 127.0.0.1 |

A probe location opens no ports. A machine agents work on needs its SSH port reachable from the centre (or an outgoing connection, behind NAT).

## Where to go from here

- [Try it on your laptop](../guide/quickstart.md): the whole loop on one machine, in 10 minutes.
- [On a server](install.md): the centre, the first probe locations and the first machine, in about 12 minutes.
- [Probe locations](probes.md): where to put them and how to install them.
- [Configuration](configuration.md): `config.yaml`, the secret files, and how changes reach a running Beacon.
- [Upgrades](upgrade.md), [Run on PostgreSQL](postgres.md), [Backup and restore](backup.md), [Security model](security.md) and [Troubleshooting](troubleshooting.md), each one page.
