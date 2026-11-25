#!/usr/bin/env bash
# Exercises the released reference deployment on a disposable Linux runner with Docker and systemd.
# Local DNS and Caddy's local CA replace public DNS and ACME. The probe talks over verified HTTPS.
set -euo pipefail

cd "$(dirname "$0")"
# shellcheck source=smoke-failed-agent.sh
source ./smoke-failed-agent.sh
# shellcheck source=smoke-status-publication.sh
source ./smoke-status-publication.sh
# shellcheck source=smoke-page-links.sh
source ./smoke-page-links.sh
# shellcheck source=smoke-session-access.sh
source ./smoke-session-access.sh
# shellcheck source=smoke-personal-incident.sh
source ./smoke-personal-incident.sh
if [[ $(uname -s) != Linux || -e .env || -e secrets || -e /etc/beacon-probe || -e /opt/beacon-probe ]] ||
  id beacon >/dev/null 2>&1; then
  echo 'Use a clean checkout on a disposable Linux runner.' >&2
  exit 1
fi
docker info >/dev/null
# The reference installer uses fixed names. Refuse a runner that already holds any of its resources.
for container in beacon beacon-victoriametrics beacon-vmalert beacon-caddy; do
  if docker container inspect "$container" >/dev/null 2>&1; then
    echo "The runner already has the container $container." >&2
    exit 1
  fi
done
for volume in beacon_beacon-data beacon_victoriametrics-data beacon_caddy-data beacon_caddy-config; do
  if docker volume inspect "$volume" >/dev/null 2>&1; then
    echo "The runner already has the volume $volume." >&2
    exit 1
  fi
done
if docker network inspect beacon >/dev/null 2>&1; then
  echo 'The runner already has the beacon network.' >&2
  exit 1
