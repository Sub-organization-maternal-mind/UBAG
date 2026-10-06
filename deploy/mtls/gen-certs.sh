#!/usr/bin/env bash
# Generate a development mTLS trust chain: a private CA, a server cert for the
# edge (Caddy), and a client cert for high-assurance API clients. With --node,
# issue a Helper Node certificate instead (see "Helper Node certificates" in
# README.md).
#
# DEV ONLY. Do not use these certs in production — use a real PKI / cert-manager
# (for Helper Nodes the production CA belongs to the fleet manager).
# Generated key material is written to ./out and is gitignored. Never commit keys.
#
# Usage:
#   ./gen-certs.sh [--out DIR] [--cn DOMAIN] [--client NAME] [--days N]
#   ./gen-certs.sh --node NODE_ID [--out DIR] [--node-days 1..3] [--label NAME]
#
set -euo pipefail

OUT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/out"
CN="ubag.example.com"
CLIENT="ubag-client"
DAYS="365"
NODE=""
NODE_DAYS="3"
LABEL=""

while [ $# -gt 0 ]; do
  case "$1" in
    --out)       OUT="${2:?}"; shift 2 ;;
    --cn)        CN="${2:?}"; shift 2 ;;
    --client)    CLIENT="${2:?}"; shift 2 ;;
    --days)      DAYS="${2:?}"; shift 2 ;;
    --node)      NODE="${2:?}"; shift 2 ;;
    --node-days) NODE_DAYS="${2:?}"; shift 2 ;;
    --label)     LABEL="${2:?}"; shift 2 ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 1 ;;
  esac
done

command -v openssl >/dev/null 2>&1 || { echo "openssl is required" >&2; exit 1; }

mkdir -p "$OUT"
umask 077

make_ca() {
  echo "[mtls] generating CA"
  openssl genrsa -out "$OUT/ca.key" 4096
  openssl req -x509 -new -nodes -key "$OUT/ca.key" -sha256 -days "$((DAYS * 3))" \
    -subj "/CN=UBAG Dev Root CA/O=UBAG" -out "$OUT/ca.crt"
}

# Helper Node certificate: URI SAN spiffe://ubag/node/<id> is the node identity
# (UBAG ignores the CN), at most 72h of validity (decision D3), clientAuth +
# serverAuth, EC P-256 key. The CA in $OUT is reused (created if missing) so a
# rotation issues a second certificate under the same trust anchor.
issue_node() {
  case "$NODE" in
    [A-Za-z0-9]*) ;;
    *) echo "--node must start with a letter or digit" >&2; exit 1 ;;
  esac
  if [ "${#NODE}" -gt 64 ] || printf '%s' "$NODE" | grep -q '[^A-Za-z0-9._-]'; then
    echo "--node must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}\$" >&2; exit 1
  fi
  case "$NODE_DAYS" in
    1|2|3) ;;
    *) echo "--node-days must be 1, 2 or 3 (certificates live at most 72h)" >&2; exit 1 ;;
  esac
  if [ -n "$LABEL" ] && printf '%s' "$LABEL" | grep -q '[^A-Za-z0-9_-]'; then
    echo "--label may contain only letters, digits, - and _" >&2; exit 1
  fi
  [ -f "$OUT/ca.crt" ] && [ -f "$OUT/ca.key" ] || make_ca

  base="$OUT/node-${NODE}${LABEL:+.$LABEL}"
  uri="spiffe://ubag/node/${NODE}"
  echo "[mtls] issuing Helper Node cert ${uri} (${NODE_DAYS} day(s))"
  openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$base.key"
  openssl req -new -key "$base.key" -subj "/CN=${NODE}/O=UBAG" -out "$base.csr"
  cat > "$base.ext" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=clientAuth,serverAuth
subjectAltName=URI:${uri},DNS:localhost,IP:127.0.0.1
EOF
  openssl x509 -req -in "$base.csr" -CA "$OUT/ca.crt" -CAkey "$OUT/ca.key" \
    -CAcreateserial -days "$NODE_DAYS" -sha256 -extfile "$base.ext" -out "$base.crt"
  openssl verify -CAfile "$OUT/ca.crt" "$base.crt" >/dev/null

  # SPKI pin: SHA-256 of the DER SubjectPublicKeyInfo, lowercase hex. Computed
  # from the key (not piped through the cert) so it is binary-safe everywhere.
  openssl pkey -in "$base.key" -pubout -outform DER -out "$base.pub.der"
  pin="$(openssl dgst -sha256 -r "$base.pub.der" | awk '{print $1}')"
  printf '%s\n' "$pin" > "$base.spki.sha256"
  rm -f "$base.csr" "$base.ext" "$base.pub.der" "$OUT"/*.srl

  echo "[mtls] done."
  echo "  cert       $base.crt"
  echo "  key        $base.key"
  echo "  CA         $OUT/ca.crt   (the helper plane's UBAG_HELPER_CA_FILE)"
  echo "  uri_san    ${uri}"
  echo "  not_after  $(openssl x509 -in "$base.crt" -noout -enddate | cut -d= -f2)"
  echo "  spki_sha256=${pin}"
  echo "Register the pin as the node's current (first issue) or next (rotation) SPKI."
}

if [ -n "$NODE" ]; then
  issue_node
  exit 0
fi

make_ca

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
