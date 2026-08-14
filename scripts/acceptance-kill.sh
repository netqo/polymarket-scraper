#!/usr/bin/env bash
#
# The output path must never be observed truncated.
#
# A consumer may read the file the instant the process exits, and a half-written
# document there is worse than no document at all, because it looks like data.
# The write is staged and renamed for exactly this reason, and this script is
# what checks the claim rather than trusting it: it kills the process with
# SIGKILL at a spread of moments through a run, and after each one asserts that
# the path holds either the previous run's document or nothing, never a fragment
# of the new one.
#
# SIGKILL specifically, because SIGTERM is handled: the program shuts down
# cleanly and writes an honest document. The uncatchable signal is the one that
# tests the filesystem-level guarantee.
#
# Usage:
#   make acceptance-kill
#   BINARY=./result-bin/bin/polymarket-scraper ITERATIONS=40 scripts/acceptance-kill.sh

set -euo pipefail

BINARY="${BINARY:-./result-bin/bin/polymarket-scraper}"
ITERATIONS="${ITERATIONS:-30}"

if [ ! -x "$BINARY" ]; then
    echo "no binary at $BINARY; run 'nix build .#default -o result-bin' first" >&2
    exit 1
fi

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

tokens="$workdir/tokens.txt"
out="$workdir/books.json"

# A token list large enough that building the document takes a moment, which is
# what gives the kill a chance to land during the write rather than only around
# it.
for i in $(seq 1 400); do
    echo "$i" >> "$tokens"
done

# The endpoint is deliberately unreachable. Every token fails, the run still
# produces a complete document, and no network is involved. What is under test
# is the write, not the collection.
run_args=(
    --tokens "$tokens"
    --out "$out"
    --rest-only
    --rest-url "http://127.0.0.1:1"
    --duration 2
    --grace 3
    --log-level error
)

# Seed a known-good document, so every iteration also checks the stronger
# property: a killed run must leave the previous one intact. Timing it also
# tells us where the write actually happens, which is the only part of the run
# worth aiming at: kills that all land before it would prove nothing and look
# exactly like a pass.
started="$(date +%s%N)"
"$BINARY" "${run_args[@]}" >/dev/null 2>&1
finished="$(date +%s%N)"
baseline="$(cat "$out")"

run_seconds="$(awk -v a="$started" -v b="$finished" 'BEGIN { printf "%.3f", (b - a) / 1000000000 }')"
# Past the end of the run, so the last few iterations confirm that a completed
# write does replace the document rather than the check silently only ever
# testing the before-the-write case.
max_delay="$(awk -v r="$run_seconds" 'BEGIN { printf "%.3f", r * 1.15 }')"

echo "a full run takes ${run_seconds}s; spreading $ITERATIONS kills across 0 to ${max_delay}s"

failures=0
replaced=0
intact=0

for i in $(seq 1 "$ITERATIONS"); do
    # Spread the kill across the run, so successive iterations land before,
    # during and after the write rather than all in the same place.
    delay="$(awk -v i="$i" -v n="$ITERATIONS" -v m="$max_delay" 'BEGIN { printf "%.3f", (i / n) * m }')"

    "$BINARY" "${run_args[@]}" >/dev/null 2>&1 &
    pid=$!

    sleep "$delay"
    kill -9 "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true

    if [ ! -f "$out" ]; then
        echo "  [$i] FAIL: the output path vanished; the previous document was destroyed" >&2
        failures=$((failures + 1))
        continue
    fi

    if ! jq empty "$out" >/dev/null 2>&1; then
        echo "  [$i] FAIL after ${delay}s: the output path holds invalid JSON" >&2
        head -c 200 "$out" >&2
        echo >&2
        failures=$((failures + 1))
        continue
    fi

    if [ "$(cat "$out")" = "$baseline" ]; then
        intact=$((intact + 1))
    else
        replaced=$((replaced + 1))
    fi

    # Nothing may be left behind either: a retry after a kill has to need no
    # cleanup, so a staging file surviving is a failure of its own.
    leftovers="$(find "$workdir" -name 'books.json.*' -print -quit)"
    if [ -n "$leftovers" ]; then
        echo "  [$i] FAIL: a staging file was left behind: $leftovers" >&2
        rm -f "$workdir"/books.json.*
        failures=$((failures + 1))
    fi
done

echo
echo "previous document intact: $intact"
echo "replaced by a complete new one: $replaced"
echo "failures: $failures"

if [ "$failures" -ne 0 ]; then
    echo "FAILED" >&2
    exit 1
fi

# If no kill ever landed after a completed write, the check never exercised the
# case it exists for and a pass would be meaningless.
if [ "$replaced" -eq 0 ]; then
    echo "INCONCLUSIVE: no kill landed after a completed write, so the interesting case went untested" >&2
    echo "raise ITERATIONS, or the run is finishing faster than the kills can be timed" >&2
    exit 1
fi

echo "PASSED: the output path was always either the previous document or a complete new one"
