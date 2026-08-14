# Design

How polymarket-scraper is built and why. The conformance spec it implements is
`docs/spec/scraper-requirements.md`; requirement identifiers below (A1, B4, C6,
...) refer to that document. `SCHEMA.md` is the output contract and takes
precedence over anything written here about field names.

## 1. The problem in one paragraph

A consuming agent needs live Polymarket order books for a few hundred outcome
tokens, collected over a short window, in a form it can trust. The failure mode
that actually costs money is not a missing book, it is a **stale book presented
as fresh**: that becomes a phantom arbitrage opportunity and a real losing
trade. So the scraper's job is not to collect as much data as possible, it is to
be honest about exactly what it saw and what it failed to see. Every design
decision below follows from that.

## 2. API reality

The live API was probed directly before any code was written. Six of the
behaviours below contradict or extend the official documentation, and each would
have caused a silent correctness bug.

1. **Bids are returned ascending, asks descending** on both REST `/book` and the
   websocket `book` event, which is the exact opposite of the published
   ordering. Taking `bids[0]` as the best bid yields the worst price. The
   scraper sorts on ingest and never trusts the incoming order.
2. **The initial `book` snapshot arrives as a JSON array** holding every
   subscribed asset in a single frame. Every other event is a bare object. A
   decoder that assumes objects drops all initial state.
3. **`price_change` is a batch.** The field is `price_changes[]`, the envelope
   carries no `asset_id`, and `best_bid`/`best_ask`/`hash` ride on each element.
   One message can cover both legs of a binary market.
4. **There is a silent subscribe ceiling around 750 assets**, and it is a
   byte-size limit on the subscribe frame rather than a count. Past it the
   socket stays open and keeps delivering deltas while never sending the
   snapshot, so a naive client looks healthy with no book state at all. We shard
   well under the limit and assert that the snapshot count matches the
   subscribed count before trusting a shard.
5. **`POST /books` exists** and accepts hundreds of token ids per request,
   returning full order book summaries including `tick_size`, `min_order_size`
   and `neg_risk`. This collapses a 400-token REST fallback into one request.
6. **`PING`/`PONG` are raw uppercase text frames**, not JSON and not websocket
   protocol pings. They must be intercepted before JSON parsing.

Also relevant: the market channel takes no auth field; the websocket `book`
snapshot carries `tick_size` but never `min_order_size` or `neg_risk`, so a REST
metadata seed is mandatory rather than optional; `{"operation":"subscribe"}` is
officially supported for live subscription changes; and `new_market` is a global
feed, not filtered to your subscription, so anything that reacts to it needs a
hard capacity budget.

## 3. Interpretation decisions

Ambiguities in the spec, and how they were resolved. Each is load-bearing.

