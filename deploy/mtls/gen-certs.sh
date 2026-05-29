#!/usr/bin/env bash
# Generate a development mTLS trust chain: a private CA, a server cert for the
# edge (Caddy), and a client cert for high-assurance API clients.
#
# DEV ONLY. Do not use these certs in production — use a real PKI / cert-manager.
# Generated key material is written to ./out and is gitignored. Never commit keys.
#
# Usage:
#   ./gen-certs.sh [--out DIR] [--cn DOMAIN] [--client NAME] [--days N]
#
set -euo pipefail

OUT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/out"
CN="ubag.example.com"
CLIENT="ubag-client"
DAYS="365"

while [ $# -gt 0 ]; do
  case "$1" in
    --out)    OUT="${2:?}"; shift 2 ;;
    --cn)     CN="${2:?}"; shift 2 ;;
    --client) CLIENT="${2:?}"; shift 2 ;;
    --days)   DAYS="${2:?}"; shift 2 ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 1 ;;
  esac
done

command -v openssl >/dev/null 2>&1 || { echo "openssl is required" >&2; exit 1; }

mkdir -p "$OUT"
umask 077

echo "[mtls] generating CA"
openssl genrsa -out "$OUT/ca.key" 4096
openssl req -x509 -new -nodes -key "$OUT/ca.key" -sha256 -days "$((DAYS * 3))" \
  -subj "/CN=UBAG Dev Root CA/O=UBAG" -out "$OUT/ca.crt"

echo "[mtls] generating server cert for CN=${CN}"
openssl genrsa -out "$OUT/server.key" 2048
openssl req -new -key "$OUT/server.key" -subj "/CN=${CN}/O=UBAG" -out "$OUT/server.csr"
cat > "$OUT/server.ext" <<EOF
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:${CN},DNS:localhost,IP:127.0.0.1
EOF
openssl x509 -req -in "$OUT/server.csr" -CA "$OUT/ca.crt" -CAkey "$OUT/ca.key" \
  -CAcreateserial -days "$DAYS" -sha256 -extfile "$OUT/server.ext" -out "$OUT/server.crt"

echo "[mtls] generating client cert CN=${CLIENT}"
openssl genrsa -out "$OUT/client.key" 2048
openssl req -new -key "$OUT/client.key" -subj "/CN=${CLIENT}/O=UBAG" -out "$OUT/client.csr"
cat > "$OUT/client.ext" <<EOF
basicConstraints=CA:FALSE
keyUsage=digitalSignature
extendedKeyUsage=clientAuth
EOF
openssl x509 -req -in "$OUT/client.csr" -CA "$OUT/ca.crt" -CAkey "$OUT/ca.key" \
  -CAcreateserial -days "$DAYS" -sha256 -extfile "$OUT/client.ext" -out "$OUT/client.crt"

# Convenience client bundle (PKCS#12) for browsers/clients that want it.
openssl pkcs12 -export -inkey "$OUT/client.key" -in "$OUT/client.crt" \
  -certfile "$OUT/ca.crt" -passout pass: -out "$OUT/client.p12"

rm -f "$OUT"/*.csr "$OUT"/*.ext "$OUT"/*.srl

echo "[mtls] done. Files in: $OUT"
echo "  ca.crt           trust anchor (give to clients + Caddy client_auth)"
echo "  server.crt/key   edge (Caddy) server cert"
echo "  client.crt/key   API client cert (test with curl --cert/--key)"
echo "  client.p12       client bundle (empty password)"
echo
echo "Test against an mTLS edge:"
echo "  curl --cacert $OUT/ca.crt --cert $OUT/client.crt --key $OUT/client.key https://${CN}/v1/health"
