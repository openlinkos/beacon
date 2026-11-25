#!/usr/bin/env bash
# Sourced by smoke.sh. Both machine records lead to the same disposable SSH server.

smoke_session_access() {
  local allowed=$1 addr=$2 other writer run body suffix path="$status_url/api/v1/sessions"
  local missing=20000101-000000-abcdef-1 expected sign_in
  beacon machine add second-target "$addr" >/dev/null
  writer=$(beacon token create smoke-second-machine -scopes machines:ssh -machines second-target -expires 1h)
  printf '::add-mask::%s\n' "$writer"
  run=$(token=$writer rpc '{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"run_command","arguments":{"machine":"second-target","command":"id -un"}}}')
  other=$(jq -er '.result.structuredContent | select(.exit_code == 0 and .stdout == "beacon\n") | .session' <<< "$run")
  test "$other" != "$allowed"
  expected="/console/sessions/$allowed"
  sign_in="$status_url/console/sign-in?next=$(jq -nr --arg page "$expected" '$page | @uri')"

  # The newer recording must be discarded before applying limit=1.
  curl -fsS --max-time 15 "$path?limit=1" -H "Authorization: Bearer $token" |
    jq -e --arg id "$allowed" --arg page "$sign_in" '.sessions | map([.id, .machine, .page]) == [[$id, "target", $page]]' >/dev/null
  rpc '{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"list_sessions","arguments":{"limit":1}}}' |
    jq -e --arg id "$allowed" --arg page "$sign_in" '.result.structuredContent.sessions | map([.id, .machine, .page]) == [[$id, "target", $page]]' >/dev/null
  curl -fsS --max-time 15 "$path/$allowed" -H "Authorization: Bearer $token" |
    jq -e --arg page "$sign_in" '.machine == "target" and .stdout == "beacon\n" and .page == $page' >/dev/null

  # Compare errors after substituting the requested ID in the stable response text.
  for suffix in '' /recording; do
    test "$(curl -sS --max-time 15 "$path/$missing$suffix" -H "Authorization: Bearer $token" -o "$work/absent-session.json" -w '%{http_code}')" = 404
    test "$(curl -sS --max-time 15 "$path/$other$suffix" -H "Authorization: Bearer $token" -o "$work/outside-session.json" -w '%{http_code}')" = 404
    jq -e '.code == "session.not_found"' "$work/outside-session.json" >/dev/null
    sed "s/$other/$missing/g" "$work/outside-session.json" | cmp "$work/absent-session.json" -
  done
  body=$(jq -nc --arg id "$other" '{jsonrpc:"2.0",id:10,method:"tools/call",params:{name:"get_session",arguments:{id:$id}}}')
  rpc "$body" | jq -e '.result.isError and (.result.structuredContent == null)' >/dev/null
  smoke_share_status 404 "$status_url$expected"
  smoke_share_status 200 "$status_url$expected/recording" -b "$work/cookies"
  smoke_share_status 200 "$status_url/console/sessions/$other" -b "$work/cookies"
  smoke_share_status 200 "$status_url/console/sessions/$other/recording" -b "$work/cookies"
  beacon token revoke smoke-second-machine >/dev/null
  printf 'Scoped REST and MCP session reads obeyed the machine limit; the signed-in owner could still replay both recordings.\n'
}

# These file moves are confined to the disposable reference installation. Park an artifact
# under a name that no Beacon reader uses, then restore it before the cold backup.
smoke_evidence_files() {
  docker run --rm --volumes-from beacon alpine sh -ec '
    root=/var/lib/beacon/recordings
    case "$1" in
      refuse)
        test -d "$root"
        test ! -e "$root.reference-parked"
        mv "$root" "$root.reference-parked"
        printf "%s\n" beacon-reference-recording-fault > "$root"
        ;;
      admit)
        if test -d "$root.reference-parked"; then
          test "$(cat "$root")" = beacon-reference-recording-fault
          rm "$root"
          mv "$root.reference-parked" "$root"
        fi
        ;;
      hide)
        test -f "$root/$2"
        test ! -e "$root/$2.reference-parked"
        mv "$root/$2" "$root/$2.reference-parked"
        ;;
      restore)
        if test -f "$root/$2.reference-parked"; then
          test ! -e "$root/$2"
          mv "$root/$2.reference-parked" "$root/$2"
        fi
        ;;
    esac
  ' sh "$1" "${2:-}"
}

