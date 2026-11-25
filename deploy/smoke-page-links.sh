#!/usr/bin/env bash
# Sourced by smoke.sh. All links and recorded commands belong to the disposable runner.

smoke_share_status() {
  local expected=$1 url=$2 response
  shift 2
  response=$(curl -sS --max-time 15 -o /dev/null -w '%{http_code}' "$@" "$url")
  if [[ "$response" != "$expected" ]]; then
    printf 'Expected HTTP %s at %s, got %s.\n' "$expected" "${url%%\?*}" "$response" >&2
    return 1
  fi
}

smoke_share_form() {
  local path=$1 result=$2 intent=$3 csrf
  csrf=$(curl -fsS --max-time 15 -b "$work/cookies" "$status_url$path" |
    sed -n 's/.*name="csrf" value="\([^"]*\)".*/\1/p' | awk 'NR == 1 {print}')
  test -n "$csrf" || return 1
  curl -sS --max-time 15 -b "$work/cookies" -o "$result" -D "$result.headers" -w '%{http_code}' \
    --data-urlencode "csrf=$csrf" --data-urlencode "intent=$intent" "$status_url$path"
}

smoke_create_share() {
  local path=$1 result=$2
  test "$(smoke_share_form "$path/share-links" "$result" create)" = 200 || return 1
  sed -n 's@.*<code id="issued-share-url">\([^<]*\)</code>.*@\1@p' "$result"
}

smoke_page_link_revocation() {
  local alert_path="/console/alerts/$1" replay="/console/sessions/$2" other_id other_path run
  local incident_url replay_url other_url recording_path recording_url replacement redirect url alias sign_in
  local management="$replay/share-links" preview="$work/shared-alert.html"
  run=$(rpc '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"run_command","arguments":{"machine":"target","command":"id -un"}}}')
  other_id=$(jq -er '.result.structuredContent | select(.exit_code == 0 and .stdout == "beacon\n") | .session' <<< "$run")
  test "$other_id" != "$2"
  other_path="/console/sessions/$other_id"

  incident_url=$(smoke_create_share "$alert_path" "$work/share-alert.html")
  replay_url=$(smoke_create_share "$replay" "$work/share-session.html")
  other_url=$(smoke_create_share "$other_path" "$work/share-other-session.html")
  [[ "$incident_url" == "$status_url$alert_path/episodes/"*\?s=* ]]
  [[ "$replay_url" == "$status_url$replay"\?s=* ]]
  [[ "$other_url" == "$status_url$other_path"\?s=* ]]
  curl -fsS --max-time 15 "$incident_url" > "$preview"
  for url in "$replay" "$other_path"; do
    sign_in="/console/sign-in?next=$(jq -nr --arg page "$url" '$page | @uri')"
    grep -Fq "href=\"$sign_in\"" "$preview"
    if grep -Fq -e "$url?" -e "$url/recording" "$preview"; then
      echo 'The signed-out incident contained a direct recording or signed replay link.' >&2
      return 1
    fi
  done
  smoke_share_status 404 "$status_url$replay?${incident_url#*\?}"
  smoke_share_status 303 "$status_url$management?${incident_url#*\?}" --data-urlencode intent=create -D "$work/anonymous-share.headers"
  test "$(awk 'tolower($1) == "location:" {sub(/\r$/, "", $2); print $2}' "$work/anonymous-share.headers")" = /console/sign-in
  printf 'The signed incident opened without a session and asked readers to sign in for both related replays.\n'

  curl -fsS --max-time 15 "$replay_url" > "$work/shared-replay.html"
  recording_path=$(sed -n 's/.*id="player".*data-src="\([^"]*\)".*/\1/p' "$work/shared-replay.html")
  [[ "$recording_path" == "$replay/recording"\?s=* ]]
  recording_url="$status_url$recording_path"
  curl -fsS --max-time 15 "$recording_url" > "$work/shared-recording"
  cmp "$work/recording" "$work/shared-recording"

  smoke_share_status 403 "$status_url$management" -b "$work/cookies" --data-urlencode intent=revoke
  smoke_share_status 200 "$replay_url"
  test "$(smoke_share_form "$management" "$work/revoke-session" revoke)" = 303
  redirect=$(awk 'tolower($1) == "location:" {sub(/\r$/, "", $2); print $2}' "$work/revoke-session.headers")
  [[ "$redirect" == "$management" || "$redirect" == "$management"\?* ]]
  printf '%s\n' "$replay_url" "$recording_url" > "$work/revoked-page-links"
  smoke_page_links_denied

  # A modern signature names its canonical path and must never gain authority through the old path.
  for url in "$replay_url" "$recording_url"; do
    alias=$status_url${url#"$status_url/console"}
    test "$(curl -sS --max-time 15 -o /dev/null -D "$work/share-alias.headers" -w '%{http_code}' "$alias")" = 308
    redirect=$(awk 'tolower($1) == "location:" {sub(/\r$/, "", $2); print $2}' "$work/share-alias.headers")
    test "$status_url$redirect" = "${url%%\?*}"
    smoke_share_status 404 "$status_url$redirect"
  done
  curl -fsS --max-time 15 -b "$work/cookies" "$status_url$replay" > "$work/owner-replay.html"
  grep -Fq 'id="player"' "$work/owner-replay.html"
  curl -fsS --max-time 15 -b "$work/cookies" "$status_url$replay/recording" > "$work/owner-recording"
  cmp "$work/recording" "$work/owner-recording"
  smoke_share_status 200 "$other_url"
  smoke_share_status 200 "$incident_url"
  replacement=$(smoke_create_share "$replay" "$work/share-replacement.html")
  [[ "$replacement" == "$status_url$replay"\?s=* && "$replacement" != "$replay_url" ]]
  smoke_share_status 200 "$replacement"
  smoke_page_links_denied
  printf 'The owner revoked the shared replay and recording; their own playback and the other shares still worked.\n'
}

smoke_page_links_denied() {
  local url
  test -s "$work/revoked-page-links" || return 1
  while IFS= read -r url; do
    smoke_share_status 404 "$url" || return 1
  done < "$work/revoked-page-links"
}

smoke_page_links_restored() {
  smoke_page_links_denied
  printf 'The cold restore retained the revoked replay and recording link state.\n'
}