fi
work=$(mktemp -d "${RUNNER_TEMP:?use a disposable Linux runner}/beacon-reference.XXXXXX")
start=$SECONDS
second_probe=
oncall_pid=
phone_pid=
private_hook_rule=false
token=
recovery_volume=
cleanup() {
  local result=$?
  smoke_restore_starter_status_file || true
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
    if [[ -f "$work/fail-agent" ]]; then
      tail -n 8 "$work/oncall.log" "$work/phone.log" >&2 || true
    fi
  fi
  if [[ -n "$second_probe" ]]; then
    kill "$second_probe" 2>/dev/null || true
  fi
  if [[ -n "$oncall_pid" ]]; then
    kill "$oncall_pid" 2>/dev/null || true
  fi
  if [[ -n "$phone_pid" ]]; then
    kill "$phone_pid" 2>/dev/null || true
  fi
  if sudo test -s "$work/sshd.pid"; then
    sudo kill "$(sudo cat "$work/sshd.pid")" 2>/dev/null || true
  fi
  sudo systemctl stop beacon-probe 2>/dev/null || true
  docker compose down -v >/dev/null 2>&1 || true
  if [[ "$private_hook_rule" == true ]]; then
    sudo iptables -D INPUT ! -i "$bridge" -d "$host/32" -p tcp --dport 22301 -j DROP 2>/dev/null || true
  fi
  if [[ -n "$recovery_volume" ]]; then
    docker volume rm "$recovery_volume" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

status_host=beacon-status.test
metrics_host=beacon-metrics.test
status_url=https://$status_host
metrics_url=https://$metrics_host
version=$(sed -n 's/^BEACON_VERSION=//p' .env.example)
test "$version" = "$(sed -n 's/^BEACON_VERSION=//p' try/.env.example)"
# Start the first-page acceptance with an empty catalogue, then restore the shipped example.
if [[ "$version" != v0.3.0-rc.36 ]]; then
  mv catalog/status_pages.yaml "$work/starter-status-pages.yaml"
fi
printf '127.0.0.1 %s %s\n' "$status_host" "$metrics_host" | sudo tee -a /etc/hosts >/dev/null
sed -i '/^{$\(STATUS_HOST\|METRICS_HOST\)} {/a\	tls internal' Caddyfile
# Keep the probes entirely on the runner and keep the other shipped probe targets.
sed -i 's#https://example.com/#https://status.example.com/healthz#' targets/all-web.yaml
if [[ "$version" != v0.3.0-rc.44 ]]; then
  sed -i '/^gateway:$/a\  exercise_target: https://status.example.com/drill/healthz' config.yaml
fi
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
bridge=$(ip -o -4 address show | awk -v gateway="$host" '{split($4, address, "/"); if (address[1] == gateway) print $2}')
test -n "$bridge"
sudo iptables -I INPUT 1 ! -i "$bridge" -d "$host/32" -p tcp --dport 22301 -j DROP
private_hook_rule=true
oncall_archive="beacon-oncall_${version#v}_linux_amd64.tar.gz"
release="https://github.com/openlinkos/beacon/releases/download/$version"
curl -fsSL --retry 3 "$release/SHA256SUMS" -o "$work/SHA256SUMS"
curl -fsSL --retry 3 "$release/$oncall_archive" -o "$work/$oncall_archive"
(cd "$work" && awk -v file="$oncall_archive" '$2 == file' SHA256SUMS | sha256sum -c --status)
tar -xzf "$work/$oncall_archive" -C "$work" beacon-oncall
"$work/beacon-oncall" version | grep -F "beacon-oncall ${version#v} ("
smoke_agent_reports=false
report_args=()
if smoke_runner_reports_supported "$work/beacon-oncall"; then
  smoke_agent_reports=true
  reporter_name=smoke-runner
  reporter_token=$(beacon token create "$reporter_name" -scopes alerts:report)
  printf '::add-mask::%s\n' "$reporter_token"
  printf '%s\n' "$reporter_token" > "$work/report-token"
  report_args=(-report-url "$status_url" -report-token-file "$work/report-token")
elif [[ "$version" == v0.3.0-rc.32 ]]; then
  echo 'The rc.32 reference harness must support Agent run reports.' >&2
  exit 1
else
  printf 'The pinned harness %s predates Agent run reporting.\n' "$version"
fi
openssl rand -hex 32 > "$work/hook-secret"
sudo install -o 65532 -g 65532 -m 0400 "$work/hook-secret" secrets/smoke-hook-secret
openssl rand -hex 32 > "$work/credential-key"
sudo install -o 65532 -g 65532 -m 0400 "$work/credential-key" secrets/smoke-credential-key
cat >> config.yaml <<YAML

credentials:
  enabled: true
  key_file: /etc/beacon/secrets/smoke-credential-key
  allowed_hosts: ['127.0.0.1:1']

agents:
  - name: smoke-oncall
    kind: webhook
    url: http://$host:22301/beacon
    secret_file: /etc/beacon/secrets/smoke-hook-secret
    service: drill
YAML
if [[ "$smoke_agent_reports" == true ]]; then
  printf '    reporter: "token:%s"\n' "$reporter_name" >> config.yaml
fi
"$work/beacon-oncall" -listen "$host:22301" -secret-file "$work/hook-secret" -log-dir "$work/hook-logs" \
  "${report_args[@]}" \
  -run "jq -ce 'select(.event == \"firing\" and .alert.labels.alertname == \"ServiceDown\")' > '$work/hook.tmp' && mv '$work/hook.tmp' '$work/hook.json' && if test -f '$work/fail-agent'; then exit 79; fi" \
  > "$work/oncall.log" 2>&1 &
oncall_pid=$!
hook_health() { docker compose exec -T caddy wget -q -O /dev/null "http://$host:22301/healthz"; }
wait_for 'Beacon network can reach the private on-call harness' 15 hook_health
if curl -fsS --noproxy '*' --max-time 2 "http://$host:22301/healthz" >/dev/null 2>&1; then
  echo 'The private hook accepted a connection outside the Beacon bridge.' >&2
  exit 1
fi
hook_rejection=$(docker compose exec -T caddy wget -S -O /dev/null \
  --header 'X-Beacon-Signature: sha256=00' --post-data '{}' "http://$host:22301/beacon" 2>&1 || true)
if [[ $(sed -n 's/^ *HTTP\/[0-9.]* \([0-9]*\).*/\1/p' <<< "$hook_rejection" | tail -1) != 401 ]]; then
  printf 'Expected the private on-call harness to reject a bad signature with HTTP 401.\n%s\n' "$hook_rejection" >&2
  exit 1
fi
test ! -e "$work/hook.json"
printf 'Private on-call harness rejected a bad signature from the Beacon network (HTTP 401).\n'
beacon check-config -config /etc/beacon/config.yaml
docker compose restart beacon
centre_health() { curl -fsS "$status_url/healthz" >/dev/null; }
wait_for 'Reference centre is ready with its on-call hook' 45 centre_health
if [[ "$smoke_agent_reports" == true ]]; then
  test "$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $reporter_token" \
    "$status_url/api/v1/alerts")" = 403
