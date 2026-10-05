# Sourced by the other scripts. UBAG_TOKEN = app secret / PAT (keep it server-side).
: "${UBAG_TOKEN:?Set UBAG_TOKEN}"
UBAG_BASE_URL="${UBAG_BASE_URL:-http://127.0.0.1:8080}"
api() { # api METHOD PATH [curl args...]
  local method="$1" path="$2"; shift 2
  curl -sS --fail-with-body -X "$method" -H "Authorization: Bearer $UBAG_TOKEN" "$@" "$UBAG_BASE_URL$path"
}