smoke_session_evidence() (
  local id=$1 route name denied call
  route="$status_url/api/v1/sessions/$id"
  curl -fsS "$route" -H "Authorization: Bearer $token" > "$work/complete-session.json"
  name=$(jq -er '.recording' "$work/complete-session.json")
  [[ "$name" =~ ^[0-9]{4}/[0-9]{2}/[0-9]{2}/[0-9a-f-]+$ && "${name##*/}" == "$id" ]]
  jq -e '.recording_state == "complete" and .transcript_state == "complete" and
    .recording_available and .stdout == "beacon\n" and .exit == 0' "$work/complete-session.json" >/dev/null
  trap 'smoke_evidence_files admit; smoke_evidence_files restore "$name.cast"; smoke_evidence_files restore "$name.json"' EXIT

  smoke_evidence_files hide "$name.cast"
  curl -fsS "$route" -H "Authorization: Bearer $token" |
    jq -e '.recording_state == "complete" and .recording_available == false and
      .transcript_state == "complete" and .stdout == "beacon\n"' >/dev/null
  test "$(curl -sS -o "$work/recording-unavailable.json" -w '%{http_code}' \
    "$route/recording" -H "Authorization: Bearer $token")" = 404
  jq -e '.code == "session.recording_unavailable"' "$work/recording-unavailable.json" >/dev/null
  curl -fsS -b "$work/cookies" "$status_url/console/sessions/$id" > "$work/no-player.html"
  if grep -Fq 'id="player"' "$work/no-player.html"; then
    echo 'The console offered a replay whose recording was unavailable.' >&2
    exit 1
  fi
  smoke_evidence_files restore "$name.cast"

  smoke_evidence_files hide "$name.json"
  call=$(jq -nc --arg id "$id" '{jsonrpc:"2.0",id:44,method:"tools/call",params:{name:"get_session",arguments:{id:$id}}}')
  rpc "$call" | jq -e '.result.structuredContent | .recording_state == "complete" and .recording_available and
    .transcript_state == "unavailable" and .stdout == null' >/dev/null
  curl -fsS -b "$work/cookies" "$status_url/console/sessions/$id" > "$work/no-transcript.html"
  grep -Fq 'id="player"' "$work/no-transcript.html"
  smoke_evidence_files restore "$name.json"
  printf 'The signed release distinguishes replay availability and transcript failures in REST, MCP and the session page.\n'

  local marker=/home/beacon/.beacon-recording-fault-test
  sudo test ! -e "$marker"
  denied=$(jq -nc --arg command "printf 'observed' > '$marker'" '{command:$command}')
  smoke_evidence_files refuse
  test "$(curl -sS -o "$work/evidence-admission.json" -w '%{http_code}' \
    "$status_url/api/v1/machines/target/run" -H "Authorization: Bearer $token" \
    -H 'Content-Type: application/json' --data "$denied")" = 503
  jq -e '.code == "ssh.evidence_unavailable"' "$work/evidence-admission.json" >/dev/null
  call=$(jq -nc --argjson in "$denied" '{jsonrpc:"2.0",id:45,method:"tools/call",
    params:{name:"run_command",arguments:($in + {machine:"target"})}}')
  rpc "$call" | jq -e '.result.isError and any(.result.content[]; .text | contains("ssh.evidence_unavailable"))' >/dev/null
  sudo test ! -e "$marker"
  smoke_evidence_files admit
  rpc "$call" | jq -e '.result.structuredContent | .exit_code == 0 and
    .recording_state == "complete" and .transcript_state == "complete"' >/dev/null
  sudo grep -Fxq observed "$marker"
  sudo rm "$marker"
  printf 'The signed release refused unrecordable machine commands before execution and resumed recorded commands when storage returned.\n'
)

smoke_scoped_search() (
  local directory query request rest remote expected session
  directory=$(sudo --user=beacon mktemp -d /home/beacon/beacon-search.XXXXXX)
  trap 'sudo --user=beacon rm -f "$directory/service.log"; sudo --user=beacon rmdir "$directory"' EXIT
  query='Reference [verify]'
  printf '%s\n' 'first line' "$query found" | sudo --user=beacon tee "$directory/service.log" >/dev/null
  request=$(jq -nc --arg directory "$directory" --arg query "$query" '{directory:$directory,query:$query}')
  rest=$(curl -fsS -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
    --data "$request" "$status_url/api/v1/machines/target/search")
  expected=$(jq -nc --arg path "$directory/service.log" --arg text "$query found" '[[ $path,2,$text ]]')
  jq -e --argjson expected "$expected" '.machine == "target" and .limited == false and
    (.matches | map([.path,.line,.text])) == $expected' <<< "$rest" >/dev/null
  session=$(jq -er '.session' <<< "$rest")
  curl -fsS -H "Authorization: Bearer $token" "$status_url/api/v1/sessions/$session" |
    jq -e --arg directory "$directory" '.kind == "file" and .recording_available and
      .transcript_state == "complete" and .command == ("search " + $directory)' >/dev/null

  remote=$(jq -nc --argjson input "$request" '{jsonrpc:"2.0",id:61,method:"tools/call",
    params:{name:"search_files",arguments:($input + {machine:"target"})}}')
  rpc "$remote" | jq -e --argjson expected "$expected" '.result.structuredContent |
    .machine == "target" and (.matches | map([.path,.line,.text])) == $expected' >/dev/null
  test "$(curl -sS -o "$work/search-denied.json" -w '%{http_code}' \
    -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
    --data "$request" "$status_url/api/v1/machines/second-target/search")" = 403
  jq -e '.code == "machine.forbidden"' "$work/search-denied.json" >/dev/null
  printf 'The signed release found the same literal line through REST and MCP and refused search outside the Agent machine scope.\n'
)