fi

setup=$(beacon setup)
form=$(curl -fsS -c "$work/cookies" "$setup" | sed -n 's/.*name="form" value="\([^"]*\)".*/\1/p')
test -n "$form"
password=$(openssl rand -hex 20)
test "$(curl -fsS -b "$work/cookies" -c "$work/cookies" -o /dev/null -w '%{http_code}' \
  --data-urlencode "form=$form" --data-urlencode "code=${setup##*code=}" --data-urlencode username=duty-person \
  --data-urlencode 'display_name=Test Operator' --data-urlencode "password=$password" \
  --data-urlencode "again=$password" "$status_url/console/setup")" = 303
curl -fsS -b "$work/cookies" -D "$work/console.headers" "$status_url/console" > "$work/console.html"
if grep -qi '^location: /workspace/' "$work/console.headers"; then
  curl -fsS -b "$work/cookies" "$status_url/console/api/v1/session" > "$work/console-session.json"
  jq -e '.name == "Test Operator"' "$work/console-session.json" >/dev/null
else
  grep -q 'Test Operator' "$work/console.html"
fi
if [[ "$version" == v0.3.0-rc.36 ]]; then
  printf 'The pinned release %s predates first status page creation in the console.\n' "$version"
else
  smoke_first_status_page
  smoke_restore_starter_status_file
  beacon config import /etc/beacon -note 'restore the starter status page after testing first publication'
  smoke_first_status_restored
fi
smoke_status_publication

# A real OpenSSH server trusts the reference install's CA and hosts the restricted account.
if [[ ! -x /usr/sbin/sshd ]]; then
  sudo apt-get -qq update
  sudo apt-get -y -qq install openssh-server
fi
ssh-keygen -q -t ed25519 -N '' -f "$work/target_key"
ca_public_key=$(sudo ssh-keygen -y -f secrets/ssh-ca-key)
printf '%s\n' "$ca_public_key" > "$work/ca.pub"
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
if [[ "$version" != v0.3.0-rc.43 ]]; then
  printf '%s\n' "$token" > "$work/agent-check-token"
  check_args=(-url "$status_url" -owner duty-person -agent recovery -machine target
    -agent-token-file "$work/agent-check-token" -report-token-file "$work/report-token")
  "$work/beacon-oncall" check "${check_args[@]}" -reporter "$reporter_name" > "$work/check-ready.log"
  grep -Fq 'is limited to enrolled machine "target"' "$work/check-ready.log"
  if "$work/beacon-oncall" check "${check_args[@]}" -reporter recovery > "$work/check-wrong.log" 2>&1; then
    echo 'The first drill check accepted a different configured reporter.' >&2
    exit 1
  fi
  grep -Fq 'the report token must be the standalone token named' "$work/check-wrong.log"
  test ! -e "$work/hook.json"
  printf 'The signed first-drill checker verified current Agent and reporter access and refused a reporter mismatch before injecting a fault.\n'
