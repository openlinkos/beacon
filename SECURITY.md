# Security

Beacon holds the credentials agents never see, signs the certificates that let them into machines, and decides who is told when something breaks. A hole in it is a hole in every deployment. Please report one privately.

## Reporting a vulnerability

Write to **security@linkos.io**. Do not open a public issue or pull request for it.

Please include what you can of:

- the version or commit (`beacon version`), and which part: the service (API, MCP, gateway, alert delivery, SSH channel), the probe agent, or the deployment files under `deploy/`;
- what an attacker gains: a token, a machine, an alert that is lost or forged, a secret in a log;
- how to reproduce it: requests, configuration, a probe module, a transcript;
- whether it is already public or being exploited, as far as you know;
- how you would like to be credited.

Encrypted mail is welcome; ask for a key in a first message.

## What to expect

- An acknowledgement within three working days.
- An assessment within a week: whether it is a vulnerability, how serious, and a plan.
- A fix in the next release candidate, with you informed before it is published, and credit in `CHANGELOG.md` unless you prefer none. We ask you to keep the details private until the fix is out; we aim for that within 30 days of the report, sooner when it is being exploited.

## Supported versions

The latest minor release (`vX.Y`), and its release candidates, receive fixes. Older minors do not; move to the latest one. Before 1.0 that means the latest tag on the [releases page](https://github.com/openlinkos/beacon/releases). Deployments following the reference deployment pin `BEACON_VERSION` in `deploy/.env` and update by raising it.

## Scope

In scope:

- the service `beacon`: the REST API and MCP server, the gateway for machines, alert routing and delivery (Feishu, Pushover, Bark, healthchecks.io), the hooks that wake agents, and the SSH channel (sign-in, certificates, recordings, the tools);
- the probe agent `beacon-probe`: the probers, the push to the gateway, what it reads from the machine;
- the deployment files under `deploy/`: the compose project, the Caddyfile, the rules, the install scripts and the systemd unit;
- the release pipeline: the signatures, SBOMs and provenance of the published binaries and image.

Out of scope, and best reported to the project concerned: vulnerabilities in VictoriaMetrics, vmalert, Caddy, Docker, Feishu, Pushover or GitHub themselves. Also out of scope: a deployment's own choices that the documentation warns about, such as giving `config:write` to an agent you would not let edit the probe modules, or an account on a machine with more rights than Beacon's work there needs.

## How Beacon is built to stay safe

Tokens are stored only as SHA-256 hashes, carry scopes and expire; failed attempts are limited per client address. A probe's or host's token writes only as its own location or host, and an expired machine cannot write. Agents never hold a key for a machine: Beacon signs a certificate valid for five minutes for each connection, and machines trust only its certificate authority; every session is recorded. Secrets are read from files the configuration names and never logged. The rule engine's alerts need the rules token. `/metrics` answers direct connections only. Releases are reproducible, signed with cosign, and carry SBOMs and build provenance; the release notes say how to verify them. Base images are pinned by digest and updated by Dependabot.
