#!/usr/bin/env bash
# Builds and serves the dashboard's real production output locally. `vite
# preview` does NOT serve a SvelteKit adapter-static build (see
# scripts/serve-static.mjs) so this always builds first, then serves dist/
# with the stdlib static server. Not part of the build/test pipeline — a
# convenience script for local development only.
#
# Usage: bash apps/dashboard/scripts/run-local.sh
# Settings (gateway URL, app secret) are stored in the browser's localStorage,
# not on disk here, so they persist across restarts as long as the browser
# profile is not cleared.

set -euo pipefail
cd "$(dirname "$0")/.."

echo "Building dashboard..."
pnpm build

: "${PORT:=4179}"
export PORT

echo "Serving dist/ at http://localhost:${PORT}"
exec node scripts/serve-static.mjs
