#!/bin/sh
# check-test-leaks.sh — the fixture leak check the secrets packages' gate runs.
#
# It runs the vault's tests with everything they print captured, then greps the output for the
# fixture values the tests use (made up, never a real export; the list is internal/secrets/
# vault_test.go's fixtureValues and internal/secrets/importer/importer_test.go's fxMarkers). A
# value that reaches a log line, an error, an audit text or a test's own output is a leak: the
# check fails and prints where. The import's own dry-run check is here too: the report a dry-run
# answers must carry no value, which the import tests assert and this script re-checks over the
# whole output.
#
# Usage: scripts/check-test-leaks.sh [package...]   (default: the packages that print fixture
# values — the vault, the console, the API, the admin socket, MCP and cmd/beacon)

set -eu

if [ "$#" -eq 0 ]; then
    set -- ./internal/secrets/... ./internal/web/... ./internal/api/... ./internal/ctl/... ./internal/mcpserver/... ./cmd/beacon
fi
PACKAGES=$*

# the fixture markers, kept in step with internal/secrets/vault_test.go and
# internal/secrets/importer/importer_test.go
MARKERS='
fixture-value-Xk7-plugh-Quixote-9b
fixture-value-API-zephyr-5c2d-grault
fixture-notes-caboosetumbler
fixture-attachment-tumbler-quartz
fixture-user-zz31@example.com
GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ
fixture-import-1pux-value-xyzzy-frobnicate
fixture-import-1pux-notes-tumbler
fixture-import-1pux-attachment-quartz
fixture-import-1pux-old-value-grault
fixture-import-chrome-value-plugh-quux9
fixture-import-bitwarden-value-cleesh3
fixture-import-op-value-frotz5blorb
fixture-import-csv-value-waldo-fred7
fixture-import-1pux-user@example.com
fixture-import-chrome-user@example.com
fixture-import-web-value-3
'

OUT=$(mktemp)
trap 'rm -f "$OUT"' EXIT

# -v so every log line the tests write is captured
if ! go test -v -count=1 $PACKAGES >"$OUT" 2>&1; then
    echo "the tests themselves failed; their output follows" >&2
    cat "$OUT" >&2
    exit 1
fi

STATUS=0
for m in $MARKERS; do
    if grep -F -n -- "$m" "$OUT" >/dev/null 2>&1; then
        echo "LEAK: the fixture value marker $m appears in the tests' output:" >&2
        grep -F -n -- "$m" "$OUT" | head -5 >&2
        STATUS=1
    fi
done

if [ "$STATUS" -eq 0 ]; then
    echo "check-test-leaks: no fixture value appears in the tests' output"
fi
exit $STATUS