fi
if [[ "$version" != v0.3.0-rc.44 && "$version" != v0.3.0-rc.45 ]]; then
  curl -fsS -H "Authorization: Bearer $reporter_token" "$status_url/api/v1/agent-hooks/smoke-oncall/first-drill" |
    jq -e '.signed_webhook and .matches_drill and .drill_only and .critical_chat == "none"' >/dev/null
  if "$work/beacon-oncall" check "${check_args[@]}" -reporter "$reporter_name" \
    -hook smoke-oncall -chat notification_webhook > "$work/check-no-chat.log" 2>&1; then
    echo 'The first drill check accepted a deployment without a critical chat route.' >&2
    exit 1
  fi
  grep -Fq 'there is no configured critical chat route' "$work/check-no-chat.log"
  test ! -e "$work/hook.json"
  printf 'The signed checker inspected the loaded first-drill hook and refused the absent critical chat before dispatch.\n'
fi
rpc() {
  curl -fsS "$status_url/mcp" -H "Authorization: Bearer $token" \
    -H 'Accept: application/json, text/event-stream' -H 'Content-Type: application/json' \
    -H 'MCP-Protocol-Version: 2025-03-26' --data "$1"
}
rpc '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"reference-smoke","version":"1"}}}' |
  jq -e '.result.serverInfo.name != null' >/dev/null

if [[ "$version" != v0.3.0-rc.44 ]]; then
  curl -fsS -X POST -H "Authorization: Bearer $token" "$status_url/api/v1/machines/target/check" |
    jq -e '.machine == "target" and .route == "direct" and .status == "ready" and (.check_id | length > 0)' >/dev/null
  rpc '{"jsonrpc":"2.0","id":63,"method":"tools/call","params":{"name":"check_machine_ssh","arguments":{"machine":"target"}}}' |
    jq -e '.result.structuredContent | .machine == "target" and .route == "direct" and .status == "ready" and (.check_id | length > 0)' >/dev/null
  curl -fsS -H "Authorization: Bearer $token" "$status_url/api/v1/sessions?machine=target" |
    jq -e '.sessions | length == 0' >/dev/null
  printf 'The signed release verified the enrolled SSH route and certificate through REST and MCP before any recorded command.\n'
fi

probe_target_groups() {
  printf 'header = "Authorization: Bearer %s"\n' "$(cat "$work/edge-1.token")" |
    curl -fsS --config - "$metrics_url/api/v1/targets" |
    jq -Sc .
}
if [[ "$version" != v0.3.0-rc.47 ]]; then
  assignment_token=$(beacon token create monitor-assignment -scopes status:read,config:write,machines:write,machines:ssh -machines target)
  printf '::add-mask::%s\n' "$assignment_token"
  config_version=$(curl -fsS -H "Authorization: Bearer $assignment_token" "$status_url/api/v1/config/targets" |
    jq -er '.version | select(. > 0)')
  registration=$(curl -fsS -H "Authorization: Bearer $assignment_token" "$status_url/api/v1/machines" |
    jq -er '.machines[] | select(.name == "target") | .registration | select(length > 0)')
  probe_target_groups > "$work/targets-before-machine"
  body=$(jq -nc --argjson version "$config_version" --arg registration "$registration" \
    '{if_version:$version,machine:{name:"target",registration:$registration},note:"Reference response machine"}')
  curl -fsS -X PUT -H "Authorization: Bearer $assignment_token" -H 'Content-Type: application/json' \
    --data "$body" "$status_url/api/v1/config/targets/all-drill/groups/0/machine" |
    jq -e --argjson version "$config_version" '.version == ($version + 1) and any(.items[]; .key == "all-drill")' >/dev/null
  probe_target_groups > "$work/targets-after-machine"
  cmp "$work/targets-before-machine" "$work/targets-after-machine"
  beacon token revoke monitor-assignment >/dev/null
fi

drill_started=$SECONDS
drill_output=$(beacon drill fail 15m)
printf '%s\n' "$drill_output"
drill_run=$(sed -n 's/^drill run //p' <<< "$drill_output")
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

