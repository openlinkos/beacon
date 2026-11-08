#!/usr/bin/env bash
# The site's smoke test: reads a deployed site and fails at the first page, redirect or header that is not as
# it should be. The site workflow runs it on every deployment, preview or production.
#   bash docs/smoke.sh https://<deployment>.<project>.pages.dev
set -euo pipefail
s="${1:?the address of the deployed site}"
s="${s%/}"

# A new deployment can take a few seconds to answer everywhere, so every request is retried on errors: a failed
# connection always, and an HTTP error only where -f makes it one (a 301 or a 404 asked for is not retried).
get() { curl -sS --retry 5 --retry-all-errors --retry-delay 3 "$@"; }
# grep reads the whole page (no -q): with pipefail, -q would stop early and fail curl with SIGPIPE on a long page
page() { get -f "$s$1"; }
has() { page "$1" | grep -F -- "$2" >/dev/null || { echo "$1 lacks: $2" >&2; exit 1; }; }

has / 'Infrastructure for AI agents, on the record'
has / 'beacon/deploy/try'
has / 'One outage, end to end'
has / 'hreflang="zh-CN"'
has / 'id="openlinkos-lang"'
# The Chinese title lives on the page; this file stays in English (TestNoChinese).
title=$(sed -n 's/^title: //p' "$(dirname "$0")/zh/index.md" | head -n 1)
test -n "$title"
has /zh/ "$title"
has /zh/ 'lang="zh-CN"'
has /guide/on-call 'Put an agent on call'
has /guide/using-beacon 'Using Beacon'
has /self-hosting/install 'On a server'
has /guide/quickstart 'Try it on your laptop'
has /reference/api 'auth.scope'
has /reference/mcp 'probe_now'
has /changelog 'Changelog'
has /zh/changelog 'lang="zh-CN"'
has /zh/guide/people 'lang="zh-CN"'
has /zh/about/architecture 'lang="zh-CN"'

# The pages the site structure moved answer on their old addresses with a 301 to their new homes (_redirects).
while read -r old new _; do
  case "$old" in '' | '#'*) continue ;; esac
  headers=$(get -I "$s$old" | tr -d '\r')
  echo "$headers" | grep -E '^HTTP/[0-9.]+ 301' >/dev/null || { echo "$old does not answer 301" >&2; exit 1; }
  echo "$headers" | grep -iE "^location: ($s)?$new\$" >/dev/null || { echo "$old is not sent to $new" >&2; exit 1; }
done <"$(dirname "$0")/public/_redirects"

code() { get -o /dev/null -w '%{http_code}' "$s$1"; }
test "$(code /no-such-page)" = 404 || { echo "/no-such-page does not answer 404" >&2; exit 1; }
test "$(code /zh/no-such-page)" = 404 || { echo "/zh/no-such-page does not answer 404" >&2; exit 1; }

# _headers: pages are revalidated, the hashed assets kept for a year
get -fI "$s/" | tr -d '\r' | grep -i '^cache-control: no-cache$' >/dev/null || { echo "/ is not served no-cache" >&2; exit 1; }
asset=$(page / | grep -o '/assets/[^"]*\.css' | head -n 1)
test -n "$asset"
get -fI "$s$asset" | tr -d '\r' | grep -i '^cache-control: public, max-age=31536000, immutable$' >/dev/null ||
  { echo "$asset is not served immutable" >&2; exit 1; }

# brand assets, metadata and language detection from @openlinkos/vitepress-theme
get -f -o /dev/null "$s/favicon.ico"
has /site.webmanifest '"icons"'
has /robots.txt 'Sitemap: https://beacon.openlinkos.com/sitemap.xml'
has /sitemap.xml 'hreflang="x-default"'
has /.well-known/security.txt 'Contact:'
echo "the site at $s is as it should be"
