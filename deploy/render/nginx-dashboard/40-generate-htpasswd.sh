#!/bin/sh
# Generates /etc/nginx/.htpasswd from env vars at container start. Render has
# no persistent local file to bind-mount the way deploy/small's
# set-operator-password.sh does — this runs on every boot instead, so the
# only durable copy of the credential is whatever you set as the Render env
# var (rotate it there, not by editing a file in the running container).
set -eu

: "${UBAG_OPERATOR_USER:?Set UBAG_OPERATOR_USER}"
: "${UBAG_OPERATOR_PASSWORD:?Set UBAG_OPERATOR_PASSWORD}"

hash=$(openssl passwd -apr1 "$UBAG_OPERATOR_PASSWORD")
echo "${UBAG_OPERATOR_USER}:${hash}" > /etc/nginx/.htpasswd