retained_started_episode() {
  local state=$1 request
  # Read the same frozen start window after resolution and restore.
  # shellcheck disable=SC2016
  local match='any(.episodes[]; .episode == $episode and .started_at == $start and .opening_severity == "critical" and .state == $state)'
  curl -fsSG "$status_url/api/v1/incident-episodes" -H "Authorization: Bearer $token" \
    --data-urlencode "group=$alert" --data-urlencode "since=$episode_since" --data-urlencode "until=$episode_until" |
    jq -e --arg episode "$cohort_episode" --arg start "$episode_started" --arg state "$state" "$match" >/dev/null
  request=$(jq -nc --arg group "$alert" --arg since "$episode_since" --arg until "$episode_until" \
    '{jsonrpc:"2.0",id:64,method:"tools/call",params:{name:"list_incident_episodes",arguments:{group:$group,since:$since,until:$until}}}')
  rpc "$request" |
    jq -e --arg episode "$cohort_episode" --arg start "$episode_started" --arg state "$state" ".result.structuredContent | $match" >/dev/null
}
if [[ "$version" != v0.3.0-rc.44 && "$version" != v0.3.0-rc.45 ]]; then
  cohort_episode=$(jq -er '.alert.episode | select(length > 0)' "$work/hook.json")
  started=$(curl -fsSG "$status_url/api/v1/incident-episodes" -H "Authorization: Bearer $token" --data-urlencode "group=$alert")
  episode_since=$(jq -er '.since' <<< "$started")
  episode_until=$(jq -er '.until' <<< "$started")
  episode_started=$(jq -er --arg episode "$cohort_episode" '.episodes[] | select(.episode == $episode) | .started_at' <<< "$started")
  retained_started_episode open
  curl -fsS -b "$work/cookies" "$status_url/console/incident-episodes?group=$alert" > "$work/started-episodes.html"
  grep -Fq "id=\"episodes-since\"" "$work/started-episodes.html"
fi

retained_designated_machine() {
  local request
  local match='.designated_machines == [{service:"drill",name:"target"}]'
  curl -fsS -H "Authorization: Bearer $token" "$status_url/api/v1/alerts/$alert/evidence" | jq -e "$match" >/dev/null
  request=$(jq -nc --arg alert "$alert" '{jsonrpc:"2.0",id:65,method:"tools/call",params:{name:"get_evidence",arguments:{id:$alert}}}')
  rpc "$request" | jq -e ".result.structuredContent | $match" >/dev/null
}
if [[ "$version" != v0.3.0-rc.47 ]]; then
  retained_designated_machine
  curl -fsS -b "$work/cookies" "$status_url/console/alerts/$alert" > "$work/designated-machine.html"
  grep -Fq 'Designated service machines' "$work/designated-machine.html"
fi
run=$(rpc '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"run_command","arguments":{"machine":"target","command":"id -un"}}}')
jq -e '.result.structuredContent | .exit_code == 0 and .stdout == "beacon\n"' <<< "$run" >/dev/null
session=$(jq -er '.result.structuredContent.session' <<< "$run")
curl -fsS "$status_url/api/v1/sessions/$session/recording" -H "Authorization: Bearer $token" > "$work/recording"
test -s "$work/recording"
note_text='Verified restricted machine access and the recorded session.'
note=$(jq -nc --arg alert "$alert" --arg text "$note_text" '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"add_note","arguments":{"id":$alert,"text":$text}}}')
rpc "$note" | jq -e '.result != null and .result.isError != true' >/dev/null
beacon drill off
probe=$(jq -nc --arg alert "$alert" '{jsonrpc:"2.0",id:4,method:"tools/call",params:{name:"probe_now",arguments:{alert_id:$alert,wait:45}}}')
probe_result=$(rpc "$probe")
jq -e '.result.structuredContent | .complete and (.results | length == 2 and all(.[]; .ok) and (map(.location) | unique | sort == ["edge-1", "edge-2"]))' <<< "$probe_result" >/dev/null
wait_for 'Both probes saw recovery' 120 probes 1
resolved() {
  curl -fsS "$status_url/api/v1/alerts/$alert" -H "Authorization: Bearer $token" |
    jq -e '.status == "resolved"' >/dev/null
}
wait_for 'The same alert resolved' 150 resolved
if [[ "$version" != v0.3.0-rc.44 && "$version" != v0.3.0-rc.45 ]]; then
  retained_started_episode resolved
