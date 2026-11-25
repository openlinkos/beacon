#!/usr/bin/env bash
# Sourced by smoke.sh. The temporary monitor checks the local centre, and is removed before the drill.

smoke_public_components() {
  curl -fsS --max-time 15 "$status_url/status/status.json" |
    jq -ce '.components | map({name, title}) | sort_by(.name)'
}

smoke_owner_form_post() {
  local path=$1 form=$2 result=$3 csrf base confirmation
  shift 3
  csrf=$(sed -n 's/.*name="csrf" value="\([^"]*\)".*/\1/p' "$form" | awk 'NR == 1 {print}')
  base=$(sed -n 's/.*name="version" value="\([^"]*\)".*/\1/p' "$form" | awk 'NR == 1 {print}')
  confirmation=$(sed -n 's/.*name="confirmation" value="\([^"]*\)".*/\1/p' "$form" | awk 'NR == 1 {print}')
  test -n "$csrf" && test -n "$base" || return 1
  curl -sS --max-time 15 -b "$work/cookies" -o "$result" -D "$result.headers" -w '%{http_code}' \
    --data-urlencode "csrf=$csrf" --data-urlencode "version=$base" --data-urlencode "confirmation=$confirmation" "$@" "$status_url$path"
}

smoke_public_maintenance() {
  curl -fsS --max-time 15 "$status_url/status/status.json" |
    jq -c --arg title "$1" --arg description "$2" 'any(.maintenance[]?; .title == $title and .description == $description)'
}

smoke_schedule_publication_maintenance() {
  local secret=$1 service=$2 title=$3 description=$4 start end body
  read -r start end < <(python3 - <<'PY'
from datetime import datetime, timedelta, timezone

now = datetime.now(timezone.utc).replace(microsecond=0)
print((now + timedelta(hours=2)).isoformat(), (now + timedelta(hours=3)).isoformat())
PY
)
  body=$(jq -cn --arg service "$service" --arg title "$title" --arg description "$description" \
    --arg start "$start" --arg end "$end" \
    '{services: [$service], title: $title, description: $description, starts_at: $start, ends_at: $end}')
  curl -fsS --max-time 15 "$status_url/api/v1/maintenance" \
    -H "Authorization: Bearer $secret" -H 'Content-Type: application/json' --data "$body" | jq -er '.id'
}

smoke_restore_starter_status_file() {
  if [[ -f "$work/starter-status-pages.yaml" ]]; then
    mv "$work/starter-status-pages.yaml" catalog/status_pages.yaml
  fi
}

smoke_first_status_private() {
  local name=$1 title=$2 maintenance=$3 description=$4 path response
  local body="$work/status-first-unpublished.html"
  curl -fsS --max-time 15 "$status_url/" > "$body"
  if grep -Fq -e "$name" -e "$title" -e "$maintenance" -e "$description" "$body"; then
    echo 'The signed-out landing page exposed the unpublished page or maintenance.' >&2
    return 1
  fi
  for path in /status /status.json "/status/$name" "/status/$name.json"; do
    response=$(curl -sS --max-time 15 -o "$body" -w '%{http_code}' "$status_url$path")
    if [[ "$response" != 404 ]] || grep -Fq -e "$name" -e "$title" -e "$maintenance" -e "$description" "$body"; then
      printf 'Expected HTTP 404 with no draft content on signed-out %s, got HTTP %s.\n' "$path" "$response" >&2
      return 1
    fi
  done
}

smoke_first_status_page() {
  local path=/console/status-pages/new/ name=first-reference-page title='First reference status page'
  local maintenance='First page private maintenance' description='Maintaining the reference centre before publication.'
  local form="$work/status-first-form.html" preview="$work/status-first-preview.html" published="$work/status-first-published"
  local canonical="$status_url/status/$name" history maintenance_secret maintenance_id redirect url
  local -a fields=(--data-urlencode "name=$name" --data-urlencode "title=$title" --data-urlencode service=beacon)

  if ! grep -Fq 'href="/console/status-pages"' "$work/console.html"; then
    printf 'The owner has no Status pages navigation with an empty catalogue.\n' >&2
    return 1
  fi
  curl -fsS --max-time 15 -b "$work/cookies" "$status_url/console/status-pages" > "$work/status-first-list.html"
  if ! grep -Fq "href=\"$path\"" "$work/status-first-list.html"; then
    printf 'The owner catalogue has no link to create the first status page.\n' >&2
    return 1
  fi
  maintenance_secret=$(beacon token create smoke-first-publication -scopes maintenance:write -expires 1h)
  printf '::add-mask::%s\n' "$maintenance_secret"
  maintenance_id=$(smoke_schedule_publication_maintenance "$maintenance_secret" beacon "$maintenance" "$description")
  smoke_first_status_private "$name" "$title" "$maintenance" "$description"

  history=$(beacon config history -limit 1)
  curl -fsS --max-time 15 -b "$work/cookies" "$status_url$path" > "$form"
  test "$(smoke_owner_form_post "$path" "$form" "$work/status-first-confirmation-required" --data-urlencode intent=publish "${fields[@]}")" = 409
  smoke_first_status_private "$name" "$title" "$maintenance" "$description"
  test "$(smoke_owner_form_post "$path" "$form" "$preview" --data-urlencode intent=preview "${fields[@]}")" = 200
  grep -Fq 'Maintenance announcements that will become public' "$preview"
  grep -Fq "$maintenance" "$preview"
  grep -Fq "$description" "$preview"
  test "$(beacon config history -limit 1)" = "$history"
  smoke_first_status_private "$name" "$title" "$maintenance" "$description"
  printf 'First status page review kept the page and maintenance private.\n'

  test "$(smoke_owner_form_post "$path" "$preview" "$published" --data-urlencode intent=publish "${fields[@]}")" = 303
  redirect=$(awk 'tolower($1) == "location:" {sub(/\r$/, "", $2); print $2}' "$published.headers")
  test "$redirect" = "/console/status-pages/$name?done=status_page_created"
  for url in "$canonical" "$status_url/status" "$status_url/"; do
    curl -fsS --max-time 15 "$url" > "$work/status-first-public.html"
    grep -Fq "<link rel=\"canonical\" href=\"$canonical\">" "$work/status-first-public.html"
    grep -Fq "$title" "$work/status-first-public.html"
    grep -Fq "$maintenance" "$work/status-first-public.html"
    grep -Fq "$description" "$work/status-first-public.html"
  done
  for url in "$canonical.json" "$status_url/status.json"; do
    curl -fsS --max-time 15 "$url" |
      jq -e --arg name "$name" --arg title "$title" --arg url "$canonical" --arg id "$maintenance_id" \
        --arg maintenance "$maintenance" --arg description "$description" '
        .name == $name and .title == $title and .url == $url and ([.components[].name] == ["services"]) and
        any(.maintenance[]?; .id == $id and .title == $maintenance and .description == $description)' >/dev/null
  done
  printf 'First status page and reviewed maintenance are public on the named and default routes.\n'

  curl -fsS --max-time 15 -X DELETE "$status_url/api/v1/maintenance/$maintenance_id" -H "Authorization: Bearer $maintenance_secret" >/dev/null
  beacon token revoke smoke-first-publication >/dev/null
}