| # | Ambiguity | Decision |
|---|---|---|
| I1 | C6 wants one timestamp convention *and* verbatim passthrough | Two clearly separated classes. Scraper-generated times (`started_at`, `received_at`, ...) are ISO-8601 UTC with milliseconds and `Z`. Feed-provided times (`exchange_timestamp`) are the verbatim string the API sent. Re-formatting the feed's epoch milliseconds would be exactly the parse-and-re-serialize C6 forbids. |
| I2 | What makes `source` equal `ws+rest_resync` | Set if and only if a REST call contributed book **levels**. A reconnect re-seeded purely from a fresh websocket snapshot stays `ws` and carries the flag `delta_gap_resynced`. Incidents live in `flags`; `source` stays a clean provenance signal. |
| I3 | The websocket cannot supply `min_order_size` or `neg_risk` | A paced REST metadata seed for every token is first-class, not optional. It fills metadata and `condition_id` only, and does not change `source`. |
| I4 | D4 read literally would resync everything on one clock quirk | Per-connection delivery order is authoritative. Exact duplicates (same timestamp and hash) are dropped. Timestamp regressions trigger a resync only beyond `--reorder-tolerance`; smaller ones are flagged. Documented as a deviation in the README. |
| I5 | Whether the status enum may grow | Never. Exactly the four values from C4. A REST 404 on a malformed token is `no_data` plus the flag `token_not_found`. `flags` is the extension point, because the consuming prompt hardcodes the status set. |
| I6 | Field presence on failed tokens | Every per-token object always carries every key. Unknown values are `null`, never absent, never defaulted. No `omitempty` anywhere in the report package. This generalizes C4's own rationale that a missing key destroys the distinction the consumer needs. |
| I7 | Where dynamically discovered tokens go | Into `books` with the flag `discovered_mid_window`, plus a separate counter. `tokens_requested` stays the input count. Every requested token is still present exactly once; `books` may be a superset. |
| I8 | `--rest-only` versus the A4 deadline | The deadline becomes `max(duration, tokens / rest_rate) + grace`, documented in `--help`. Otherwise a short duration with many tokens is unsatisfiable by construction. |
| I9 | Signal handling | SIGINT/SIGTERM jumps straight to finalize-and-write with a compressed drain, and exits 0. A5's contract is "exit 0 if and only if the file is valid", and a truncated run still produces an honest file. |
| I10 | stdout policy | The summary line is printed only on success. On failure stdout is empty. This makes "stdout is non-empty" a reliable success predicate. |

## 4. Package layout

```
cmd/polymarket-scraper/   process wiring only
internal/decimal/         verbatim-decimal value type            (L0)
internal/tokenlist/       token file loading                     (L1)
internal/config/          flags, --help, shutdown budget         (L1)
internal/wire/            protocol anti-corruption layer         (L1)
internal/book/            pure sorted order book                 (L2)
internal/tracker/         per-token state machine, window stats  (L3)
internal/wsclient/        one websocket shard                    (L4)
internal/restclient/      GET /book, POST /books, rate limiting  (L4)
internal/report/          output schema, builder, atomic write   (L5)
internal/engine/          orchestration                          (L6)
internal/testsupport/     fake websocket and REST servers (test-only)
```

Strictly layered and acyclic. Each package exists because it isolates one
testable guarantee, not for tidiness. Deliberately **not** created, to avoid
fragmentation: a logging package (it is process wiring and belongs in `cmd`), an
atomic-file package (used once, lives in `report`), a metrics package (counters
are fields on the engine), and an errors package (the standard library plus
per-package sentinel values is enough).

## 5. Verbatim decimals versus numeric comparison

C6 forbids parsing API decimals into floats and re-serializing them: `"0.982"`
must come back out byte-identical, never `0.9819999999999999`. But crossed-book
detection, level sorting, delta matching and the window statistics all need
numeric comparison. These pull in opposite directions.

The resolution is one value type carrying two representations, where the
comparison form is structurally unable to reach the output:

```go
type Dec struct {
    raw   string // exactly the bytes the API sent; the ONLY thing serialized
    nano  int64  // value * 1e9, derived once; comparison and statistics ONLY
    valid bool
}
```

`MarshalJSON` emits `raw`, and the zero value marshals to `null` rather than an
empty string. `UnmarshalJSON` reads the raw source bytes, so even if Polymarket
switches a field from string to number the text is still preserved and no
float64 ever appears in the path.

Fixed-point int64 nano is exact for every value Polymarket can produce and is a
million times finer than the smallest tick. Sizes above roughly 9.2e9 overflow
to `valid=false`, which is harmless because only prices are ever compared or
sorted, and prices are bounded in [0,1]. A level whose price cannot be parsed is
still kept, because the scraper never drops data (D1); it is pushed to an
unsorted tail and the token is flagged.

Rejected alternatives: a float64 field alongside the string (correct for
ordering, but one careless struct tag silently violates C6), and a
big-decimal dependency (exact, but buys precision six orders of magnitude finer
than any tick).

## 6. Concurrency and deterministic shutdown

