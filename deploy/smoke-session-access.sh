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
