#!/bin/sh
# Refreshes the design system the console is drawn with, @openlinkos/ui, vendored under
# internal/web/static/ol so that the beacon binary needs nothing from the network:
#
#   scripts/update-ui.sh [version]      # default: the version named below
#
# It fetches the package with npm pack (no install, no scripts run), copies the stylesheet, the icon
# set, the fonts and the licences, and writes NOTICE with the version, where it came from and the
# SHA-256 of the package and of every file. Review the diff, run go test ./internal/web/..., and
# delete internal/web/static/beacon-brand.css once the package ships data-brand="beacon".
set -eu

version=${1:-0.7.0}
root=$(cd "$(dirname "$0")/.." && pwd)
dest=$root/internal/web/static/ol

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM
mkdir "$work/pack" "$work/x"
(cd "$work/pack" && npm pack --ignore-scripts --silent "@openlinkos/ui@$version" >/dev/null)
tgz=$work/pack/openlinkos-ui-$version.tgz
[ -f "$tgz" ] || { echo "npm pack did not produce $tgz" >&2; exit 1; }
tar -xzf "$tgz" -C "$work/x"
pkg=$work/x/package
got=$(sed -n 's/^  "version": "\(.*\)",$/\1/p' "$pkg/package.json")
[ "$got" = "$version" ] || { echo "the package says version $got, not $version" >&2; exit 1; }

# The files the console serves (ui.css, the fonts beside it) or reads (icons.json), and their licences.
files="ui.css icons.json fonts/Geist-Variable.woff2 fonts/GeistMono-Variable.woff2 fonts/LICENSE-geist.txt LICENSE-lucide.txt"
rm -rf "$dest"
mkdir -p "$dest/fonts"
for f in $files; do
	cp "$pkg/dist/$f" "$dest/$f"
done
cp "$pkg/LICENSE" "$dest/LICENSE"

{
	echo "@openlinkos/ui $version, the OpenLinkOS design system, vendored for the console (internal/web)."
	echo "Nothing here is fetched from the network at run time; the files are embedded in the beacon"
	echo "binary. Refresh them with scripts/update-ui.sh; do not edit them by hand."
	echo ""
	echo "  Licence: MIT (LICENSE); the Lucide icons ISC (LICENSE-lucide.txt); Geist and Geist Mono"
	echo "  SIL Open Font License 1.1 (fonts/LICENSE-geist.txt)."
	echo "  Source: https://github.com/openlinkos/design/tree/master/packages/ui"
	echo "  Package: https://registry.npmjs.org/@openlinkos/ui/-/ui-$version.tgz"
	echo "    SHA-256 $(sha256 "$tgz")"
	echo "  Files, as they are in the package (dist/, and LICENSE at its root):"
	for f in $files LICENSE; do
		echo "    $f"
		echo "      SHA-256 $(sha256 "$dest/$f")"
	done
} >"$dest/NOTICE"

echo "vendored @openlinkos/ui $version into ${dest#"$root"/}"
