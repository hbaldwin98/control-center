#!/bin/sh
set -eu

DATA="${CC_DATA_DIR:-/data}"
mkdir -p "$DATA/tls"

if [ -z "${CC_MASTER_KEY:-}" ]; then
	if [ -f "$DATA/master.key" ]; then
		CC_MASTER_KEY="$(cat "$DATA/master.key")"
	else
		CC_MASTER_KEY="$(openssl rand -hex 32)"
		umask 077
		printf '%s' "$CC_MASTER_KEY" > "$DATA/master.key"
		echo "control-center: generated master key at $DATA/master.key (volume)"
	fi
	export CC_MASTER_KEY
fi

if [ -z "${CC_BOOTSTRAP_PASSWORD:-}" ]; then
	if [ -f "$DATA/admin.password" ]; then
		CC_BOOTSTRAP_PASSWORD="$(cat "$DATA/admin.password")"
	else
		CC_BOOTSTRAP_PASSWORD="$(openssl rand -base64 18 | tr -d '\n')"
		umask 077
		printf '%s' "$CC_BOOTSTRAP_PASSWORD" > "$DATA/admin.password"
		echo "control-center: first-run admin password written to $DATA/admin.password"
		echo "control-center: log in at https://localhost:8443 with that password"
	fi
	export CC_BOOTSTRAP_PASSWORD
fi

if [ -z "${CC_TLS_CERT:-}" ] || [ -z "${CC_TLS_KEY:-}" ]; then
	if [ ! -f "$DATA/tls/cert.pem" ] || [ ! -f "$DATA/tls/key.pem" ]; then
		openssl req -x509 -newkey rsa:2048 -sha256 -nodes -days 825 \
			-subj "/CN=localhost" \
			-addext "subjectAltName=DNS:localhost,IP:127.0.0.1" \
			-keyout "$DATA/tls/key.pem" \
			-out "$DATA/tls/cert.pem"
		echo "control-center: generated self-signed TLS cert for localhost"
	fi
	export CC_TLS_CERT="$DATA/tls/cert.pem"
	export CC_TLS_KEY="$DATA/tls/key.pem"
fi

export CC_ADDR="${CC_ADDR:-0.0.0.0:8443}"
export CC_DATA_DIR="$DATA"
export CC_ORIGINS="${CC_ORIGINS:-https://localhost:8443}"

exec /usr/local/bin/controlcenter \
	--config /app/config/config.yaml \
	--static /app/web
