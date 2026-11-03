# The try environment

Beacon's whole loop on one machine, in Docker, without a domain or a certificate: Beacon, VictoriaMetrics, vmalert, one probe location and a demo machine (Alpine with sshd and a small web server), all published on the loopback interface. [docs/guide/quickstart.md](../../docs/guide/quickstart.md) walks through it in ten minutes: break the demo service, watch the alert, let an agent repair it through the SSH channel, replay what it did.

```bash
sh up.sh
```

`up.sh` copies `.env.example` to `.env`, makes the throwaway secrets under `secrets/` (the rules token, the SSH channel's host and certificate-authority keys, the demo machine's host key), starts the containers, enrolls the probe location `edge-1`, pins the demo machine's host key and prints the one-time link that makes the first account, an owner. It is safe to run again. `docker compose down -v` stops and forgets everything. Always start with `up.sh`, never `docker compose up`: the secrets it makes are mounted into the containers, and Docker would make directories in their place. If that happened, `rm -rf secrets` and run `up.sh` again.

| Path | What |
| --- | --- |
| `compose.yaml`, `.env` | The five containers, on 127.0.0.1: 17001 (the API, MCP, the console), 17002 (the SSH channel), 18080 (the demo service) |
| `config.yaml` | Beacon's configuration: `public_url` is plain http on localhost, and the routing waits are shortened ([config.example.yaml](../../config.example.yaml) explains every field) |
| `catalog/`, `targets/` | The one location, the demo target and the http module; imported at the first start |
| `rules/`, `rules/tests/` | The rules with the shortened waits (`vmalert-tool unittest --disableAlertgroupLabel --files 'deploy/try/rules/tests/*.yml'`), unlike the reference rules in [../rules](../rules), where a service is down only when at least two locations fail it |
| `demo/` | The demo machine's image: sshd trusting Beacon's certificate authority, the `beacon` account, `start-web` and `stop-web` |
| `secrets/` | Made by `up.sh`, readable (a laptop trial's throwaway keys); nothing here is real |

For a server, with TLS and a second location: [../README.md](../README.md).
