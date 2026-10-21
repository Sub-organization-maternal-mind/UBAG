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
  # `sh <script>` rather than a bare exec: the script is bind-mounted read-only
  # and its execute bit depends on how the file was copied onto the host, so
  # invoking it through the shell works whatever the mode is.
  if ! sh /scripts/register-browser-topology.sh; then
    echo "browser topology sync failed; retrying in ${interval}s" >&2
  fi
  sleep "$interval"
done
