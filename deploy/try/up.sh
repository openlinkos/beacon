#!/bin/sh
# Starts Beacon's try environment on this machine (docs/guide/quickstart.md): five containers, the whole
# loop, no domain and no certificate, everything on the loopback interface.
#   sh up.sh
# Idempotent: it makes what is missing (the secrets, the demo machine's pinned host key), starts what is
# down, replaces the probe's token, and prints the link that makes the first account (an owner) — or says
# so, when an account exists already. Stop and forget everything with: docker compose down -v
set -eu

cd "$(dirname "$0")"
start=$(date +%s)
say() { printf '%s\n' "$*"; }
die() { printf 'up.sh: %s\n' "$*" >&2; exit 1; }

command -v docker >/dev/null 2>&1 || die "Docker is not installed: https://docs.docker.com/engine/install/"
docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is missing (docker compose version fails)"
command -v ssh-keygen >/dev/null 2>&1 || die "ssh-keygen is missing: install the OpenSSH client"

# 1. .env: the release to run
[ -f .env ] || cp .env.example .env

# 2. The secrets, made here and never leaving this machine. They are a laptop trial's throwaway keys and
#    token, so unlike the reference deployment (deploy/install.sh, which runs as root and gives
#    everything to Beacon's user 65532 in mode 0400) they stay yours and readable: up.sh runs as you.
mkdir -p secrets
if [ ! -s secrets/rules-token ]; then
  t=$(mktemp)
  head -c 32 /dev/urandom | base64 | tr -d '\n=+/' > "$t" && mv "$t" secrets/rules-token
fi
for k in ssh-host-key ssh-ca-key demo-host-key; do
  [ -s "secrets/$k" ] || ssh-keygen -q -t ed25519 -N '' -C "beacon-try $k" -f "secrets/$k"
done
[ -f secrets/probe-edge-1.token ] || : > secrets/probe-edge-1.token
chmod 0644 secrets/rules-token secrets/ssh-host-key secrets/ssh-host-key.pub secrets/ssh-ca-key \
  secrets/ssh-ca-key.pub secrets/demo-host-key secrets/demo-host-key.pub secrets/probe-edge-1.token

# 3. The core: VictoriaMetrics and the rule engine, the demo machine (its sshd trusts Beacon's
#    certificate authority from the secrets above), then Beacon, which waits for both. The probe waits
#    for its token (step 4).
say "Starting the containers (the first run pulls the images and builds the demo machine) …"
docker compose up -d --build --quiet-pull victoriametrics vmalert demo beacon
say "Waiting for Beacon …"
i=0
while [ $i -lt 60 ]; do
  [ "$(docker inspect -f '{{.State.Health.Status}}' beacon-try 2>/dev/null)" = healthy ] && break
  sleep 2
  i=$((i + 1))
done
[ "$(docker inspect -f '{{.State.Health.Status}}' beacon-try 2>/dev/null)" = healthy ] ||
  die "Beacon is not healthy after 2 minutes: docker compose logs beacon"

# 4. The probe's token — 'beacon enroll' prints the secret once, on stdout alone. It is replaced on every
#    run (after a 'down -v' the database no longer knows the old one), and the probe starts, or restarts,
#    with it. Then the demo machine, whose host key 'machine add' pins by dialling it (sshd is up by then).
docker compose exec -T beacon beacon enroll probe edge-1 -rotate > secrets/probe-edge-1.token ||
  die "could not enroll the probe: docker compose logs beacon"
chmod 0644 secrets/probe-edge-1.token
docker compose up -d --force-recreate --quiet-pull probe
say "Enrolled the probe location edge-1; its results arrive every 30 seconds."
if ! docker compose exec -T beacon beacon machine list 2>/dev/null | grep -q '^demo[[:space:]]'; then
  docker compose exec -T beacon beacon machine add demo demo:22 -replace >/dev/null &&
    say "Added the demo machine (host key pinned, account beacon)."
else
  say "The demo machine is added already."
fi

# 5. The entrance, and the first account. 'beacon setup' prints a fresh one-time link while there is no
#    account; once there is one it refuses, and the console's sign-in is the way in.
took=$(( $(date +%s) - start ))
say ""
say "Beacon's try environment is up (${took}s):"
say "  http://localhost:17001          the API (/api/v1), MCP (/mcp) and the console (/console)"
say "  http://localhost:18080          the demo service this environment watches"
if link=$(docker compose exec -T beacon beacon setup 2>/dev/null) && [ -n "$link" ]; then
  say ""
  say "Make the first account (an owner): open this one-time link within 30 minutes:"
  printf '  %s\n' "$link"
else
  say ""
  say "An account exists already (or 'beacon setup' failed; run it by hand to see why)."
  say "Sign in at http://localhost:17001/console/sign-in"
fi
