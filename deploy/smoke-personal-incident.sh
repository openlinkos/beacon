#!/usr/bin/env bash
# Sourced by smoke.sh on the disposable reference runner, after its failed-Agent drill.
# shellcheck disable=SC2154 # smoke.sh supplies the URLs, its work directory and read token.

smoke_personal_html() {
  python3 - "$@" <<'PY'
from html.parser import HTMLParser
import json
from pathlib import Path
import sys
from urllib.parse import parse_qs, urlsplit


class Page(HTMLParser):
    def __init__(self, filename):
        super().__init__()
        self.links = set()
        self.forms = {}
        self.codes = []
        self.form = None
        self.code = None
        self.feed(Path(filename).read_text(encoding="utf-8"))

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if tag == "a" and "href" in a:
            self.links.add(a["href"])
        if tag == "form":
            self.form = a.get("action")
        if tag == "input" and self.form and a.get("type") == "hidden" and "name" in a:
            self.forms.setdefault(self.form, {})[a["name"]] = a.get("value", "")
        if tag == "code":
            self.code = []

    def handle_data(self, text):
        if self.code is not None:
            self.code.append(text)

    def handle_endtag(self, tag):
        if tag == "form":
            self.form = None
        if tag == "code" and self.code is not None:
            self.codes.append("".join(self.code))
            self.code = None


mode, source, *args = sys.argv[1:]
page = Page(source)
if mode == "link":
    path, anchor, incident, *machine = args
    expected = {"incident": [incident]} if incident else {}
    if machine:
        expected["ssh_machine"] = machine
    links = []
    for href in page.links:
        u = urlsplit(href)
        if u.scheme or u.netloc or u.path != path or u.fragment != anchor:
            continue
        query = parse_qs(u.query, strict_parsing=True)
        registration = query.pop("registration", [])
        if query == expected and (len(registration) == 1 if incident else not registration):
            links.append(href)
    if len(links) != 1:
        sys.exit(f"Expected exactly one link to {path}#{anchor}; got {len(links)}")
    print(links[0])
elif mode == "fields":
    action, = args
    if action not in page.forms:
        sys.exit(f"Missing form for {action}")
    print(json.dumps(page.forms[action]))
elif mode == "ssh":
    host, port, pinned_key = args
    config = [line.strip() for line in f"""# ~/.ssh/beacon-personal.conf
Host beacon-target
  HostName {host}
  Port {port}
  User target
  HostKeyAlias beacon
  IdentityFile ~/.ssh/beacon-personal
  IdentitiesOnly yes
  ControlPath none

# ~/.ssh/known_hosts
beacon {pinned_key}""".splitlines()]
    commands = "ssh -F ~/.ssh/beacon-personal.conf beacon-target"
    if commands not in page.codes or config not in [[line.strip() for line in code.splitlines()] for code in page.codes]:
        sys.exit("The personal SSH command or configuration did not match the enrolled fixture")
PY
}

smoke_personal_group() {
  curl -fsS --max-time 8 "$status_url/api/v1/alerts?state=all" -H "Authorization: Bearer $token" |
    jq -er --arg state "$1" '
      first(.alerts[] | select(.labels.alertname == "ReferencePersonalSSH" and
        .service == "reference-personal-ssh" and .severity == "info" and .status == $state and
        .channel == "none" and ([.agents[]?] | length == 0))) | [.id, .episode] | @tsv'
}

smoke_personal_session() {
  curl -fsS --max-time 8 "$status_url/api/v1/sessions?machine=target&agent=$1" -H "Authorization: Bearer $token" |
    jq -er --arg person "$1" 'first(.sessions[] | select(.agent == $person and
      .principal == ("user:" + $person) and .machine == "target" and .via == "ssh" and
      .kind == "exec" and .command == "id -un" and .exit == 0 and .recording_state == "complete")) | .id'
}