fi
retained_agent_check() {
  local check_id request
  check_id=$(jq -er '.result.structuredContent.id' <<< "$probe_result")
  # $id is a jq argument shared by the REST and MCP assertions.
  # shellcheck disable=SC2016
  local match='any(.outcomes[]; .passing_check.id == $id and .passing_check.actor_kind == "agent" and
    .passing_check.by == "duty-person / recovery" and .passing_check.passed == 2 and
    .passing_check.failed == 0 and .passing_check.missing == 0)'
  curl -fsSG "$status_url/api/v1/incident-outcomes" -H "Authorization: Bearer $token" \
    --data-urlencode "group=$alert" --data-urlencode evidence=agent_passing_check |
    jq -e --arg id "$check_id" "$match" >/dev/null
  request=$(jq -nc --arg alert "$alert" '{jsonrpc:"2.0",id:62,method:"tools/call",
    params:{name:"list_incident_outcomes",arguments:{group:$alert,evidence:"agent_passing_check"}}}')
  rpc "$request" | jq -e --arg id "$check_id" ".result.structuredContent | $match" >/dev/null
}
retained_exercise_origin() {
  test -n "$drill_run"
  curl -fsSG "$status_url/api/v1/incident-outcomes" -H "Authorization: Bearer $token" \
    --data-urlencode "group=$alert" --data-urlencode origin=exercise |
    jq -e --arg run "$drill_run" '.outcomes | any(.origin == "exercise" and .exercise_run == $run)' >/dev/null
  curl -fsSG "$status_url/api/v1/incident-outcomes" -H "Authorization: Bearer $token" \
    --data-urlencode "group=$alert" --data-urlencode origin=unknown |
    jq -e --arg run "$drill_run" '.outcomes | all(.exercise_run != $run)' >/dev/null
}
if [[ "$version" != v0.3.0-rc.44 ]]; then
  retained_agent_check
  retained_exercise_origin
fi
printf 'Fresh reference install, Agent MCP, SSH recording and recovered drill passed in %ss\n' "$((SECONDS - start))"
if [[ "$version" == v0.3.0-rc.38 ]]; then
  printf 'The pinned release %s predates machine-scoped session reads.\n' "$version"
else
  smoke_session_access "$session" "$host:22300"
fi
if [[ "$version" == v0.3.0-rc.37 ]]; then
  printf 'The pinned release %s predates revocable page links.\n' "$version"
else
  smoke_page_link_revocation "$alert" "$session"
fi

if [[ "$version" != v0.3.0-rc.42 ]]; then
  smoke_session_evidence "$session"
fi

if [[ "$version" != v0.3.0-rc.43 ]]; then
  smoke_scoped_search
fi

# Add and revoke a fixture grant before the backup. The endpoint is inside the Beacon container and
# is unused by the test. The REST list verifies delegation without sending any upstream request.
credential_agent=credential-recovery
credential_token=$(beacon agent add "$credential_agent" -owner duty-person -scopes secrets:use 2>"$work/credential-agent.log")
printf '::add-mask::%s\n' "$credential_token"
credential_value=$(openssl rand -hex 32)
printf '::add-mask::%s\n' "$credential_value"
credential_post() {
  local page=$1 action=$2 csrf
  shift 2
  csrf=$(curl -fsS -b "$work/cookies" "$status_url$page" |
    sed -n 's/.*name="csrf" value="\([^"]*\)".*/\1/p' | awk 'NR == 1 {print}') || return 1
  test -n "$csrf" || return 1
  test "$(curl -fsS -b "$work/cookies" -o /dev/null -D "$work/credential.headers" -w '%{http_code}' \
    --data-urlencode "csrf=$csrf" "$@" "$status_url$action")" = 303 || return 1
  awk 'tolower($1) == "location:" {sub(/\r$/, "", $2); print $2}' "$work/credential.headers"
}
credential_created=$(credential_post /console/credentials /console/credentials \
  --data-urlencode 'name=Reference recovery fixture' --data-urlencode 'endpoint=https://127.0.0.1:1/ready' \
  --data-urlencode "value=$credential_value")
