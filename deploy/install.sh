#!/bin/sh
# Installs or updates Beacon's reference deployment on this machine (deploy/README.md; docs/install.md walks
# through the first half hour).
#   sudo ./install.sh                                              asks for the host names and the ACME address
#   STATUS_HOST=… METRICS_HOST=… ACME_EMAIL=… sudo ./install.sh    unattended
# Idempotent: it makes what is missing (.env, the secrets, the host names in the files), validates, pulls,
# starts, waits for health and prints the next steps. Run it again after changing a file or BEACON_VERSION.
set -eu

cd "$(dirname "$0")"
start=$(date +%s)
say() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run it as root (sudo ./install.sh): the secrets belong to Beacon's user, uid 65532"
command -v docker >/dev/null 2>&1 || die "Docker is not installed: https://docs.docker.com/engine/install/"
docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is missing (docker compose version fails)"
command -v ssh-keygen >/dev/null 2>&1 || die "ssh-keygen is missing (install the OpenSSH client)"

# 1. .env: Beacon's version, the two host names, the ACME address
[ -f .env ] || cp .env.example .env
value() { sed -n "s/^$1=//p" .env | tail -n 1; }
set_value() { # set_value NAME VALUE: in .env
  if grep -q "^$1=" .env; then
    sed "s|^$1=.*|$1=$2|" .env > .env.tmp && cat .env.tmp > .env && rm -f .env.tmp
  else
    printf '%s=%s\n' "$1" "$2" >> .env
  fi
}
pick() { # pick NAME CURRENT EXAMPLE PROMPT: the environment, else .env unless it still holds the example, else ask
  v=$2
  [ -n "$v" ] || v=$(value "$1")
  [ "$v" != "$3" ] || v=""
  if [ -z "$v" ]; then
    [ -t 0 ] || die "$1 is not set: put it in .env or in the environment"
    printf '%s: ' "$4" >&2
    read -r v
  fi
  [ -n "$v" ] || die "$1 is required"
  set_value "$1" "$v"
  printf '%s\n' "$v"
}
STATUS_HOST=$(pick STATUS_HOST "${STATUS_HOST:-}" status.example.com \
  "Host name for people and agents (the API, MCP, pages and SSH channel), such as status.example.com") || exit 1
METRICS_HOST=$(pick METRICS_HOST "${METRICS_HOST:-}" metrics.example.com \
  "Host name for machines (the gateway), such as metrics.example.com") || exit 1
ACME_EMAIL=$(pick ACME_EMAIL "${ACME_EMAIL:-}" "" "Address for Let's Encrypt's notices about the certificates") || exit 1
case "$ACME_EMAIL" in
  *@example.com | *@example.net | *@example.org) die "Let's Encrypt refuses example.com addresses; use a real one" ;;
  *@*) ;;
  *) die "ACME_EMAIL must be an address" ;;
esac
[ "$STATUS_HOST" != "$METRICS_HOST" ] || die "the two host names must differ: Caddy routes by name"
version=$(value BEACON_VERSION)
[ -n "$version" ] || die "BEACON_VERSION is not set in .env"

# 2. The secrets: the rules token and the SSH channel's two keys, made here and never leaving this machine.
# Beacon's user in the container is 65532; vmalert runs as the same user to read the rules token.
made=""
install -d -o 65532 -g 65532 -m 0750 secrets
if [ ! -s secrets/rules-token ]; then
  t=$(mktemp)
  head -c 32 /dev/urandom | base64 | tr -d '\n=+/' > "$t"
  install -o 65532 -g 65532 -m 0400 "$t" secrets/rules-token
  rm -f "$t"
  made="$made rules-token"
fi
for k in ssh-host-key ssh-ca-key; do
  if [ ! -s "secrets/$k" ]; then
    d=$(mktemp -d)
    ssh-keygen -q -t ed25519 -N '' -C "beacon $k" -f "$d/key"
    install -o 65532 -g 65532 -m 0400 "$d/key" "secrets/$k"
    rm -rf "$d"
    made="$made $k"
  fi
