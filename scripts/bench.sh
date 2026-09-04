#!/usr/bin/env bash
#
# Runs one load test the same way every time and writes the result to
# benchmarks/. See docs/04-load-testing.md for why each part is here.
#
#   scripts/bench.sh <output-name> <label> [hey arguments...]
#
# Example:
#   scripts/bench.sh 04-baseline "redirect, existing link" http://localhost:8080/seed250000

set -euo pipefail

REPS=${REPS:-5}          # how many times to repeat the run
DURATION=${DURATION:-10s}
CONCURRENCY=${CONCURRENCY:-50}
WARMUP=${WARMUP:-3s}
DRAIN=${DRAIN:-30}       # seconds between runs, so TIME_WAIT sockets expire
HOST=${HOST:-http://localhost:8080}

if [ $# -lt 3 ]; then
    sed -n '3,10p' "$0"
    exit 1
fi

NAME="$1"; LABEL="$2"; shift 2
OUT="benchmarks/${NAME}.txt"
RAW=$(mktemp -d)
trap 'rm -rf "$RAW"' EXIT

if ! curl -sf "$HOST/healthz" > /dev/null; then
    echo "no server answering at $HOST" >&2
    exit 1
fi

echo "warming up for $WARMUP"
hey -z "$WARMUP" -c "$CONCURRENCY" -disable-redirects "$@" > /dev/null 2>&1

for i in $(seq 1 "$REPS"); do
    echo "run $i of $REPS"
    hey -z "$DURATION" -c "$CONCURRENCY" -disable-redirects "$@" > "$RAW/run-$i.txt" 2>&1
    if [ "$i" -lt "$REPS" ]; then sleep "$DRAIN"; fi
done

# One number per run, so the spread between runs is visible.
rps=$(grep -h 'Requests/sec' "$RAW"/run-*.txt | awk '{printf "%.0f\n", $2}')
p99=$(grep -h '99%' "$RAW"/run-*.txt | awk '{printf "%.4f\n", $3}')
med() { printf '%s\n' "$1" | sort -n | awk '{a[NR]=$0} END{print a[int((NR+1)/2)]}'; }
lo()  { printf '%s\n' "$1" | sort -n | head -1; }
hi()  { printf '%s\n' "$1" | sort -n | tail -1; }

{
    echo "$LABEL"
    echo
    echo "Commit:   $(git rev-parse --short HEAD)$(git diff --quiet || echo ' (working tree has uncommitted changes)')"
    echo "Date:     $(date '+%Y-%m-%d %H:%M %Z')"
    echo "Machine:  $(sysctl -n machdep.cpu.brand_string), $(sysctl -n hw.ncpu) cores, $(sw_vers -productName) $(sw_vers -productVersion)"
    echo "Go:       $(go version | awk '{print $3}')"
    echo "Postgres: $(psql -d shortener -tAc 'show server_version' 2>/dev/null || echo 'n/a')"
    echo "Rows:     $(psql -d shortener -tAc 'select count(*) from links' 2>/dev/null || echo 'n/a')"
    echo
    echo "Method:   $WARMUP warm-up, then $REPS runs of $DURATION at $CONCURRENCY concurrent,"
    echo "          $DRAIN seconds between runs. Load generator, server and database all"
    echo "          on this one machine."
    echo "Command:  hey -z $DURATION -c $CONCURRENCY -disable-redirects $*"
    echo
    echo "Results per run:"
    echo
    printf '  %-5s %-12s %s\n' "run" "req/s" "p99"
    paste <(printf '%s\n' "$rps") <(printf '%s\n' "$p99") | awk '{printf "  %-5d %-12s %s\n", NR, $1, $2}'
    echo
    printf '  %-8s %-12s %s\n' "median" "$(med "$rps")" "$(med "$p99")"
    printf '  %-8s %-12s %s\n' "slowest" "$(lo "$rps")" "$(hi "$p99")"
    printf '  %-8s %-12s %s\n' "fastest" "$(hi "$rps")" "$(lo "$p99")"
    echo
    echo "The median is the number to quote. The spread between fastest and slowest is"
    echo "how much of a difference this machine produces on its own, so a change smaller"
    echo "than that spread has not been shown to do anything."
    echo
    for i in $(seq 1 "$REPS"); do
        echo "--------------------------- raw output, run $i ---------------------------"
        echo
        cat "$RAW/run-$i.txt"
        echo
    done
} > "$OUT"

echo
sed -n '/Results per run:/,/has not been shown/p' "$OUT"
echo "written to $OUT"