[[ "$credential_created" == /console/credentials/*\?done=saved ]]
credential_page=${credential_created%%\?*}
credential_id=${credential_page##*/}
test "$(credential_post "$credential_page" "$credential_page/grant" --data-urlencode "agent=$credential_agent" \
  --data-urlencode version=1 --data-urlencode hours=1)" = "$credential_page?done=saved"
credential_api="$status_url/api/v1/team-credentials"
curl -fsS -H "Authorization: Bearer $credential_token" "$credential_api" |
  jq -e --arg id "$credential_id" 'any(.credentials[]; .id == $id)' >/dev/null
test "$(credential_post "$credential_page" "$credential_page/revoke" --data-urlencode "agent=$credential_agent" \
  --data-urlencode version=2)" = "$credential_page?done=saved"

# Rehearse the documented cold backup and restore with the same Alpine helper. The independent
# epoch is captured from the stopped service into its own directory before restoring the archive.
mkdir "$work/archive" "$work/anchor"
docker compose stop beacon
docker run --rm --volumes-from beacon:ro -v "$work/archive:/backup" -v "$work/anchor:/anchor" alpine \
  sh -ec 'umask 077
    cd /var/lib/beacon
    set -- beacon.db*
    if [ -d recordings ]; then set -- "$@" recordings; fi
    test -s team-credentials/epochs
    cp team-credentials/epochs /anchor/team-credential-epochs
    tar -czf /backup/beacon.tgz "$@" team-credentials/epochs'

volume_name="beacon-smoke-restore-${work##*.}"
if docker volume inspect "$volume_name" >/dev/null 2>&1; then
  echo 'The recovery volume already exists.' >&2
  exit 1
fi
recovery_volume=$(docker volume create "$volume_name")
docker run --rm -v "$recovery_volume:/var/lib/beacon" -v "$work/archive:/backup:ro" alpine \
  sh -ec 'test -z "$(ls -A /var/lib/beacon)"
    tar -C /var/lib/beacon --exclude=team-credentials/epochs -xzf /backup/beacon.tgz
    test -s /var/lib/beacon/beacon.db
    test -d /var/lib/beacon/recordings
    test ! -e /var/lib/beacon/team-credentials/epochs
    chown 65532:65532 /var/lib/beacon
    chmod 0700 /var/lib/beacon'
cat > "$work/recovery.compose.yml" <<YAML
volumes:
  beacon-data:
    name: $recovery_volume
    external: true
YAML
restore_compose() { docker compose -f compose.yaml -f "$work/recovery.compose.yml" "$@"; }
restore_compose up -d --no-deps --force-recreate beacon
test "$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/var/lib/beacon"}}{{.Name}}{{end}}{{end}}' beacon)" = "$recovery_volume"
wait_for 'Restored centre is ready' 60 centre_health
test "$(curl -sS -b "$work/cookies" -o /dev/null -w '%{http_code}' "$status_url/console/credentials")" = 503
test "$(curl -sS -H "Authorization: Bearer $credential_token" -o "$work/credential-error.json" \
  -w '%{http_code}' "$credential_api")" = 503
jq -e '.code == "credential.unavailable"' "$work/credential-error.json" >/dev/null

restore_compose stop beacon
docker run --rm --volumes-from beacon -v "$work/anchor:/backup:ro" alpine \
  sh -ec 'test -s /backup/team-credential-epochs
    install -d -m 0700 -o 65532 -g 65532 /var/lib/beacon/team-credentials
    install -m 0600 -o 65532 -g 65532 /backup/team-credential-epochs /var/lib/beacon/team-credentials/epochs
    sync'
restore_compose start beacon
wait_for 'Restored centre is ready with the independent anchor' 60 centre_health

form=$(curl -fsS -c "$work/restored-cookies" "$status_url/console/sign-in" |
  sed -n 's/.*name="form" value="\([^"]*\)".*/\1/p')
