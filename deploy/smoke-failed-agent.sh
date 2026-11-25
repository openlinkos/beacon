#!/usr/bin/env bash
# Sourced by smoke.sh on the disposable reference runner. The phone receiver only records requests.

smoke_runner_reports_supported() {
  local help
  help=$("$1" -help 2>&1) || true
  grep -Eq '^[[:space:]]+-report-url([[:space:]]|$)' <<< "$help" &&
    grep -Eq '^[[:space:]]+-report-token-file([[:space:]]|$)' <<< "$help"
}

smoke_phone_receiver() {
  exec python3 - "$1" "$2" "$3" <<'PY'
from http.server import BaseHTTPRequestHandler, HTTPServer
from ipaddress import ip_address
import json
from pathlib import Path
import re
import sys
import time
from urllib.parse import parse_qs, urlsplit

host, port, receipt_dir = sys.argv[1:]
if not ip_address(host).is_private:
    sys.exit("Use a private test address for the receiver")

class Receiver(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/healthz":
            self.send_error(404)
            return
        self.send_response(204)
        self.end_headers()

    def do_POST(self):
        if self.path != "/bark":
            self.send_error(404)
            return
        try:
            size = int(self.headers.get("Content-Length", "0"))
            if not 0 < size <= 4096 or self.headers.get_content_type() != "application/json":
                raise ValueError("invalid request")
            payload = json.loads(self.rfile.read(size))
            if payload["level"] != "critical" or not isinstance(payload["url"], str):
                raise ValueError("missing critical incident notification")
            targets = parse_qs(urlsplit(payload["url"]).query).get("next", [])
            match = re.fullmatch(r"/console/alerts/([0-9a-f]{12})(?:/episodes/[0-9a-f]{16})?", targets[0]) if len(targets) == 1 else None
            if not match:
                raise ValueError("missing alert group")
        except (ValueError, TypeError, KeyError):
            self.send_error(400)
            return
        # The test CA can also trigger a certificate alert. Keep each group's receipt separately.
        receipt = Path(receipt_dir) / f"phone-{match[1]}.json"
        tmp = receipt.with_suffix(".tmp")
        tmp.write_text(json.dumps({"received_at": time.time(), "payload": payload}), encoding="utf-8")
        tmp.replace(receipt)
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"code":200,"message":"success"}')

with HTTPServer((host, int(port)), Receiver) as server:
    server.serve_forever()
PY
}

# Use separate cookie jars for invitation and sign-in to exercise the received link without a session.
smoke_shift_sign_in() {
  local person=$1 entry=$2 page=$3 invite form secret
  secret=$(openssl rand -hex 20)
  invite=$(beacon user invite -role oncall -note "Reference drill: $person" 2>"$work/$person.invite.log")
  [[ "$invite" == "$status_url/console/join?code="* ]]
  form=$(curl -fsS -c "$work/$person.join" "$invite" | sed -n 's/.*name="form" value="\([^"]*\)".*/\1/p')
  test -n "$form"
  test "$(curl -fsS -b "$work/$person.join" -o /dev/null -w '%{http_code}' \
    --data-urlencode "form=$form" --data-urlencode "code=${invite##*code=}" \
    --data-urlencode "username=$person" --data-urlencode "password=$secret" --data-urlencode "again=$secret" \
    "$status_url/console/join")" = 303
  form=$(curl -fsS -c "$work/$person.cookies" "$entry" | sed -n 's/.*name="form" value="\([^"]*\)".*/\1/p')
  test -n "$form"
  test "$(curl -fsS -b "$work/$person.cookies" -c "$work/$person.cookies" -o /dev/null \
    -D "$work/$person.headers" -w '%{http_code}' --data-urlencode "form=$form" \
    --data-urlencode "username=$person" --data-urlencode "password=$secret" --data-urlencode "next=$page" \
    "$status_url/console/sign-in")" = 303
  test "$(awk 'tolower($1) == "location:" {sub(/\r$/, "", $2); print $2}' "$work/$person.headers")" = "$page"
}

