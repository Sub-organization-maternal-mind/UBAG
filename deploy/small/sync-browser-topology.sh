#!/bin/sh
set -eu

interval="${UBAG_TOPOLOGY_SYNC_INTERVAL_SECONDS:-60}"

case "$interval" in
  ''|*[!0-9]*)
    interval=60
    ;;
esac

if [ "$interval" -lt 15 ]; then
  interval=15
fi

echo "browser topology sync started; interval=${interval}s"

while :; do
  if ! /scripts/register-browser-topology.sh; then
    echo "browser topology sync failed; retrying in ${interval}s" >&2
  fi
  sleep "$interval"
done