done
# other secret files the operator added (the chat, the phones) need the same owner and mode
for f in secrets/*; do
  [ -f "$f" ] && { chown 65532:65532 "$f"; chmod 0400 "$f"; }
done

# 3. The host names, where the files still carry the example ones (config.yaml, the probe targets)
for f in config.yaml targets/*.yaml; do
  if grep -q -e 'status\.example\.com' -e 'metrics\.example\.com' "$f"; then
    sed -e "s/status\.example\.com/$STATUS_HOST/g" -e "s/metrics\.example\.com/$METRICS_HOST/g" "$f" > "$f.tmp" &&
      cat "$f.tmp" > "$f" && rm -f "$f.tmp"
  fi
done

# 4. Validate before starting anything: the compose file, the Caddyfile, the rules, Beacon's configuration
# with the catalogue, targets and modules (each with its own image, no service started)
say "Validating with the images of $version …"
docker compose config -q || die "compose.yaml or .env is wrong (docker compose config says why)"
docker compose pull -q || die "could not pull the images"
docker compose run --rm --no-deps -T caddy caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null 2>&1 ||
  die "the Caddyfile does not validate: docker compose run --rm --no-deps caddy caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile"
docker compose run --rm --no-deps -T vmalert -dryRun -rule='/etc/vmalert/rules/*.yml' >/dev/null 2>&1 ||
  die "the rules do not validate: docker compose run --rm --no-deps vmalert -dryRun -rule='/etc/vmalert/rules/*.yml'"
docker compose run --rm --no-deps -T beacon check-config -config /etc/beacon/config.yaml ||
  die "config.yaml, the catalogue, the targets or the modules do not validate"

# 5. Start, and wait until all four are healthy (Beacon waits for VictoriaMetrics; Caddy's first certificates
# take a moment longer and do not count here)
docker compose up -d --remove-orphans --quiet-pull
say "Waiting for the containers to be healthy …"
healthy=0
i=0
while [ $i -lt 90 ]; do
  healthy=1
  for c in beacon beacon-victoriametrics beacon-vmalert beacon-caddy; do
    [ "$(docker inspect -f '{{.State.Health.Status}}' "$c" 2>/dev/null)" = healthy ] || { healthy=0; break; }
  done
  [ $healthy = 1 ] && break
  sleep 2
  i=$((i + 1))
done
[ $healthy = 1 ] || die "not every container is healthy after 3 minutes: docker compose ps; docker compose logs"

# 6. Check the loop: the gateway answers (401 without a token), and the rule engine's Watchdog reached it
metrics() { docker compose exec -T caddy wget -q -O - "$1" 2>/dev/null || true; }
code=$(docker compose exec -T caddy wget -q -S -O /dev/null http://beacon:17006/api/v1/targets 2>&1 | sed -n 's/^ *HTTP\/[0-9.]* \([0-9]*\).*/\1/p' | tail -n 1)
[ "$code" = 401 ] || die "the gateway does not answer as expected (401 without a token; got '${code:-nothing}')"
sent=0
i=0
while [ $i -lt 12 ]; do
  if metrics http://beacon:17001/metrics | grep -Eq '^beacon_gateway_requests_total\{[^}]*route="alerts"[^}]*code="200"'; then
    sent=1
    break
  fi
  sleep 10
  i=$((i + 1))
done
[ $sent = 1 ] || die "the rule engine's alerts have not reached the gateway in 2 minutes: docker compose logs vmalert"

took=$(( $(date +%s) - start ))
say ""
say "Beacon is up (${took}s)${made:+; made the secrets:$made}."
say "  https://$STATUS_HOST          the API (/api/v1), MCP (/mcp) and pages; the first certificate takes about a minute"
say "  https://$METRICS_HOST         the gateway for machines"
say "  $STATUS_HOST:17002            the SSH channel"
say ""
say "Next (docs/install.md has the details and how long each takes); run these from this directory:"
say "  1. A probe location (edge-1 is in catalog/locations.yaml; add yours there or through the API):"
say "       docker exec beacon beacon enroll probe edge-1 > /tmp/edge-1.token"
say "     then on that machine, with probe/ and the token copied over:"
say "       sh probe/install.sh -gateway https://$METRICS_HOST -token-file edge-1.token"
say "  2. A machine agents may reach (prints the lines its sshd needs):"
say "       docker exec beacon beacon machine add NAME HOST"
say "  3. A token for the agent program you already use, with its SSH key:"
say "       docker exec beacon beacon token create claude-code \\"
say "         -scopes status:read,alerts:write,machines:ssh,sessions:read -expires 90d -ssh-key \"\$(cat ~/.ssh/id_ed25519.pub)\""
say "       claude mcp add --transport http beacon https://$STATUS_HOST/mcp --header \"Authorization: Bearer <the token>\""
say "  4. A drill, to see the whole chain once: docker exec beacon beacon drill fail 15m"
say "  The chat: uncomment feishu: in config.yaml (docs/feishu.md), put its secrets under secrets/, run ./install.sh again."
