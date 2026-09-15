#!/usr/bin/env bash
# Install Caddy on the host as the front door, replacing the caddy container.
#
#   sudo scripts/caddy-host.sh
#
# The compose stack publishes the app on 127.0.0.1:8443 and nothing else. Caddy
# terminates TLS with ACME and proxies to it, which is what CC_TRUSTED_PROXY
# assumes -- see docs/exposure.md. Running Caddy on the host rather than in the
# stack lets it serve sites that have nothing to do with this app.
#
# Hostnames come from .env, the same gitignored file docker-compose.yml reads:
#
#   CC_DOMAIN=cc.example.com     # required; the app
#   PROXY_EXTRA="dnd.example.com=3000 wiki.example.com=8000"
#                                # optional; host=port per site, proxied to
#                                # 127.0.0.1:port over plain HTTP
#
# Ports 80/443 must be forwarded to this host at the router, and every hostname
# must already resolve here or ACME cannot issue.
#
# Rerunning is safe: it rewrites the Caddyfile from .env and reloads.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

if [[ $EUID -ne 0 ]]; then
  echo "needs root: sudo scripts/caddy-host.sh" >&2
  exit 1
fi

# Compose and docker run as the owner of the checkout, never as root -- root
# would take the docker socket and leave root-owned files in the tree.
owner="$(stat -c %U "$root")"
as_owner() { sudo -u "$owner" "$@"; }

[[ -f .env ]] || { echo ".env not found; copy the CC_DOMAIN line from the header of this script" >&2; exit 1; }
set -a; . ./.env; set +a
: "${CC_DOMAIN:?CC_DOMAIN must be set in .env}"

say() { printf '\n== %s\n' "$1"; }

say "installing Caddy"
if command -v caddy >/dev/null; then
  echo "already present: $(caddy version | head -1)"
else
  apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' |
    gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
    >/etc/apt/sources.list.d/caddy-stable.list
  apt-get update
  apt-get install -y caddy
  # The package starts it; it cannot bind 80/443 while the container holds them.
  systemctl stop caddy
fi

say "writing /etc/caddy/Caddyfile"
if [[ -f /etc/caddy/Caddyfile ]] && ! grep -q 'managed by scripts/caddy-host.sh' /etc/caddy/Caddyfile; then
  cp -a /etc/caddy/Caddyfile "/etc/caddy/Caddyfile.bak.$(date +%s)"
  echo "kept the existing Caddyfile as a .bak"
fi
{
  echo "# managed by scripts/caddy-host.sh -- edits are overwritten; change .env instead"
  echo
  echo "$CC_DOMAIN {"
  echo "	reverse_proxy https://127.0.0.1:8443 {"
  echo "		transport http {"
  # The app serves its own self-signed cert on 8443; the hop is loopback-only.
  echo "			tls_insecure_skip_verify"
  echo "		}"
  # SSE (/api/events/stream) must not be buffered.
  echo "		flush_interval -1"
  echo "	}"
  echo "}"
  for site in ${PROXY_EXTRA:-}; do
    [[ $site == *=* ]] || { echo "PROXY_EXTRA entry '$site' is not host=port" >&2; exit 1; }
    echo
    echo "${site%%=*} {"
    echo "	reverse_proxy 127.0.0.1:${site##*=}"
    echo "}"
  done
} >/etc/caddy/Caddyfile
chmod 644 /etc/caddy/Caddyfile
caddy validate --config /etc/caddy/Caddyfile

say "applying the compose stack"
# --remove-orphans drops the caddy container: it is no longer a service here.
as_owner docker compose up -d --remove-orphans

say "checking 80/443 are free"
if as_owner docker ps --filter name=caddy --format '{{.Names}}' | grep -q .; then
  echo "a caddy container is still running; it would fight the host service" >&2
  exit 1
fi
if ss -tln '( sport = :80 or sport = :443 )' | grep -q LISTEN; then
  echo "something still holds 80/443:" >&2
  ss -tlnp '( sport = :80 or sport = :443 )' >&2
  exit 1
fi

say "starting Caddy"
systemctl enable --now caddy
systemctl reload caddy 2>/dev/null || true
sleep 3
systemctl --no-pager --lines=10 status caddy || true

say "verifying"
curl -sk -o /dev/null -m 10 -w '  127.0.0.1:8443/healthz -> %{http_code}\n' https://127.0.0.1:8443/healthz || true

# Pin every hostname to the local Caddy with --resolve. Asking DNS instead would
# send the request out to the public address and back, which needs NAT hairpin
# support this router does not have: it times out even when everything works.
# What we can prove from here is Caddy's own answer; reaching it from outside is
# the router's job. No -k, so a bad or self-signed cert still fails.
probe() { curl -s -m 10 -o /dev/null -w '%{http_code}' --resolve "$1:443:127.0.0.1" "https://$1$2" || echo 000; }

code=000
# First issue takes a few seconds; give ACME a minute before calling it broken.
for _ in 1 2 3 4 5 6; do
  code="$(probe "$CC_DOMAIN" /healthz)"
  [[ $code == 200 ]] && break
  sleep 10
done
echo "  https://$CC_DOMAIN/healthz -> $code (via local Caddy)"
for site in ${PROXY_EXTRA:-}; do
  host="${site%%=*}"
  echo "  https://$host/ -> $(probe "$host" /)  (502 until ${site##*=} is listening)"
done

say "done"
echo "logs:   journalctl -u caddy -f"
echo "reload: sudo scripts/caddy-host.sh"
