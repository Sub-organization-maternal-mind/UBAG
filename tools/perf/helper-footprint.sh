#!/usr/bin/env bash
# perf-fleet P0.16: real-Chrome + Python worker footprint under helper cgroup ceilings.
# Linux Docker host only. Never run against the shared VPS. Numbers from a laptop are NON-AUTHORITATIVE.
# Usage: tools/perf/helper-footprint.sh [-p 2c4g|4c8g|both] [-s SECONDS] [-n SLOTS] [-o OUTDIR]
# Env: UBAG_FOOTPRINT_IMAGE (default ubag-browser-viewer:footprint; build from deploy/small/browser-viewer/Dockerfile
#      with the repo root as context). Image needs google-chrome-stable + python3.
# Safe-mode: Chrome opens about:blank with a throwaway profile; no provider page, no login, no typing.
set -euo pipefail

PROFILE=both SECS=60 SLOTS=2 OUT=./footprint-out
while getopts p:s:n:o: o; do case $o in p) PROFILE=$OPTARG;; s) SECS=$OPTARG;; n) SLOTS=$OPTARG;; o) OUT=$OPTARG;; *) exit 2;; esac; done
IMAGE=${UBAG_FOOTPRINT_IMAGE:-ubag-browser-viewer:footprint}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)

[ "$(uname -s)" = Linux ] || { echo "SKIP: Linux Docker host required" >&2; exit 0; }
command -v docker >/dev/null && docker info >/dev/null 2>&1 || { echo "SKIP: docker unavailable" >&2; exit 0; }
mkdir -p "$OUT"

# Inside the container: start Chrome (about:blank, CDP on loopback), run the stub worker slots, sample cgroup v2.
INNER='
set -u
cg=/sys/fs/cgroup
google-chrome-stable --headless=new --no-sandbox --disable-gpu --remote-debugging-address=127.0.0.1 \
  --remote-debugging-port=9222 --user-data-dir=$(mktemp -d) about:blank >/dev/null 2>&1 &
sleep 5
python3 /repo/apps/worker/tests/bench/measure_slots.py --slots "$SLOTS" --json >/tmp/slots.json 2>&1 &
echo "t,mem_current,mem_peak,cpu_usec,nr_throttled,throttled_usec"
for t in $(seq 1 "$SECS"); do
  th=$(awk "/^nr_throttled/{n=\$2}/^throttled_usec/{u=\$2}END{print n\",\"u}" $cg/cpu.stat)
  echo "$t,$(cat $cg/memory.current),$(cat $cg/memory.peak 2>/dev/null || echo n/a),$(awk "/^usage_usec/{print \$2}" $cg/cpu.stat),$th"
  sleep 1
done
echo "oom: $(grep -E "^oom_kill " $cg/memory.events)" >&2
'

run() { # name cpus mem
  echo "== $1 (--cpus=$2 --memory=$3) =="
  docker run --rm --cpus="$2" --memory="$3" --memory-swap="$3" -v "$ROOT:/repo:ro" \
    -e SECS="$SECS" -e SLOTS="$SLOTS" "$IMAGE" bash -c "$INNER" >"$OUT/$1.csv" 2>"$OUT/$1.err" || echo "run failed, see $OUT/$1.err" >&2
  awk -F, 'NR>1&&$2>m{m=$2}NR>1{n=$5;u=$6}END{printf "peak_mem_current=%.0f MiB nr_throttled=%s throttled_usec=%s\n",m/1048576,n,u}' "$OUT/$1.csv"
  grep -h oom "$OUT/$1.err" || true
}

case $PROFILE in 2c4g|both) run 2c4g 1.5 2560m;; esac
case $PROFILE in 4c8g|both) run 4c8g 3 5g;; esac
echo "CSV/err in $OUT. See docs/perf-fleet/slices/P0.16.md for thresholds."
