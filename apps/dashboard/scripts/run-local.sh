#!/usr/bin/env bash
# Builds and serves the dashboard's real production output locally. `vite
# preview` does NOT serve a SvelteKit adapter-static build (see
# scripts/serve-static.mjs) so this always builds first, then serves dist/
# with the stdlib static server. Not part of the build/test pipeline — a
# convenience script for local development only.
#
# Usage: bash apps/dashboard/scripts/run-local.sh
#
# The gateway URL and app secret are baked into the build (see
# vite.config.ts's `define` + src/lib/stores/settings.ts) so every fresh
# browser profile, Incognito window, or cleared localStorage still opens
# already pointed at the right gateway — not left to default to the
# dashboard's own origin. localStorage still overrides this if the user
# explicitly changes it in Settings.

set -euo pipefail
cd "$(dirname "$0")/.."

: "${UBAG_DEV_DEFAULT_GATEWAY_URL:=http://127.0.0.1:58080}"
: "${UBAG_DEV_DEFAULT_APP_SECRET:=dev_local_secret_12345678}"
export UBAG_DEV_DEFAULT_GATEWAY_URL UBAG_DEV_DEFAULT_APP_SECRET

echo "Building dashboard (default gateway: ${UBAG_DEV_DEFAULT_GATEWAY_URL})..."
pnpm build

: "${PORT:=58179}"
export PORT

echo "Serving dist/ at http://localhost:${PORT}"
exec node scripts/serve-static.mjs
