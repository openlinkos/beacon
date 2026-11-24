#!/usr/bin/env bash
# Exercises the released reference deployment on a disposable Linux runner with Docker and systemd.
# Local DNS and Caddy's local CA replace public DNS and ACME. The probe talks over verified HTTPS.
set -euo pipefail

cd "$(dirname "$0")"
work=$(mktemp -d "${RUNNER_TEMP:?use a disposable Linux runner}/beacon-reference.XXXXXX")
start=$SECONDS
second_probe=
oncall_pid=
token=
cleanup() {
  local result=$?
  if (( result != 0 )); then
    for metric in probe_success beacon:probe_failing beacon:service_down; do
      curl -fsSG --max-time 5 http://127.0.0.1:8428/api/v1/query --data-urlencode "query=$metric{service=\"drill\"}" |
        jq -c --arg metric "$metric" '{metric: $metric, samples: .data.result}' || true
    done
    curl -fsS --max-time 5 http://127.0.0.1:8880/api/v1/rules |
      jq -c '[.data.groups[].rules[] | select(.name == "ServiceDown") | {name, state, health, lastError, alerts}]' || true
    if [[ -n "$token" ]]; then
      curl -fsS --max-time 5 "https://beacon-status.test/api/v1/alerts?state=all" -H "Authorization: Bearer $token" |
        jq -c '[.alerts[] | {id, service, status, labels}]' || true
    fi
  fi
  if [[ -n "$second_probe" ]]; then
    kill "$second_probe" 2>/dev/null || true
  fi
  if [[ -n "$oncall_pid" ]]; then
    kill "$oncall_pid" 2>/dev/null || true
  fi
  if sudo test -s "$work/sshd.pid"; then
    sudo kill "$(sudo cat "$work/sshd.pid")" 2>/dev/null || true
  fi
  sudo systemctl stop beacon-probe 2>/dev/null || true
  docker compose down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

status_host=beacon-status.test
metrics_host=beacon-metrics.test
status_url=https://$status_host
metrics_url=https://$metrics_host
version=$(sed -n 's/^BEACON_VERSION=//p' .env.example)
test "$version" = "$(sed -n 's/^BEACON_VERSION=//p' try/.env.example)"
printf '127.0.0.1 %s %s\n' "$status_host" "$metrics_host" | sudo tee -a /etc/hosts >/dev/null
sed -i '/^{$\(STATUS_HOST\|METRICS_HOST\)} {/a\	tls internal' Caddyfile
# Keep the probes entirely on the runner and keep the other shipped probe targets.
sed -i 's#https://example.com/#https://status.example.com/healthz#' targets/all-web.yaml
sudo env STATUS_HOST="$status_host" METRICS_HOST="$metrics_host" ACME_EMAIL=ops@ci.invalid ./install.sh
docker exec beacon beacon version | grep -F "beacon ${version#v} ("
docker compose cp caddy:/data/caddy/pki/authorities/local/root.crt "$work/caddy.crt"
sudo install -m 0644 "$work/caddy.crt" /usr/local/share/ca-certificates/beacon-smoke.crt
sudo update-ca-certificates >/dev/null
curl -fsS "$status_url/healthz" >/dev/null
test "$(curl -sS -o /dev/null -w '%{http_code}' "$metrics_url/api/v1/targets")" = 401
printf 'Reference centre ready after %ss\n' "$((SECONDS - start))"

umask 077
beacon() { docker exec beacon beacon "$@"; }
beacon enroll probe edge-1 > "$work/edge-1.token"
sudo sh probe/install.sh -gateway "$metrics_url" -token-file "$work/edge-1.token"
/opt/beacon-probe/bin/beacon-probe version | grep -F "${version#v} ("
cat >> catalog/locations.yaml <<'YAML'
  - id: edge-2
    name: Test East
    region: overseas
    network: BGP
YAML
beacon config import /etc/beacon -dry-run
beacon config import /etc/beacon -note 'second test location'
beacon enroll probe edge-2 > "$work/edge-2.token"
/opt/beacon-probe/bin/beacon-probe agent -gateway "$metrics_url" -token-file "$work/edge-2.token" \
  -state "$work/edge-2" -interval 15s -listen 127.0.0.1:27009 > "$work/probe.log" 2>&1 &
second_probe=$!
query() { curl -fsSG http://127.0.0.1:8428/api/v1/query --data-urlencode "query=$1"; }
wait_for() {
  local description=$1 timeout=$2 deadline
  shift 2
  deadline=$((SECONDS + timeout))
  until "$@"; do
    if (( SECONDS >= deadline )); then
      printf '%s timed out after %ss\n' "$description" "$timeout" >&2
      return 1
    fi
    sleep 3
  done
  printf '%s after %ss\n' "$description" "$((SECONDS - start))"
}
probes() {
  query 'probe_success{service="drill"}' |
    jq -e --arg value "$1" '[.data.result[] | select(.value[1] == $value) | .metric.location] | unique | sort == ["edge-1", "edge-2"]' >/dev/null
}
wait_for 'Both installed probes reported a healthy drill' 120 probes 1

# Receive a real signed wake in the released harness. The fixture program only accepts the ServiceDown episode.
host=$(docker network inspect beacon --format '{{(index .IPAM.Config 0).Gateway}}')
oncall_archive="beacon-oncall_${version#v}_linux_amd64.tar.gz"
release="https://github.com/openlinkos/beacon/releases/download/$version"
curl -fsSL --retry 3 "$release/SHA256SUMS" -o "$work/SHA256SUMS"
curl -fsSL --retry 3 "$release/$oncall_archive" -o "$work/$oncall_archive"
(cd "$work" && awk -v file="$oncall_archive" '$2 == file' SHA256SUMS | sha256sum -c --status)
tar -xzf "$work/$oncall_archive" -C "$work" beacon-oncall
"$work/beacon-oncall" version | grep -F "beacon-oncall ${version#v} ("
openssl rand -hex 32 > "$work/hook-secret"
sudo install -o 65532 -g 65532 -m 0400 "$work/hook-secret" secrets/smoke-hook-secret
cat >> config.yaml <<YAML

agents:
  - name: smoke-oncall
    kind: webhook
    url: http://$host:22301/beacon
    secret_file: /etc/beacon/secrets/smoke-hook-secret
    service: drill
YAML
"$work/beacon-oncall" -listen "$host:22301" -secret-file "$work/hook-secret" -log-dir "$work/hook-logs" \
  -run "jq -ce 'select(.event == \"firing\" and .alert.labels.alertname == \"ServiceDown\")' > '$work/hook.tmp' && mv '$work/hook.tmp' '$work/hook.json'" \
  > "$work/oncall.log" 2>&1 &
oncall_pid=$!
hook_health() { curl -fsS "http://$host:22301/healthz" >/dev/null; }
wait_for 'Released on-call harness is listening' 15 hook_health
beacon check-config -config /etc/beacon/config.yaml
docker compose restart beacon
centre_health() { curl -fsS "$status_url/healthz" >/dev/null; }
wait_for 'Reference centre is ready with its on-call hook' 45 centre_health

setup=$(beacon setup)
form=$(curl -fsS -c "$work/cookies" "$setup" | sed -n 's/.*name="form" value="\([^"]*\)".*/\1/p')
test -n "$form"
password=$(openssl rand -hex 20)
test "$(curl -fsS -b "$work/cookies" -c "$work/cookies" -o /dev/null -w '%{http_code}' \
  --data-urlencode "form=$form" --data-urlencode "code=${setup##*code=}" --data-urlencode username=duty-person \
  --data-urlencode 'display_name=Test Operator' --data-urlencode "password=$password" \
  --data-urlencode "again=$password" "$status_url/console/setup")" = 303
curl -fsS -b "$work/cookies" "$status_url/console" > "$work/console.html"
grep -q 'Test Operator' "$work/console.html"

# A real OpenSSH server trusts the reference install's CA and hosts the restricted account.
if [[ ! -x /usr/sbin/sshd ]]; then
  sudo apt-get -qq update
  sudo apt-get -y -qq install openssh-server
fi
ssh-keygen -q -t ed25519 -N '' -f "$work/target_key"
sudo ssh-keygen -y -f secrets/ssh-ca-key > "$work/ca.pub"
sudo install -m 0644 "$work/ca.pub" /etc/ssh/beacon-reference-ca.pub
sudo install -d -m 0755 /run/sshd
sudo useradd -m -s /bin/sh -p '*' beacon
cat > "$work/sshd.conf" <<CONFIG
ListenAddress $host
Port 22300
HostKey $work/target_key
PidFile $work/sshd.pid
TrustedUserCAKeys /etc/ssh/beacon-reference-ca.pub
AuthenticationMethods publickey
PasswordAuthentication no
PermitRootLogin no
AllowUsers beacon
CONFIG
sudo /usr/sbin/sshd -f "$work/sshd.conf"
beacon machine add target "$host:22300"
if ! token=$(beacon agent add recovery -owner duty-person -preset operator -machines target 2>"$work/agent-instructions"); then
  echo 'Could not enrol the MCP agent' >&2
  exit 1
fi
printf '::add-mask::%s\n' "$token"
rpc() {
  curl -fsS "$status_url/mcp" -H "Authorization: Bearer $token" \
    -H 'Accept: application/json, text/event-stream' -H 'Content-Type: application/json' \
    -H 'MCP-Protocol-Version: 2025-03-26' --data "$1"
}
rpc '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"reference-smoke","version":"1"}}}' |
  jq -e '.result.serverInfo.name != null' >/dev/null

drill_started=$SECONDS
beacon drill fail 15m
test "$(curl -sS -o /dev/null -w '%{http_code}' "$status_url/drill/healthz")" = 503
wait_for 'Both probes saw the failed drill' 120 probes 0
firing() {
  curl -fsS "$status_url/api/v1/alerts" -H "Authorization: Bearer $token" |
    jq -er '[.alerts[] | select(.labels.alertname == "ServiceDown" and .service == "drill" and .status == "firing") | .id][0] // empty'
}
wait_for 'Reference rules opened the drill alert' "$((drill_started + 300 - SECONDS))" firing > "$work/alert"
alert=$(head -1 "$work/alert")
tail -1 "$work/alert"
printf 'The alert reached Beacon %ss after the injected failure\n' "$((SECONDS - drill_started))"
hook_firing() {
  [[ -s "$work/hook.json" ]] && jq -e --arg alert "$alert" '.alert.id == $alert' "$work/hook.json" >/dev/null
}
wait_for 'Signed drill alert started the Agent fixture' "$((drill_started + 300 - SECONDS))" hook_firing
(( SECONDS <= drill_started + 300 ))
printf 'The on-call harness started the Agent fixture %ss after the injected failure\n' "$((SECONDS - drill_started))"
run=$(rpc '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"run_command","arguments":{"machine":"target","command":"id -un"}}}')
jq -e '.result.structuredContent | .exit_code == 0 and .stdout == "beacon\n"' <<< "$run" >/dev/null
session=$(jq -er '.result.structuredContent.session' <<< "$run")
curl -fsS "$status_url/api/v1/sessions/$session/recording" -H "Authorization: Bearer $token" > "$work/recording"
test -s "$work/recording"
note=$(jq -nc --arg alert "$alert" '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"add_note","arguments":{"id":$alert,"text":"Verified restricted machine access and the recorded session."}}}')
rpc "$note" | jq -e '.result != null and .result.isError != true' >/dev/null
beacon drill off
probe=$(jq -nc --arg alert "$alert" '{jsonrpc:"2.0",id:4,method:"tools/call",params:{name:"probe_now",arguments:{alert_id:$alert,wait:45}}}')
rpc "$probe" | jq -e '.result.structuredContent | .complete and (.results | length == 2 and all(.[]; .ok) and (map(.location) | unique | sort == ["edge-1", "edge-2"]))' >/dev/null
wait_for 'Both probes saw recovery' 120 probes 1
resolved() {
  curl -fsS "$status_url/api/v1/alerts/$alert" -H "Authorization: Bearer $token" |
    jq -e '.status == "resolved"' >/dev/null
}
wait_for 'The same alert resolved' 150 resolved
printf 'Fresh reference install, Agent MCP, SSH recording and recovered drill passed in %ss\n' "$((SECONDS - start))"
