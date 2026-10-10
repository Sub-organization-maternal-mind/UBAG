#!/bin/bash
# Provision only UBAG's primary tunnel and its bounded fleet peer sync timer.
set -euo pipefail
test "$(id -u)" = 0
command -v wg >/dev/null || { apt-get update -qq; apt-get install -y wireguard-tools; }
install -d -m 700 /etc/wireguard
umask 077
if [ ! -s /etc/wireguard/ubagwg.key ]; then wg genkey > /etc/wireguard/ubagwg.key; fi
if [ ! -s /etc/wireguard/ubagwg.conf ]; then
    { printf '[Interface]\nAddress = 10.253.240.1/24\nListenPort = 51821\nPrivateKey = '; cat /etc/wireguard/ubagwg.key; printf '\n'; } > /etc/wireguard/ubagwg.conf
fi
install -m 755 "$(dirname "$0")/sync-wireguard-peers.py" /usr/local/sbin/ubag-sync-wireguard-peers
cat > /usr/local/sbin/ubag-wireguard-firewall <<'FIREWALL'
#!/bin/sh
set -eu
iptables -C INPUT -i ubagwg -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT 2>/dev/null || iptables -I INPUT -i ubagwg -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -C INPUT -i ubagwg -p icmp -j ACCEPT 2>/dev/null || iptables -A INPUT -i ubagwg -p icmp -j ACCEPT
iptables -C INPUT -i ubagwg -j DROP 2>/dev/null || iptables -A INPUT -i ubagwg -j DROP
# Published Docker ports traverse FORWARD, rather than INPUT.
iptables -C DOCKER-USER -i ubagwg -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT 2>/dev/null || iptables -I DOCKER-USER -i ubagwg -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -C DOCKER-USER -i ubagwg -j DROP 2>/dev/null || iptables -A DOCKER-USER -i ubagwg -j DROP
FIREWALL
chmod 755 /usr/local/sbin/ubag-wireguard-firewall
cat > /etc/systemd/system/ubag-wireguard-peers.service <<'UNIT'
[Unit]
After=wg-quick@ubagwg.service
Requires=wg-quick@ubagwg.service
[Service]
Type=oneshot
ExecStart=/usr/local/sbin/ubag-sync-wireguard-peers
ExecStartPre=/usr/local/sbin/ubag-wireguard-firewall
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
UNIT
cat > /etc/systemd/system/ubag-wireguard-peers.timer <<'UNIT'
[Unit]
Description=Reconcile UBAG private helper peers
[Timer]
OnBootSec=10s
OnUnitActiveSec=30s
[Install]
WantedBy=timers.target
UNIT
systemctl daemon-reload
systemctl enable --now wg-quick@ubagwg ubag-wireguard-peers.timer
# Allow only replies and ICMP on the primary host. Helper RPC is initiated by
# the gateway; the helper never receives access to primary SSH or databases.
/usr/local/sbin/ubag-wireguard-firewall
printf 'primary_public_key='; wg pubkey < /etc/wireguard/ubagwg.key
