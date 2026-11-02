#!/bin/sh
# Installs or updates linkos-probe, the probe agent, on this machine as the systemd service linkos-probe.
#   sudo sh install.sh -gateway https://metrics.example.com -token-file edge-1.token [-version v0.3.0-rc.2]
# The token comes from `linkos-beacon enroll probe <location id>` on the centre (printed once); the file is
# moved into /etc/linkos-agent/token. The binary is downloaded from the GitHub release and checked against its
# SHA256SUMS; -binary FILE installs one you fetched yourself (a machine without access to GitHub). Idempotent:
# run it again to update the binary (a new -version) or the token; only what changed is replaced.
set -eu

version=v0.3.0-rc.2 # BEACON_VERSION in deploy/.env; the probe and the service are released together
gateway=""
token=""
binary=""
usage() {
  echo "usage: install.sh -gateway URL -token-file FILE [-version vX.Y.Z] [-binary FILE]" >&2
  exit 2
}
while [ $# -gt 0 ]; do
  case "$1" in
    -gateway) gateway=${2:-}; shift 2 ;;
    -token-file) token=${2:-}; shift 2 ;;
    -version) version=${2:-}; shift 2 ;;
    -binary) binary=${2:-}; shift 2 ;;
    *) usage ;;
  esac
done
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run it as root"
command -v systemctl >/dev/null 2>&1 || die "this machine has no systemd; run linkos-probe agent your own way (see the unit for the command line)"
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "no release for $(uname -m); build linkos-probe yourself and pass -binary" ;;
esac
here=$(cd "$(dirname "$0")" && pwd)

# the account, the directories: the token readable by the agent's group only; the textfile directory for heartbeats
id linkos-agent >/dev/null 2>&1 || useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin linkos-agent
install -d -m 0755 /opt/linkos-agent /opt/linkos-agent/bin /var/lib/linkos-agent /var/lib/linkos-agent/textfile
install -d -o root -g linkos-agent -m 0750 /etc/linkos-agent

changed=""
put() { # put SOURCE TARGET MODE [OWNER:GROUP]: install when it differs
  if ! cmp -s "$1" "$2"; then
    install -o "${4%%:*}" -g "${4#*:}" -m "$3" "$1" "$2"
    changed="$changed ${2##*/}"
  fi
}
if [ -n "$token" ]; then
  [ -s "$token" ] || die "the token file $token is empty or missing"
  put "$token" /etc/linkos-agent/token 0640 root:linkos-agent
fi
[ -s /etc/linkos-agent/token ] || die "no token: -token-file FILE, with the output of 'linkos-beacon enroll probe <id>'"
if [ -n "$gateway" ]; then
  t=$(mktemp)
  printf 'GATEWAY=%s\n' "$gateway" > "$t"
  put "$t" /etc/linkos-agent/agent.env 0640 root:linkos-agent
  rm -f "$t"
fi
[ -s /etc/linkos-agent/agent.env ] || die "no gateway: -gateway https://metrics.example.com"

# the binary, from the release unless given
fetch() { # fetch URL FILE
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    die "neither curl nor wget is installed; pass -binary FILE"
  fi
}
if [ -z "$binary" ]; then
  d=$(mktemp -d)
  v=${version#v}
  base="https://github.com/openlinkos/beacon/releases/download/$version"
  archive="linkos-probe_${v}_linux_${arch}.tar.gz"
  fetch "$base/SHA256SUMS" "$d/SHA256SUMS" || die "cannot download $base/SHA256SUMS"
  fetch "$base/$archive" "$d/$archive" || die "cannot download $base/$archive"
  (cd "$d" && grep " $archive\$" SHA256SUMS | sha256sum -c --status) || die "$archive does not match SHA256SUMS"
  tar -xzf "$d/$archive" -C "$d" linkos-probe
  binary="$d/linkos-probe"
fi
put "$binary" /opt/linkos-agent/bin/linkos-probe 0755 root:root
put "$here/linkos-probe.service" /etc/systemd/system/linkos-probe.service 0644 root:root
[ ! -f "$here/linkos-heartbeat" ] || put "$here/linkos-heartbeat" /usr/local/bin/linkos-heartbeat 0755 root:root
rm -rf "${d:-}"

systemctl daemon-reload
systemctl enable linkos-probe >/dev/null 2>&1
if [ -n "$changed" ]; then
  systemctl restart linkos-probe
fi
systemctl is-active --quiet linkos-probe || systemctl start linkos-probe

if [ "$(timedatectl show -p NTPSynchronized --value 2>/dev/null || echo unknown)" != yes ]; then
  echo "warning: the clock is not synchronised with NTP; results carry timestamps, so install chrony or enable systemd-timesyncd" >&2
fi

# the first push: the agent's own /metrics says when the gateway last accepted one
get() {
  if command -v curl >/dev/null 2>&1; then curl -fsS --max-time 3 "$1" 2>/dev/null; else wget -q -O - "$1" 2>/dev/null; fi
}
ok=0
i=0
while [ $i -lt 75 ]; do
  if get http://127.0.0.1:17009/metrics | grep -Eq '^linkos_probe_last_push_timestamp_seconds [1-9]'; then
    ok=1
    break
  fi
  sleep 2
  i=$((i + 1))
done
rss=$(ps -o rss= -p "$(systemctl show -p MainPID --value linkos-probe)" 2>/dev/null | awk '{ printf "%.0f MB resident", $1 / 1024 }' || true)
gw=$(sed -n 's/^GATEWAY=//p' /etc/linkos-agent/agent.env)
echo "$(hostname): linkos-probe $(/opt/linkos-agent/bin/linkos-probe version 2>/dev/null | head -n 1)${changed:+; installed:$changed}; ${rss:-running}"
if [ $ok = 1 ]; then
  echo "the gateway at $gw accepted the first push; the location's results arrive every minute"
else
  die "no push accepted by $gw within 150 seconds: journalctl -u linkos-probe -n 50"
fi