A4 requires the process to exit within the window plus a grace period **even on
hung connections**. Go goroutines are not killable: one blocked in a syscall
never observes context cancellation, so the only hard guarantee is a watchdog
that calls `os.Exit`. That single constraint dictates the whole model, because
it means **the output must be producible without joining any I/O goroutine**.

Therefore book state is owned per shard by a single goroutine that performs no
I/O and makes no blocking channel send. It receives from an inbox, computes, and
sends non-blockingly. It cannot wedge, so its state is always retrievable. All
wedging risk is confined to reader, writer and REST goroutines, which own no
state and whose loss costs only their in-flight message. A mutex-guarded shared
map would reintroduce exactly the hang A4 forbids, by putting state behind a
lock an I/O goroutine might be holding.

REST results re-enter through the originating shard's inbox, so the
single-writer property holds end to end. Exactly two objects are shared and
mutex-guarded, both append-only: the event log and the error sink. The error
sink is capped, with a "+N suppressed" tail, so bounded memory survives a
reconnect storm.

The shutdown timeline is a pure function of the duration and grace period,
living in `internal/config`, so it is table-testable without a single sleep:
sweep still-pending tokens before the window ends, close sockets at the
deadline, drain in-flight REST results, finalize, collect, write, exit. A shard
that fails to report by the collect deadline has all of its tokens emitted as
`resync_failed`, never as stale websocket data.

## 7. Trust, per token

A token moves from pending to live once seeded, and back to untrusted on a
connection drop, a decode failure, a large timestamp regression, or a tick-size
change arriving before any snapshot. Untrusted means untrusted: the book is not
serialized. For any status other than `ok`, the output carries empty sides and
null timestamps, and this is enforced structurally, because the level-copy
branch is unreachable unless the status is `ok`. That is D2, and it is the
requirement the whole project exists to satisfy.

The state machine returns intents rather than performing side effects, so it is
a pure function of (state, event) and gets an exhaustive transition-matrix test
with no concurrency involved. It is the highest-risk logic in the program and it
is isolated accordingly.

## 8. Output guarantees

- **Completeness (C4)** is a property of a loop, not of the pipeline succeeding:
  the builder iterates the requested token list as the authority and inserts an
  explicit `no_data` entry for anything the pipeline never produced.
- **Atomicity (C1)**: marshal in memory, write to a temporary file *in the same
  directory* (rename is only atomic within one filesystem), sync, close, re-read
  from disk and strictly decode it, rename, then sync the directory. Removing
  the temporary file on every error path means a retry needs no cleanup.
- **No invented values (D3)**: `omitempty` appears nowhere in the report
  package, enforced by a reflection test. Absent is never used; unknown is
  `null`.
- **Verbatim values (C6)** are guarded three ways: the decimal marshaller, a
  reflection test that walks the whole document type graph and fails on any
  float, and a byte-level test that runs a pathological decimal through the full
  pipeline and asserts it appears unchanged in the written file.

## 9. Dependencies

Two at runtime, one for tests, all pinned exactly.

- `github.com/coder/websocket` for context-aware reads and writes, which is
  decisive for A4, plus a hard teardown that never blocks on a close handshake.
- `golang.org/x/time/rate` for a context-respecting limiter, so pacing and
  backoff both unblock at the drain deadline.
- `github.com/google/go-cmp` for readable diffs in tests.

Rejected: a CLI framework (there are no subcommands, and the help text is
hand-written because it is the ground truth a consuming agent reads), any JSON
library other than the standard library, and any logging library.

## 10. Testing

Fake websocket and REST servers run in-process and are injected via `--ws-url`
and `--rest-url`. No test touches the network in CI. Time-dependent *logic*
takes an injected clock and is tested with no sleeps; time-dependent
*networking* uses real but compressed durations, so the suite runs in about a
second.

Roughly seventy percent of the code sits behind zero-concurrency,
zero-network table tests. The acceptance checklist in the spec maps onto
integration tests against the fakes, a `//go:build live` suite run by hand
against the real API, and one shell script that kills the process mid-write in a
loop to prove the output file is never observed truncated.
