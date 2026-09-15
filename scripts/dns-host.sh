#!/usr/bin/env bash
# Point this host's resolver at public DNS instead of the AT&T gateway.
#
#   sudo scripts/dns-host.sh
#
# The gateway at 192.168.1.254 serves a stale view of the wittick.org zone: it
# answers NXDOMAIN for the newer records (control-center, ts3) while resolving
# the older ones (dnd, ts) fine, and it ignores the zone's 1800s negative TTL,
# so the bad answer never ages out. Nothing is wrong with the server -- the app
# answers 200 through Caddy and Cloudflare the moment you resolve it elsewhere.
#
# netplan owns the interface here via 50-cloud-init.yaml, which cloud-init may
# regenerate, so this writes a higher-numbered file that wins the merge and is
# left alone. dhcp*-overrides.use-dns is the load-bearing half: without it
# systemd-resolved keeps the gateway alongside these servers and can still hand
# back the NXDOMAIN.
#
# Overridable:
#   DNS_SERVERS="1.1.1.1 8.8.8.8"   # what to use instead of the gateway
#   DNS_IFACE=enp2s0                # default: the default-route interface
#
# Rerunning is safe: it rewrites the same file and reapplies.
# To undo: sudo rm /etc/netplan/99-dns.yaml && sudo netplan apply
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "needs root: sudo scripts/dns-host.sh" >&2
  exit 1
fi

conf=/etc/netplan/99-dns.yaml
servers="${DNS_SERVERS:-1.1.1.1 8.8.8.8}"
iface="${DNS_IFACE:-$(ip -o route show default | awk '{print $5; exit}')}"
[[ -n $iface ]] || { echo "no default-route interface; set DNS_IFACE" >&2; exit 1; }

say() { printf '\n== %s\n' "$1"; }

say "checking the interface"
ip -o link show "$iface" >/dev/null || exit 1
echo "using $iface with: $servers"

say "writing $conf"
{
  echo "# managed by scripts/dns-host.sh -- edits are overwritten; rerun instead"
  echo "network:"
  echo "  version: 2"
  echo "  ethernets:"
  echo "    $iface:"
  # Drop the gateway's DHCP-supplied resolver; keep everything else from DHCP.
  echo "      dhcp4-overrides:"
  echo "        use-dns: false"
  echo "      dhcp6-overrides:"
  echo "        use-dns: false"
  echo "      nameservers:"
  echo "        addresses: [${servers// /, }]"
} >"$conf"
# netplan warns loudly about world-readable configs.
chmod 600 "$conf"
netplan generate

say "applying"
# This renews DHCP on $iface. The lease is renewed, not released, so an SSH
# session on that interface survives -- but a flaky link is worth a console.
netplan apply
sleep 2
resolvectl flush-caches

say "resolver now in use"
resolvectl status "$iface" | sed -n '/DNS Servers/,+3p'

say "verifying the names the gateway was breaking"
for n in control-center.wittick.org ts3.wittick.org ts.wittick.org dnd.wittick.org; do
  if out="$(resolvectl query "$n" 2>&1)"; then
    printf '  %-28s -> %s\n' "$n" "$(echo "$out" | awk 'NR==1{print $2}')"
  else
    printf '  %-28s -> FAILED\n' "$n"
    echo "$out" | sed 's/^/      /'
  fi
done

say "end to end"
curl -s -o /dev/null -m 15 -w '  https://control-center.wittick.org/ -> %{http_code}\n' \
  https://control-center.wittick.org/ || echo "  request failed"

say "done"
echo "undo: sudo rm $conf && sudo netplan apply"
