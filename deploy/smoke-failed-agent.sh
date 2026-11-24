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
            match = re.fullmatch(r"/console/alerts/([0-9a-f]{12})", targets[0]) if len(targets) == 1 else None
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
        self.end_headers()

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
  local person=$1 page=$2 button=$3 note=$4 csrf expected
  curl -fsS -b "$work/$person.cookies" "$status_url$page" > "$work/$person.alert.html"
  grep -Fq "$button" "$work/$person.alert.html"
  if [[ "${smoke_agent_reports:-false}" == true ]]; then
    smoke_failed_run_banner "$work/$person.alert.html"
  fi
  csrf=$(sed -n 's/.*id="alert-ack".*name="csrf" value="\([^"]*\)".*/\1/p' "$work/$person.alert.html")
  expected=$(sed -n 's/.*name="expected_ack" value="\([^"]*\)".*/\1/p' "$work/$person.alert.html")
  test -n "$csrf"
  test -n "$expected"
  test "$(curl -fsS -b "$work/$person.cookies" -o /dev/null -D "$work/$person.headers" -w '%{http_code}' \
    --data-urlencode "csrf=$csrf" --data-urlencode "expected_ack=$expected" --data-urlencode "note=$note" \
    "$status_url$page/ack")" = 303
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
    jq -e --arg delivery "$delivery" '
      any(.events[]; .kind == "agent" and .event == "firing" and .id == $delivery and .result == "ok")' >/dev/null
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
    jq -e 'any(.events[]; .kind == "notification" and .via == "bark" and .result == "ok")' >/dev/null
}

smoke_failed_evidence() {
  curl -fsS "$status_url/api/v1/alerts/$alert/evidence" -H "Authorization: Bearer $token" |
    jq -e '.probes.results | length == 2 and all(.[]; .ok == false and .reason == "http_status" and .http_status == 503) and
      (map(.location) | unique | sort == ["edge-1", "edge-2"])' >/dev/null
}

smoke_failed_agent_drill() {
  local previous_alert=${1:-} previous= alert drill_start page episode entry first second first_note second_note csrf location
  drill_start=$SECONDS
  first=smoke-first-shift
  second=smoke-next-shift
  first_note='The external Agent failed to run. I checked both failed locations.'
  second_note='I took over and am confirming the HTTP recovery.'
  if [[ -n "$previous_alert" ]]; then
    previous=$(curl -fsS "$status_url/api/v1/alerts/$previous_alert" -H "Authorization: Bearer $token" | jq -er '.starts_at')
  fi
  rm -f "$work/hook.json"
  touch "$work/fail-agent"
  beacon drill fail 15m
  test "$(curl -sS -o /dev/null -w '%{http_code}' "$status_url/drill/healthz")" = 503
  wait_for 'Both probes measured the second HTTP fault' 120 probes 0
  wait_for 'The rules opened the failed-Agent drill alert' "$((drill_start + 300 - SECONDS))" firing > "$work/failed-agent-alert"
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
  python3 - "$entry" "$status_url" "$page" "$work/phone-$alert.json" "$work/hook.json" <<'PY'
from datetime import datetime
import json
from pathlib import Path
import sys
from urllib.parse import parse_qs, urlsplit

entry, base, alert, receipt, wake = sys.argv[1:]
entry, base = urlsplit(entry), urlsplit(base)
if (entry.scheme, entry.netloc, entry.path) != (base.scheme, base.netloc, "/console/sign-in"):
    sys.exit("The recorded phone notification links outside the test sign-in")
if parse_qs(entry.query).get("next") != [alert]:
    sys.exit("The recorded phone notification must link to the drill alert")
received = json.loads(Path(receipt).read_text())["received_at"]
sent = datetime.fromisoformat(json.loads(Path(wake).read_text())["at"].replace("Z", "+00:00")).timestamp()
delay = received - sent
if not 19 <= delay <= 60:
    sys.exit(f"Expected the 20-second phone delay, observed {delay:.1f}s from wake to receiver")
print(f"Local phone provider accepted the critical push {delay:.1f}s after the signed wake.")
PY
  smoke_shift_sign_in "$first" "$entry" "$page"
  smoke_shift_claim "$first" "$page" 'Acknowledge with note' "$first_note"
  smoke_shift_sign_in "$second" "$entry" "$page"
  curl -fsS -b "$work/$second.cookies" "$status_url$page" > "$work/previous-shift.html"
  grep -Fq "$first_note" "$work/previous-shift.html"
  smoke_shift_claim "$second" "$page" 'Take over with note' "$second_note"
  printf 'Both on-call accounts claimed the same episode through the received link.\n'

  beacon drill off
  test "$(curl -fsS -o /dev/null -w '%{http_code}' "$status_url/drill/healthz")" = 200
  curl -fsS -b "$work/$second.cookies" "$status_url$page" > "$work/recheck.html"
  csrf=$(sed -n 's/.*action="[^"]*\/probe-now".*name="csrf" value="\([^"]*\)".*/\1/p' "$work/recheck.html")
  test -n "$csrf"
  test "$(curl -fsS --max-time 60 -b "$work/$second.cookies" -o /dev/null -D "$work/recheck.headers" -w '%{http_code}' \
    --data-urlencode "csrf=$csrf" "$status_url$page/probe-now")" = 303
  location=$(awk 'tolower($1) == "location:" {sub(/\r$/, "", $2); print $2}' "$work/recheck.headers")
  [[ "$location" == "$page?probe="* ]]
  wait_for 'Both probes measured HTTP recovery after handover' 120 probes 1
  wait_for 'The handed-off alert resolved' 150 resolved
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
  curl -fsS "$status_url/api/v1/incident-outcomes?group=$alert" -H "Authorization: Bearer $token" |
    jq -e --arg episode "$episode" --arg second "$second" '
      any(.outcomes[]; .started_at == $episode and
        any(.participation[]; .kind == "person" and .by == $second and .via == "console") and
        (.verification | .by == $second and .passed == 2 and .failed == 0 and .missing == 0))' >/dev/null
  printf 'The failed-Agent drill preserved human handover and verified recovery in history (%ss).\n' "$((SECONDS - drill_start))"
}
