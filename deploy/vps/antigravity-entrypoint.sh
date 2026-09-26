#!/bin/sh
set -eu

umask 007
dbus-daemon --session --address="$DBUS_SESSION_BUS_ADDRESS" --fork --nopidfile --print-address=3 3>/dev/null
gnome-keyring-daemon --start --components=secrets >/dev/null
exec python -m ubag_antigravity_cli_adapter.account_server