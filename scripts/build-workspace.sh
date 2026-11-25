#!/bin/sh
# Build the client separately, then bundle only the static output into Beacon.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
pnpm --dir frontend install --frozen-lockfile
pnpm --dir frontend build

destination=internal/web/static/workspace
rm -rf -- "$destination"
mkdir -p "$destination"
cp -R frontend/dist/. "$destination/"
