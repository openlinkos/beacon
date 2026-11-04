#!/bin/sh
# The demo machine's first boot: sshd that trusts Beacon's certificate authority, and the web server the
# probe checks. /secrets holds what up.sh made on this machine: ssh-ca-key.pub (Beacon's certificate
# authority) and demo-host-key (this machine's own host key, so Beacon pins one that survives a restart).
set -eu

mkdir -p /etc/ssh
cp /secrets/demo-host-key /etc/ssh/ssh_host_ed25519_key
cp /secrets/demo-host-key.pub /etc/ssh/ssh_host_ed25519_key.pub
chmod 0600 /etc/ssh/ssh_host_ed25519_key
cp /secrets/ssh-ca-key.pub /etc/ssh/beacon_ca.pub
chmod 0644 /etc/ssh/beacon_ca.pub

cat > /etc/ssh/sshd_config <<'EOF'
Port 22
HostKey /etc/ssh/ssh_host_ed25519_key
TrustedUserCAKeys /etc/ssh/beacon_ca.pub
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
AllowUsers beacon
Subsystem sftp internal-sftp
LogLevel INFO
EOF

/usr/local/bin/start-web
exec /usr/sbin/sshd -D -e
