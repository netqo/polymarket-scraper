#!/usr/bin/env bash
#
# One live run, checked against what a healthy run looks like.
#
# The offline suite proves the scraper behaves as designed. The live tests prove
# the design matches the exchange on the day they run. Neither notices the
# exchange changing its behaviour next Tuesday, which is the failure that has
# cost this project the most time.
#
# So this exists to be run repeatedly, by hand or on a timer, and to say nothing
# at all unless something deviates. It is anomaly detection against a measured
# baseline, not a test: it catches "something changed", and the 719 offline tests
# catch "the logic is wrong".
#
# The baseline is not invented. It comes from a flag census over three minutes
# against 40 live tokens on 2026-08-22, recorded alongside the timing and
# keepalive measurements:
#
#   duplicate_delta_dropped   36 of 40 tokens   the feed resends deltas constantly
#   best_bid_ask_mismatch      4 of 40          the published quote is sometimes stale
#   discovered_mid_window      2                announcements being picked up
#   snapshot_only              varies           an illiquid token that never ticked
#   everything else            silent
#
# Anything outside that set is worth a person looking. In particular delta_gap,
# decode_error, disconnected or any resync_failed means the run lost trust in a
# book, which is the thing the whole project exists to notice.
#
# Usage:
#   make soak
#   DURATION=300 scripts/soak.sh
#   POLYMARKET_LIVE_TOKENS=tokens.txt scripts/soak.sh
#
# Exit status:
#   0  the run matched the baseline
#   1  the run deviated, and the deviation is printed
#   2  the run could not be attempted at all

set -euo pipefail

DURATION="${DURATION:-120}"
GRACE="${GRACE:-20}"
TOKEN_COUNT="${TOKEN_COUNT:-40}"
BINARY="${BINARY:-}"

# Flags a healthy run is allowed to raise. Overridable, because "healthy" for a
# quiet Sunday is not "healthy" for a market open, and pretending otherwise
# would train whoever reads this to ignore it.
EXPECTED_FLAGS="${EXPECTED_FLAGS:-duplicate_delta_dropped best_bid_ask_mismatch discovered_mid_window snapshot_only}"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

tokens="$workdir/tokens.txt"
out="$workdir/books.json"
stream="$workdir/changes.jsonl"
log="$workdir/run.log"

for tool in jq curl; do
    command -v "$tool" >/dev/null || { echo "soak: $tool is required" >&2; exit 2; }
done

# A binary rather than 'go run', so that what is soaked is what would ship.
if [ -z "$BINARY" ]; then
    BINARY="$workdir/polymarket-scraper"
    go build -o "$BINARY" ./cmd/polymarket-scraper || { echo "soak: build failed" >&2; exit 2; }
fi

# Fresh token ids unless the operator supplied a list. Fresh is usually what is
# wanted: a stale list drifts towards resolved markets, and a run full of dead
# tokens would look like a scraper problem.
if [ -n "${POLYMARKET_LIVE_TOKENS:-}" ]; then
    cp "$POLYMARKET_LIVE_TOKENS" "$tokens"
else
    echo "fetching $TOKEN_COUNT live token ids"
    curl -sS --max-time 30 'https://clob.polymarket.com/sampling-simplified-markets' \
        | jq -r --argjson n "$TOKEN_COUNT" \
            '[(.data // .)[] | .tokens[]? | .token_id | select(. != null)] | .[:$n] | .[]' \
        > "$tokens" || { echo "soak: could not fetch token ids" >&2; exit 2; }
fi

requested="$(grep -c . "$tokens" || true)"
if [ "$requested" -eq 0 ]; then
    echo "soak: no token ids to collect" >&2
    exit 2
fi

echo "collecting $requested tokens for ${DURATION}s"

started="$(date +%s)"
set +e
"$BINARY" --tokens "$tokens" --out "$out" --stream "$stream" --log-file "$log" \
    --duration "$DURATION" --grace "$GRACE" --log-level warn >/dev/null 2>&1
code=$?
set -e
elapsed=$(( $(date +%s) - started ))

deviations=()
note() { deviations+=("$1"); }