test -n "$form"
test "$(curl -fsS -b "$work/restored-cookies" -c "$work/restored-cookies" -o /dev/null -w '%{http_code}' \
  --data-urlencode "form=$form" --data-urlencode username=duty-person --data-urlencode "password=$password" \
  "$status_url/console/sign-in")" = 303
curl -fsS -b "$work/restored-cookies" "$status_url$credential_page" > "$work/restored-credential.html"
grep -Fq 'Reference recovery fixture' "$work/restored-credential.html"
grep -Fq 'No agent grants for this credential.' "$work/restored-credential.html"
if grep -Fq "$credential_value" "$work/restored-credential.html"; then
  echo 'The restored console disclosed the credential value.' >&2
  exit 1
fi
test "$(curl -sS -X POST -H "Authorization: Bearer $credential_token" -o "$work/credential-error.json" \
  -w '%{http_code}' "$credential_api/$credential_id/use")" = 403
jq -e '.code == "credential.denied"' "$work/credential-error.json" >/dev/null

resolved
if [[ "$version" != v0.3.0-rc.44 ]]; then
  retained_agent_check
  retained_exercise_origin
  printf 'REST and MCP retained the identified Agent passing check before and after cold restore.\n'
  printf 'The signed reference matched the recorded exercise run to the first rule quorum and preserved it after cold restore.\n'
fi
if [[ "$version" != v0.3.0-rc.44 && "$version" != v0.3.0-rc.45 ]]; then
  retained_started_episode resolved
  printf 'REST and MCP retained the same started episode while open, after resolution and after cold restore.\n'
fi
if [[ "$version" != v0.3.0-rc.47 ]]; then
  retained_designated_machine
  printf 'The signed imported response-machine assignment kept probe targets identical and stayed visible in the incident after cold restore.\n'
fi
curl -fsS "$status_url/api/v1/alerts/$alert/timeline" -H "Authorization: Bearer $token" |
  jq -e --arg note "$note_text" 'any(.events[]; .kind == "note" and .text == $note)' >/dev/null
curl -fsS "$status_url/api/v1/sessions/$session/recording" -H "Authorization: Bearer $token" > "$work/restored-recording"
cmp "$work/recording" "$work/restored-recording"
curl -fsS -b "$work/restored-cookies" "$status_url/console/sessions/$session" > "$work/restored-replay.html"
grep -Fq 'id="player"' "$work/restored-replay.html"
if [[ "$version" != v0.3.0-rc.37 ]]; then
  smoke_page_links_restored
fi
printf 'Cold SQLite restore preserved account access, the incident note, SSH replay and credential revocation after %ss\n' "$((SECONDS - start))"

# A fresh episode preserves the separate successful Agent and restore checks above. The Bark
# endpoint belongs to this runner and records provider acceptance without reaching a device.
smoke_phone_receiver "$host" 22302 "$work" > "$work/phone.log" 2>&1 &
phone_pid=$!
phone_health() { curl -fsS --noproxy '*' "http://$host:22302/healthz" >/dev/null; }
wait_for 'The isolated phone receiver is listening' 15 phone_health
printf 'http://%s:22302/bark\n' "$host" > "$work/bark-url"
sudo install -o 65532 -g 65532 -m 0400 "$work/bark-url" secrets/smoke-bark-url
cat >> config.yaml <<'YAML'

bark:
  url_file: /etc/beacon/secrets/smoke-bark-url

escalation:
  critical: {channel: none, bark: 20s}
YAML
beacon check-config -config /etc/beacon/config.yaml
restore_compose restart beacon
wait_for 'The restored centre is ready for the failed-Agent drill' 45 centre_health
test "$(curl -fsS -o /dev/null -w '%{http_code}' "$status_url/drill/healthz")" = 200
wait_for 'Both probes see a healthy service before the failed-Agent drill' 90 probes 1
smoke_failed_agent_drill "$alert"
if [[ "$version" != v0.3.0-rc.43 ]]; then
  smoke_personal_incident
fi
