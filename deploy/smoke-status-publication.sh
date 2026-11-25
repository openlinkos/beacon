#!/usr/bin/env bash
# Sourced by smoke.sh. The temporary monitor checks the local centre, and is removed before the drill.

smoke_public_components() {
  curl -fsS --max-time 15 "$status_url/status/status.json" |
    jq -ce '.components | map({name, title}) | sort_by(.name)'
}

smoke_owner_form_post() {
  local path=$1 form=$2 result=$3 csrf base
  shift 3
  csrf=$(sed -n 's/.*name="csrf" value="\([^"]*\)".*/\1/p' "$form" | awk 'NR == 1 {print}')
  base=$(sed -n 's/.*name="version" value="\([^"]*\)".*/\1/p' "$form" | awk 'NR == 1 {print}')
  test -n "$csrf" && test -n "$base" || return 1
  curl -fsS --max-time 15 -b "$work/cookies" -o "$result" -D "$result.headers" -w '%{http_code}' \
    --data-urlencode "csrf=$csrf" --data-urlencode "version=$base" "$@" "$status_url$path"
}

smoke_new_monitor_results() {
  query "probe_success{service=\"$1\",module=\"http_2xx\",visibility=\"internal\"}" |
    jq -e '[.data.result[] | select(.value[1] == "1") | .metric.location] | unique | sort == ["edge-1", "edge-2"]' >/dev/null
}

smoke_add_publication_monitor() {
  local service=$1 form="$work/publication-monitor-add.html"
  curl -fsS --max-time 15 -b "$work/cookies" "$status_url/console/monitors/add/" > "$form"
  if ! grep -Fq 'value="http_2xx"' "$form"; then
    printf 'The reference deployment must offer a header-free HTTP 2xx profile in the console.\n' >&2
    return 1
  fi
  test "$(smoke_owner_form_post /console/monitors "$form" "$work/publication-monitor-added" \
    --data-urlencode "service=$service" --data-urlencode "url=$status_url/healthz" \
    --data-urlencode scope=all --data-urlencode module=http_2xx --data-urlencode severity=info)" = 303
  wait_for 'Both probes passed the new internal console monitor' 120 smoke_new_monitor_results "$service"
}

smoke_remove_publication_monitor() {
  local path="/console/monitors/$1" form="$work/publication-monitor-edit.html"
  curl -fsS --max-time 15 -b "$work/cookies" "$status_url$path/edit" > "$form"
  test "$(smoke_owner_form_post "$path/remove" "$form" "$work/publication-monitor-removed")" = 303
  test "$(curl -sS --max-time 15 -b "$work/cookies" -o /dev/null -w '%{http_code}' "$status_url$path")" = 404
}

smoke_status_publication() {
  local page=/console/status-pages/status id=reference-component title='Reference test component'
  local service=reference-internal-service
  local add_form="$work/status-add.html" add_preview="$work/status-add-preview.html"
  local remove_form="$work/status-remove.html" remove_preview="$work/status-remove-preview.html"
  local response baseline expected redirect
  local add_path="$page/components/add" remove_path="$page/components/$id/remove"
  local -a fields=(--data-urlencode "component=$id" --data-urlencode "title=$title" --data-urlencode "service=$service")

  response=$(curl -sS --max-time 15 -b "$work/cookies" -o "$add_form" -w '%{http_code}' "$status_url$add_path")
  if [[ "$response" == 404 && "$version" == v0.3.0-rc.33 ]]; then
    printf 'The pinned release %s predates console status publication.\n' "$version"
    return
  fi
  if [[ "$response" != 200 ]]; then
    printf 'Expected the console status publication form on %s, got HTTP %s.\n' "$version" "$response" >&2
    return 1
  fi

  baseline=$(smoke_public_components)
  smoke_add_publication_monitor "$service"
  curl -fsS --max-time 15 -b "$work/cookies" "$status_url$add_path" > "$add_form"
  jq -e --arg id "$id" 'length > 0 and all(.[]; .name != $id)' <<< "$baseline" >/dev/null
  expected=$(jq -c --arg id "$id" --arg title "$title" '. + [{name: $id, title: $title}] | sort_by(.name)' <<< "$baseline")
  test "$(smoke_owner_form_post "$add_path" "$add_form" "$add_preview" --data-urlencode intent=preview "${fields[@]}")" = 200
  grep -Fq 'Newly visible services' "$add_preview"
  test "$(smoke_public_components)" = "$baseline"
  printf 'The owner preview left the signed-out status page unchanged.\n'

  test "$(smoke_owner_form_post "$add_path" "$add_preview" "$work/status-published" --data-urlencode intent=publish "${fields[@]}")" = 303
  redirect=$(awk 'tolower($1) == "location:" {sub(/\r$/, "", $2); print $2}' "$work/status-published.headers")
  test "$redirect" = "$page?done=status_page_updated"
  test "$(smoke_public_components)" = "$expected"
  curl -fsS --max-time 15 "$status_url/status" > "$work/status-public.html"
  grep -Fq "$title" "$work/status-public.html"
  printf 'Published status component is visible in the signed-out page and JSON.\n'

  curl -fsS --max-time 15 -b "$work/cookies" "$status_url$remove_path" > "$remove_form"
  test "$(smoke_owner_form_post "$remove_path" "$remove_form" "$remove_preview" --data-urlencode intent=preview --data-urlencode "component=$id")" = 200
  grep -Fq 'Services that will no longer be visible' "$remove_preview"
  test "$(smoke_public_components)" = "$expected"
  test "$(smoke_owner_form_post "$remove_path" "$remove_preview" "$work/status-removed" --data-urlencode intent=publish --data-urlencode "component=$id")" = 303
  redirect=$(awk 'tolower($1) == "location:" {sub(/\r$/, "", $2); print $2}' "$work/status-removed.headers")
  test "$redirect" = "$page?done=status_page_updated"
  test "$(smoke_public_components)" = "$baseline"
  curl -fsS --max-time 15 "$status_url/status" > "$work/status-after-removal.html"
  if grep -Fq "$title" "$work/status-after-removal.html"; then
    printf 'The temporary component is still visible after removal.\n' >&2
    return 1
  fi
  smoke_remove_publication_monitor "$service"
  printf 'Removal preview kept the component public, and publication restored the page before the drill.\n'
}