smoke_personal_incident() (
  local port=${1:-17002} person=smoke-personal-duty publisher=smoke-personal-publisher
  local send_token fixture group episode incident page entry machine account back fields csrf registration key_added pinned remote_user session replay
  local personal_dir="$work/personal-incident" fixture_open=true
  mkdir -m 700 "$personal_dir"
  send_token=$(beacon token create "$publisher" -scopes alerts:send -expires 1h)
  printf '::add-mask::%s\n' "$send_token"
  fixture=$(jq -nc '[{labels:{alertname:"ReferencePersonalSSH",service:"reference-personal-ssh",severity:"info",machine:"target"},
    annotations:{summary:"Reference personal SSH exercise"}, startsAt:(now | todate), endsAt:(now+240 | todate)}]')
  post_fixture() {
    curl -fsS --max-time 8 -o /dev/null -H "Authorization: Bearer $send_token" -H 'Content-Type: application/json' \
      --data "$1" "$metrics_url/api/v2/alerts"
  }
  resolve_fixture() {
    post_fixture "$(jq -c '.[0].endsAt=(now-1 | todate)' <<< "$fixture")"
  }
  # shellcheck disable=SC2329 # called by the EXIT trap in the test subshell
  close_fixture() {
    if [[ "$fixture_open" == true ]]; then
      resolve_fixture || true
    fi
    beacon token revoke "$publisher" >/dev/null || true
    rm -f "$personal_dir/key"
  }
  trap close_fixture EXIT
  post_fixture "$fixture"
  wait_for 'The isolated info incident is firing' 50 smoke_personal_group firing > "$personal_dir/group"
  IFS=$'\t' read -r group episode < "$personal_dir/group"
  [[ "$group" =~ ^[0-9a-f]{12}$ && "$episode" =~ ^[0-9a-f]{16}$ ]]
  incident="$group.$episode"
  page="/console/alerts/$group/episodes/$episode"
  entry="$status_url/console/sign-in?next=$(jq -nr --arg page "$page" '$page | @uri')"

  smoke_shift_sign_in "$person" "$entry" "$page"
  get_page() {
    curl -fsS --max-time 10 -b "$work/$person.cookies" "$status_url${1%%#*}"
  }
  get_page "$page" > "$personal_dir/incident.html"
  machine=$(smoke_personal_html link "$personal_dir/incident.html" /console/machines/target ssh-connect "$incident")
  get_page "$machine" > "$personal_dir/no-key.html"
  smoke_personal_html link "$personal_dir/no-key.html" "$page" '' '' >/dev/null
  account=$(smoke_personal_html link "$personal_dir/no-key.html" /console/account ssh-keys "$incident" target)
  get_page "$account" > "$personal_dir/account.html"
  smoke_personal_html link "$personal_dir/account.html" /console/machines/target ssh-connect "$incident" |
    grep -Fx "$machine" >/dev/null
  fields=$(smoke_personal_html fields "$personal_dir/account.html" /console/account/keys)
  csrf=$(jq -er --arg incident "$incident" \
    'select(.incident == $incident and .ssh_machine == "target") | .csrf | select(length > 0)' <<< "$fields")
  registration=$(jq -er '.registration | select(length > 0)' <<< "$fields")
  ssh-keygen -q -t ed25519 -N '' -f "$personal_dir/key"
  test "$(curl -fsS --max-time 10 -b "$work/$person.cookies" -o /dev/null -D "$personal_dir/post.headers" \
    -w '%{http_code}' --data-urlencode "csrf=$csrf" --data-urlencode "incident=$incident" \
    --data-urlencode ssh_machine=target --data-urlencode "registration=$registration" \
    --data-urlencode "key@$personal_dir/key.pub" "$status_url/console/account/keys")" = 303
  key_added=$(awk 'tolower($1) == "location:" {sub(/\r$/, "", $2); print $2}' "$personal_dir/post.headers")
  test "$key_added" = "/console/account?done=key_added&${account#*\?}"
  get_page "$key_added" > "$personal_dir/key-added.html"
  smoke_personal_html link "$personal_dir/key-added.html" /console/machines/target ssh-connect "$incident" |
    grep -Fx "$machine" >/dev/null

  get_page "$machine" > "$personal_dir/machine.html"
  back=$(smoke_personal_html link "$personal_dir/machine.html" "$page" '' '')
  pinned=$(sudo ssh-keygen -y -f secrets/ssh-host-key | awk '{print $1, $2}')
  test -n "$pinned"
  smoke_personal_html ssh "$personal_dir/machine.html" "$status_host" "$port" "$pinned"
  printf 'beacon %s\n' "$pinned" > "$personal_dir/known_hosts"
  cat > "$personal_dir/ssh-config" <<CONFIG
Host beacon-target
  HostName $status_host
  Port $port
  User target
  HostKeyAlias beacon
  IdentityFile "$personal_dir/key"
  IdentitiesOnly yes
  ControlPath none
  UserKnownHostsFile "$personal_dir/known_hosts"
  GlobalKnownHostsFile /dev/null
  StrictHostKeyChecking yes
  BatchMode yes
  ConnectTimeout 5
CONFIG
  remote_user=$(timeout 25s ssh -F "$personal_dir/ssh-config" -nT beacon-target id -un)
  test "$remote_user" = beacon

  wait_for 'The direct SSH session was recorded under the on-call account' 18 smoke_personal_session "$person" > "$personal_dir/session"
  session=$(head -1 "$personal_dir/session")
  [[ "$session" =~ ^[0-9]{8}-[0-9]{6}-[0-9a-f]{6}-[0-9]+$ ]]
  get_page "$back" > "$personal_dir/returned.html"
  replay=$(smoke_personal_html link "$personal_dir/returned.html" "/console/sessions/$session" '' '')
  get_page "$replay" > "$personal_dir/replay.html"
  grep -Fq "<dd>$person</dd>" "$personal_dir/replay.html"
  grep -Fq 'id="player"' "$personal_dir/replay.html"

  resolve_fixture
  fixture_open=false
  wait_for 'The isolated info incident resolved' 15 smoke_personal_group resolved > "$personal_dir/resolved"
  test "$(head -1 "$personal_dir/group")" = "$(head -1 "$personal_dir/resolved")"
  printf 'Personal SSH first-use passed: the on-call account returned to the same incident and replayed its own command.\n'
)