# 1. The run has to have finished on its own terms. Exit 3 is the watchdog,
#    which means it could not shut down and produced no document at all.
if [ "$code" -ne 0 ]; then
    note "exited $code (0 is the only healthy code; 3 means the watchdog had to terminate it)"
fi

# 2. Within its budget. Overrunning is the guarantee the whole shutdown timeline
#    exists to provide.
budget=$(( DURATION + GRACE ))
if [ "$elapsed" -gt "$budget" ]; then
    note "took ${elapsed}s against a ${budget}s budget"
fi

if [ ! -f "$out" ]; then
    note "no output document was written"
    printf '%s\n' "${deviations[@]}" >&2
    exit 1
fi
if ! jq empty "$out" >/dev/null 2>&1; then
    note "the output document is not valid JSON"
    printf '%s\n' "${deviations[@]}" >&2
    exit 1
fi

ok="$(jq -r '.tokens_ok' "$out")"
total="$(jq -r '.tokens_requested' "$out")"
errors="$(jq -r '.errors | length' "$out")"
reconnects="$(jq -r '.connection.reconnects' "$out")"
resyncs="$(jq -r '.connection.rest_resyncs' "$out")"
connections="$(jq -r '.connection.ws_connections' "$out")"
discovered="$(jq -r '.tokens_discovered' "$out")"

# 3. Every requested token current. Anything less means a book was lost.
if [ "$ok" -ne "$total" ]; then
    note "$ok/$total tokens ok"
    jq -r '.books | to_entries[] | select(.value.status != "ok")
           | "    \(.key) -> \(.value.status) \(.value.flags)"' "$out" | head -10 >&2
fi

# 4. The error list is meant to be empty on a healthy run. Repeats are collapsed
#    with a count, so one entry can still mean thousands of occurrences.
if [ "$errors" -ne 0 ]; then
    note "$errors error message(s) in the document"
    jq -r '.errors[] | "    \(.)"' "$out" | head -10 >&2
fi

# 5. A reconnect is not fatal, but it means every token on that connection lost
#    trust and had to be re-seeded. On a good network it should not happen.
if [ "$reconnects" -ne 0 ]; then
    note "$reconnects reconnect(s)"
fi

# 6. Flags outside the measured baseline.
if [ -f "$stream" ]; then
    unexpected="$(jq -r 'select(.kind == "flag") | .flag' "$stream" 2>/dev/null \
        | sort -u \
        | grep -vxF -f <(printf '%s\n' $EXPECTED_FLAGS) || true)"
    if [ -n "$unexpected" ]; then
        note "flags outside the baseline: $(echo "$unexpected" | tr '\n' ' ')"
        while read -r flag; do
            [ -n "$flag" ] || continue
            count="$(jq -r --arg f "$flag" 'select(.kind == "flag" and .flag == $f) | .token' "$stream" | sort -u | wc -l)"
            echo "    $flag on $count token(s)" >&2
        done <<< "$unexpected"
    fi

    # 7. Every line of the stream has to parse on its own. That is the whole
    #    reason for the format, and a run that exited cleanly should leave no
    #    partial line behind.
    if ! jq empty "$stream" >/dev/null 2>&1; then
        note "the change stream contains a line that does not parse"
    fi

    # 8. A token that is ok was seeded, and being seeded reports a quote. No
    #    quotes at all means the stream is not carrying what it claims to.
    quotes="$(jq -r 'select(.kind == "quote") | .token' "$stream" 2>/dev/null | sort -u | wc -l)"
    if [ "$ok" -gt 0 ] && [ "$quotes" -eq 0 ]; then
        note "$ok tokens are ok but the stream carries no quotes for any of them"
    fi
else
    note "no change stream was written"
fi

summary="tokens=$ok/$total discovered=$discovered connections=$connections reconnects=$reconnects resyncs=$resyncs errors=$errors elapsed=${elapsed}s"

if [ "${#deviations[@]}" -eq 0 ]; then
    echo "OK  $summary"
    exit 0
fi

echo >&2
echo "DEVIATED  $summary" >&2
for deviation in "${deviations[@]}"; do
    echo "  - $deviation" >&2
done
echo >&2
echo "the run's own log is the next thing to read; re-run with LOG_LEVEL=debug to name the token behind every flag" >&2

exit 1