smoke_first_status_restored() {
  test "$(curl -sS --max-time 15 -o /dev/null -w '%{http_code}' "$status_url/status/first-reference-page")" = 404
  test "$(curl -sS --max-time 15 -o /dev/null -w '%{http_code}' "$status_url/status/first-reference-page.json")" = 404
  curl -fsS --max-time 15 "$status_url/status.json" |
    jq -e '.name == "status" and ([.components[].name] | sort) == ["beacon", "website"] and
      all(.maintenance[]?; .title != "First page private maintenance")' >/dev/null
  printf 'The starter status page was restored before the component and outage drills.\n'
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
  local maintenance_secret='' maintenance_id=''
  local maintenance_title='Reference private maintenance' maintenance_text='Replacing a component on the internal test service.'
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
  if [[ "$version" != v0.3.0-rc.34 && "$version" != v0.3.0-rc.35 ]]; then
    maintenance_secret=$(beacon token create smoke-publication -scopes maintenance:write -expires 1h)
    printf '::add-mask::%s\n' "$maintenance_secret"
    maintenance_id=$(smoke_schedule_publication_maintenance "$maintenance_secret" "$service" "$maintenance_title" "$maintenance_text")
    test "$(smoke_public_maintenance "$maintenance_title" "$maintenance_text")" = false
  fi
  curl -fsS --max-time 15 -b "$work/cookies" "$status_url$add_path" > "$add_form"
  jq -e --arg id "$id" 'length > 0 and all(.[]; .name != $id)' <<< "$baseline" >/dev/null
  expected=$(jq -c --arg id "$id" --arg title "$title" '. + [{name: $id, title: $title}] | sort_by(.name)' <<< "$baseline")
  test "$(smoke_owner_form_post "$add_path" "$add_form" "$add_preview" --data-urlencode intent=preview "${fields[@]}")" = 200
  grep -Fq 'Newly visible services' "$add_preview"
  test "$(smoke_public_components)" = "$baseline"
  if [[ -n "$maintenance_id" ]]; then
    grep -Fq "$maintenance_title" "$add_preview"
    grep -Fq "$maintenance_text" "$add_preview"
    test "$(smoke_public_maintenance "$maintenance_title" "$maintenance_text")" = false
    test "$(smoke_owner_form_post "$add_path" "$add_form" "$work/status-missing-confirmation" --data-urlencode intent=publish "${fields[@]}")" = 409
  fi
  printf 'The owner preview left the signed-out status page unchanged.\n'

  test "$(smoke_owner_form_post "$add_path" "$add_preview" "$work/status-published" --data-urlencode intent=publish "${fields[@]}")" = 303
  redirect=$(awk 'tolower($1) == "location:" {sub(/\r$/, "", $2); print $2}' "$work/status-published.headers")
  test "$redirect" = "$page?done=status_page_updated"
  test "$(smoke_public_components)" = "$expected"
  curl -fsS --max-time 15 "$status_url/status" > "$work/status-public.html"
  grep -Fq "$title" "$work/status-public.html"
  if [[ -n "$maintenance_id" ]]; then
    grep -Fq "$maintenance_title" "$work/status-public.html"
    grep -Fq "$maintenance_text" "$work/status-public.html"
    test "$(smoke_public_maintenance "$maintenance_title" "$maintenance_text")" = true
    printf 'The owner saw the exact maintenance announcement before it appeared on the signed-out status page.\n'
  fi
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
  if [[ -n "$maintenance_id" ]]; then
    test "$(smoke_public_maintenance "$maintenance_title" "$maintenance_text")" = false
    curl -fsS --max-time 15 -X DELETE "$status_url/api/v1/maintenance/$maintenance_id" -H "Authorization: Bearer $maintenance_secret" >/dev/null
    beacon token revoke smoke-publication >/dev/null
  fi
  smoke_remove_publication_monitor "$service"
  printf 'Removal preview kept the component public, and publication restored the page before the drill.\n'
}