smoke_shift_claim() {
  local person=$1 page=$2 button=$3 note=$4 csrf expected expected_episode return_episode
  curl -fsS -b "$work/$person.cookies" "$status_url$page" > "$work/$person.alert.html"
  grep -Fq "$button" "$work/$person.alert.html"
  if [[ "${smoke_agent_reports:-false}" == true ]]; then
    smoke_failed_run_banner "$work/$person.alert.html"
  fi
  csrf=$(sed -n 's/.*id="alert-ack".*name="csrf" value="\([^"]*\)".*/\1/p' "$work/$person.alert.html")
  expected=$(sed -n 's/.*name="expected_ack" value="\([^"]*\)".*/\1/p' "$work/$person.alert.html")
  expected_episode=$(sed -n 's/.*id="alert-ack".*name="expected_episode" value="\([^"]*\)".*/\1/p' "$work/$person.alert.html")
  return_episode=$(sed -n 's/.*id="alert-ack".*name="return_episode" value="\([^"]*\)".*/\1/p' "$work/$person.alert.html")
  test -n "$csrf"
  test -n "$expected"
  if [[ "$page" == */episodes/* ]]; then
    test "$expected_episode" = "${page##*/}"
    test "$return_episode" = "$expected_episode"
  fi
  test "$(curl -fsS -b "$work/$person.cookies" -o /dev/null -D "$work/$person.headers" -w '%{http_code}' \
    --data-urlencode "csrf=$csrf" --data-urlencode "expected_ack=$expected" --data-urlencode "note=$note" \
    --data-urlencode "expected_episode=$expected_episode" --data-urlencode "return_episode=$return_episode" \
    "$status_url${page%%/episodes/*}/ack")" = 303
  test "$(awk 'tolower($1) == "location:" {sub(/\r$/, "", $2); print $2}' "$work/$person.headers")" = "$page?done=ack"
}

smoke_failed_run_banner() {
  local banner
  banner=$(sed -n '/<a class="[^"]*bc-alert-agent-run"/,/<\/a>/p' "$1")
  grep -Fq 'A hook accepted the alert.' "$1" &&
    grep -Fq 'Agent program for the alert trigger (smoke-oncall)' <<< "$banner" &&
    grep -Fq '>Failed<' <<< "$banner" &&
    grep -Fq '>exit 79<' <<< "$banner"
}

smoke_failed_wake() {
  hook_firing || return 1
  local delivery
  delivery=$(jq -er '.delivery' "$work/hook.json") || return 1
  grep -Fq "delivery=$delivery event=firing alert=$alert exit=79" "$work/oncall.log" || return 1
  curl -fsS "$status_url/api/v1/alerts/$alert/timeline" -H "Authorization: Bearer $token" |
    jq -er --arg delivery "$delivery" '
      [.events[] | select(.kind == "agent" and .event == "firing" and .id == $delivery and .result == "ok") | .at][0] // empty' \
      > "$work/wake-accepted-at"
}

smoke_failed_run_report() {
  local delivery
  delivery=$(jq -er '.delivery' "$work/hook.json") || return 1
  curl -fsS "$status_url/api/v1/alerts/$alert/timeline" -H "Authorization: Bearer $token" |
    jq -e --arg delivery "$delivery" '
      any(.events[]; .kind == "agent" and .event == "firing" and .id == $delivery and .result == "ok") and
      any(.events[]; .kind == "agent_run" and .event == "firing" and .id == $delivery and
        .by == "smoke-oncall" and .via == "api" and .result == "failed" and .exit == 79)' >/dev/null
}

smoke_phone_acceptance() {
  [[ -s "$work/phone-$alert.json" ]] &&
    curl -fsS "$status_url/api/v1/alerts/$alert/timeline" -H "Authorization: Bearer $token" |
    jq -er '[.events[] | select(.kind == "notification" and .via == "bark" and .result == "ok") | .at][0] // empty' \
      > "$work/phone-accepted-at"
}

smoke_service_down() {
  curl -fsS --max-time 5 "${smoke_vmalert_url:-http://127.0.0.1:8880}/api/v1/rules" |
    jq -e 'any(.data.groups[].rules[] | select(.name == "ServiceDown") | .alerts[]?;
      .state == "firing" and .labels.service == "drill" and .labels.severity == "critical")' >/dev/null
}

smoke_now() {
  python3 -c 'import time; print(time.time())'
}

smoke_failed_evidence() {
  curl -fsS "$status_url/api/v1/alerts/$alert/evidence" -H "Authorization: Bearer $token" |
    jq -e '.probes.results | length == 2 and all(.[]; .ok == false and .reason == "http_status" and .http_status == 503) and
      (map(.location) | unique | sort == ["edge-1", "edge-2"])' >/dev/null
}

smoke_failed_agent_drill() {
  local previous_alert=${1:-} previous='' previous_link='' alert drill_start page episode entry first second first_note second_note csrf location
  local return_episode expected_episode
  local fault_requested fault_confirmed probes_observed rule_observed group_observed
  drill_start=$SECONDS
  first=smoke-first-shift
  second=smoke-next-shift
  first_note='The external Agent failed to run. I checked both failed locations.'
  second_note='I took over and am confirming the HTTP recovery.'
  if [[ -n "$previous_alert" ]]; then
    curl -fsS "$status_url/api/v1/alerts/$previous_alert" -H "Authorization: Bearer $token" > "$work/first-episode.json"
    previous=$(jq -er '.starts_at' "$work/first-episode.json")
    previous_link=$(jq -er '.page' "$work/first-episode.json")
  fi
  rm -f "$work/hook.json"
  touch "$work/fail-agent"
  fault_requested=$(smoke_now)
  beacon drill fail 15m
  test "$(curl -sS -o /dev/null -w '%{http_code}' "$status_url/drill/healthz")" = 503
  fault_confirmed=$(smoke_now)
  wait_for 'Both probes measured the second HTTP fault' 120 probes 0
  probes_observed=$(smoke_now)
  wait_for 'vmalert reports the drill ServiceDown rule firing' "$((drill_start + 300 - SECONDS))" smoke_service_down
  rule_observed=$(smoke_now)
  wait_for 'The rules opened the failed-Agent drill alert' "$((drill_start + 300 - SECONDS))" firing > "$work/failed-agent-alert"
  group_observed=$(smoke_now)
  alert=$(head -1 "$work/failed-agent-alert")
  if [[ -n "$previous_alert" ]]; then
    test "$alert" = "$previous_alert"
  fi
  page="/console/alerts/$alert"
  wait_for 'The harness accepted the signed wake and its program exited 79' 30 smoke_failed_wake
  if [[ "${smoke_agent_reports:-false}" == true ]]; then
    wait_for 'Beacon recorded the accepted wake and actual exit 79 for the same delivery' 30 smoke_failed_run_report
  fi
  curl -fsS "$status_url/api/v1/alerts/$alert/timeline" -H "Authorization: Bearer $token" |
    jq -e 'all(.events[]; .kind != "note" and .kind != "acknowledged")' >/dev/null
  wait_for 'The alert evidence shows HTTP 503 at both locations' 30 smoke_failed_evidence
  wait_for 'The local receiver and Beacon recorded scheduled Bark acceptance' 60 smoke_phone_acceptance
  curl -fsS "$status_url/api/v1/alerts/$alert" -H "Authorization: Bearer $token" > "$work/second-episode.json"
  jq -e --arg previous "$previous" '.status == "firing" and .ack == null and .starts_at != $previous' "$work/second-episode.json" >/dev/null
  episode=$(jq -er '.starts_at' "$work/second-episode.json")
  entry=$(jq -er '.payload.url' "$work/phone-$alert.json")
  python3 - "$entry" "$status_url" "$page" "$work/phone-$alert.json" "$work/hook.json" \
    "$fault_requested" "$fault_confirmed" "$probes_observed" "$rule_observed" "$group_observed" \
    "$work/wake-accepted-at" "$work/phone-accepted-at" "$version" "$work/received-incident" <<'PY'
from datetime import datetime
import json
from pathlib import Path
import re
import sys
from urllib.parse import parse_qs, urlsplit

entry, base, alert, receipt, wake = sys.argv[1:6]
requested, confirmed, probes, rule, group = map(float, sys.argv[6:11])
wake_accepted_at, phone_accepted_at = map(Path, sys.argv[11:13])
version, incident_path = sys.argv[13], Path(sys.argv[14])
entry, base = urlsplit(entry), urlsplit(base)
if (entry.scheme, entry.netloc, entry.path) != (base.scheme, base.netloc, "/console/sign-in"):
    sys.exit("The recorded phone notification links outside the test sign-in")
paths = parse_qs(entry.query).get("next", [])
if version == "v0.3.0-rc.34":
    valid = paths == [alert]
else:
    valid = len(paths) == 1 and bool(re.fullmatch(re.escape(alert) + r"/episodes/[0-9a-f]{16}", paths[0]))
if not valid:
    sys.exit("The recorded phone notification must link to the drill alert")
incident_path.write_text(paths[0], encoding="utf-8")
received = json.loads(Path(receipt).read_text())["received_at"]
wake_queued = json.loads(Path(wake).read_text())["at"]


def epoch(value):
    return datetime.fromisoformat(value.strip().replace("Z", "+00:00")).timestamp()


wake_accepted = epoch(wake_accepted_at.read_text())
phone_accepted = epoch(phone_accepted_at.read_text())
delay = received - epoch(wake_queued)
if not 19 <= delay <= 60:
    sys.exit(f"Expected the 20-second phone delay, observed {delay:.1f}s from wake to receiver")
if not confirmed <= wake_accepted <= received <= phone_accepted:
    sys.exit("Expected phone acceptance after the signed wake and HTTP fault")
print(f"Local phone provider accepted the critical push {delay:.1f}s after the signed wake.")
# The fault lies between the request and the first HTTP 503. Probe, rule and group checks are
# polled, so their observations can be later than the actual transition.
print("Failed-Agent phase timing (seconds from fault request): " + json.dumps({
    "http_503_confirmed": round(confirmed - requested, 3),
    "both_probes_observed": round(probes - requested, 3),
    "rule_firing_observed": round(rule - requested, 3),
    "beacon_group_observed": round(group - requested, 3),
    "signed_wake_accepted": round(wake_accepted - requested, 3),
    "local_phone_request_received": round(received - requested, 3),
    "local_phone_accepted": round(phone_accepted - requested, 3),
    "phone_after_signed_wake": round(phone_accepted - wake_accepted, 3),
    "within_five_minutes": phone_accepted - requested <= 300,
}))
PY
  page=$(cat "$work/received-incident")
  smoke_shift_sign_in "$first" "$entry" "$page"
  smoke_shift_claim "$first" "$page" 'Acknowledge with note' "$first_note"
  if [[ "$version" != v0.3.0-rc.39 ]]; then
    local latest_note='The first shift isolated the relay and left the recovery check to the incoming operator.'
    csrf=$(sed -n 's/.*id="alert-ack".*name="csrf" value="\([^"]*\)".*/\1/p' "$work/$first.alert.html")
    test -n "$csrf"
    test "$(curl -fsS -b "$work/$first.cookies" -o /dev/null -w '%{http_code}' --data-urlencode "csrf=$csrf" \
      --data-urlencode "expected_episode=${page##*/}" --data-urlencode "return_episode=${page##*/}" \
      --data-urlencode "text=$latest_note" "$status_url${page%%/episodes/*}/notes")" = 303
  fi
  smoke_shift_sign_in "$second" "$entry" "$page"
  curl -fsS -b "$work/$second.cookies" "$status_url$page" > "$work/previous-shift.html"
  grep -Fq "$first_note" "$work/previous-shift.html"
  if [[ "$version" != v0.3.0-rc.39 ]]; then
    awk '/aria-labelledby="alert-latest-note"/ { preview = 1 }
      preview { print }
      preview && /<p class="ol-break">/ { exit }' "$work/previous-shift.html" > "$work/latest-note.html"
    grep -Fq "$first" "$work/latest-note.html"
    grep -Fq "$latest_note" "$work/latest-note.html"
    printf 'The next shift saw the recent note in the response panel before taking over.\n'
  fi
  smoke_shift_claim "$second" "$page" 'Take over with note' "$second_note"
  printf 'Both on-call accounts claimed the same episode through the received link.\n'
  if [[ "$version" != v0.3.0-rc.34 && -n "$previous_alert" ]]; then
    local old_outcome
    [[ "$previous_link" == "$status_url/console/alerts/$previous_alert/episodes/"* ]]
    old_outcome=$(curl -fsS "$status_url/api/v1/incident-outcomes?group=$alert" -H "Authorization: Bearer $token" |
      jq -er --arg previous "$previous" '.outcomes[] | select(.started_at == $previous) | .id')
    test "$(curl -fsS -b "$work/$second.cookies" -o /dev/null -D "$work/old-incident.headers" -w '%{http_code}' "$previous_link")" = 303
    test "$(awk 'tolower($1) == "location:" {sub(/\r$/, ""); print $2}' "$work/old-incident.headers")" = "/console/incident-outcomes/$old_outcome"
    printf 'The first episode link opens its exact retained history after the same service fires again.\n'
    if [[ "$version" != v0.3.0-rc.39 ]]; then
      local old_episode request
      old_episode=$(jq -er '.episode' "$work/first-episode.json")
      request=$(jq -nc --arg id "$alert" --arg episode "$old_episode" '{alert_id:$id,expected_episode:$episode,wait:1}')
      test "$(curl -sS -H "Authorization: Bearer $token" -H 'Content-Type: application/json' -o "$work/stale-recheck.json" \
        -w '%{http_code}' --data "$request" "$status_url/api/v1/probe-now")" = 409
      jq -e '.code == "incident.changed"' "$work/stale-recheck.json" >/dev/null
      rpc "$(jq -nc --argjson input "$request" '{jsonrpc:"2.0",id:29,method:"tools/call",params:{name:"probe_now",arguments:$input}}')" |
        jq -e '.result.isError and any(.result.content[]; .text | contains("incident.changed"))' >/dev/null
      printf 'REST and MCP rejected the old episode before probing the replacement incident.\n'
    fi
  fi

  beacon drill off
  test "$(curl -fsS -o /dev/null -w '%{http_code}' "$status_url/drill/healthz")" = 200
  curl -fsS -b "$work/$second.cookies" "$status_url$page" > "$work/recheck.html"
  csrf=$(sed -n 's/.*action="[^"]*\/probe-now".*name="csrf" value="\([^"]*\)".*/\1/p' "$work/recheck.html")
  expected_episode=$(sed -n 's/.*action="[^"]*\/probe-now".*name="expected_episode" value="\([^"]*\)".*/\1/p' "$work/recheck.html")
  return_episode=$(sed -n 's/.*action="[^"]*\/probe-now".*name="return_episode" value="\([^"]*\)".*/\1/p' "$work/recheck.html")
  test -n "$csrf"
  test "$(curl -fsS --max-time 60 -b "$work/$second.cookies" -o /dev/null -D "$work/recheck.headers" -w '%{http_code}' \
    --data-urlencode "csrf=$csrf" --data-urlencode "expected_episode=$expected_episode" \
    --data-urlencode "return_episode=$return_episode" "$status_url${page%%/episodes/*}/probe-now")" = 303
  location=$(awk 'tolower($1) == "location:" {sub(/\r$/, "", $2); print $2}' "$work/recheck.headers")
  [[ "$location" == "$page?probe="* ]]
  wait_for 'Both probes measured HTTP recovery after handover' 120 probes 1
  wait_for 'The handed-off alert resolved' 150 resolved
  if [[ "$version" != v0.3.0-rc.44 ]]; then
    curl -fsS -b "$work/$second.cookies" "$status_url$page" > "$work/latest-incident-check.html"
    awk '/aria-labelledby="alert-latest-check"/ { remaining = 8 } remaining > 0 { print; remaining-- }' "$work/latest-incident-check.html" > "$work/latest-incident-check-panel.html"
    grep -Fq 'Latest on-demand check' "$work/latest-incident-check-panel.html"
    grep -Fq "Requested by $second" "$work/latest-incident-check-panel.html"
    grep -Fq "href=\"$location\"" "$work/latest-incident-check-panel.html"
    grep -Fq '2 passed, 0 failed, 0 missing' "$work/latest-incident-check-panel.html"
    printf 'The incident response panel showed the latest on-demand check and its requesting operator.\n'
  fi
  if [[ "${smoke_agent_reports:-false}" == true ]]; then
    smoke_failed_run_report
    local delivery
    delivery=$(jq -er '.delivery' "$work/hook.json")
    curl -fsS -b "$work/$second.cookies" "$status_url$page" > "$work/recovered-agent-report.html"
    awk -v anchor="agent-run-$delivery-failed" '
      /<tr[ >]/ { agent_run = /<tr class="kind-agent_run"[ >]/ && index($0, "id=\"" anchor "\"") }
      agent_run && /Failed \(exit 79\)/ { found = 1 }
      /<\/tr>/ && !/<tr[ >]/ { agent_run = 0 }
      END { exit !found }
    ' "$work/recovered-agent-report.html"
    printf 'The recovered incident shows exit 79 in the original delivery row.\n'
  fi
  curl -fsS "$status_url/api/v1/alerts/$alert/timeline" -H "Authorization: Bearer $token" |
    jq -e --arg first "$first" --arg second "$second" --arg a "$first_note" --arg b "$second_note" '
      [.events[] | select(.kind == "acknowledged" and .type == "person" and .via == "console") | [.by, .text]] ==
        [[$first, $a], [$second, $b]] and
      any(.events[]; .kind == "probe_now" and .by == $second and
        (.probes | length == 2 and all(.[]; .status == "ok") and (map(.location) | unique | sort == ["edge-1", "edge-2"]))) and
      any(.events[]; .kind == "resolved")' >/dev/null
  curl -fsS "$status_url/api/v1/incident-outcomes?group=$alert" -H "Authorization: Bearer $token" > "$work/handover-history.json"
  jq -e --arg episode "$episode" --arg second "$second" '
      any(.outcomes[]; .started_at == $episode and
        any(.participation[]; .kind == "person" and .by == $second and .via == "console") and
        (.verification | .by == $second and .passed == 2 and .failed == 0 and .missing == 0))' "$work/handover-history.json" >/dev/null
  if [[ "$version" != v0.3.0-rc.35 ]]; then
    jq -e --arg episode "$episode" --arg second "$second" --arg probe "${location##*#probe-}" '
      any(.outcomes[]; .started_at == $episode and (.passing_check |
        .id == $probe and .actor_kind == "person" and .by == $second and .via == "console" and
        .passed == 2 and .failed == 0 and .missing == 0))' "$work/handover-history.json" >/dev/null
    printf 'The resolved incident retains the same complete passing check and its signed-in operator.\n'
  fi
  if [[ "$version" != v0.3.0-rc.39 && "${smoke_agent_reports:-false}" == true ]]; then
    local saved_outcome
    saved_outcome=$(jq -er --arg episode "$episode" --arg delivery "$delivery" '
      .outcomes[] | select(.started_at == $episode and any(.agent_deliveries[];
        .id == $delivery and .event == "firing" and .status == "ok" and .started_at != null and
        .run.status == "failed" and .run.exit_code == 79)) | .id' "$work/handover-history.json")
    curl -fsS -b "$work/$second.cookies" "$status_url/console/incident-outcomes/$saved_outcome" > "$work/agent-outcome.html"
    grep -Fq 'Accepted by hook' "$work/agent-outcome.html"
    grep -Fq 'exit 79' "$work/agent-outcome.html"
    printf 'The retained outcome separates hook acceptance and exit 79 for the same firing delivery.\n'
    if [[ "$version" != v0.3.0-rc.40 && "$version" != v0.3.0-rc.41 ]]; then
      curl -fsS "$status_url/api/v1/incident-outcomes?group=$alert&severity=critical&since=7d&evidence=passing_check" \
        -H "Authorization: Bearer $token" > "$work/filtered-history.json"
      jq -e --arg outcome "$saved_outcome" --arg second "$second" '
        any(.outcomes[]; .id == $outcome and
          (.human_acknowledgement | .by == $second and .via == "console" and .at != null) and
          (.passing_check | .passed == 2 and .failed == 0 and .missing == 0))' "$work/filtered-history.json" >/dev/null
      curl -fsS -b "$work/$second.cookies" \
        "$status_url/console/incident-outcomes?group=$alert&severity=critical&since=7d&evidence=human_ack" > "$work/filtered-history.html"
      grep -Fq "href=\"/console/incident-outcomes/$saved_outcome\"" "$work/filtered-history.html"
      printf 'The retained incident was reachable through evidence filters with its named acknowledgement.\n'
    fi
  fi
  printf 'The failed-Agent drill preserved human handover and verified recovery in history (%ss).\n' "$((SECONDS - drill_start))"
}
