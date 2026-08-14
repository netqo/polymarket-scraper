# Scraper Requirements — Polymarket Market Scout

Conformance spec for the websocket scraper the scout agent invokes in §5 of `polymarket-market-scout-prompt.md`. Each requirement is written to be testable. **MUST** = the scout breaks or produces unsafe data without it; **SHOULD** = strongly recommended for reliability; **MAY** = nice-to-have. The `(→ §…)` notes say which part of the scout prompt consumes the requirement.

The core idea: the scraper is a dumb, honest pipe. It collects raw live books for a given token list over a given window and reports exactly what it saw, including what it failed to see. All judgment (filtering, edge math, fees, scoring) belongs to Claude — the scraper's only job is to make the raw data trustworthy and legible.

## A. Invocation and lifecycle

**A1 (MUST).** Non-interactive CLI, runnable headless: no prompts, no TTY requirement, no interactive config. One command starts it, it runs, it exits by itself. (→ §5, §7.4)

**A2 (MUST).** Accepts three inputs as flags: the token list, the scan duration in seconds, and the output path. Example shape: `scraper --tokens tokens.txt --duration 90 --out books.json`. Exact names are free, but they must be stable across versions — Claude fills §5 with the literal command once. (→ §5)

**A3 (MUST).** Token input format handles at least 500 token IDs (config default `max_ws_assets` is 400): a file with one token ID per line, or a JSON array file. Token IDs are long decimal strings — treat as strings, never as numbers. (→ §4, §7.3)

**A4 (MUST).** Self-terminating: exits on its own within `duration + 30s` grace, even on hung connections. Claude never has to kill it or babysit it. (→ §9 run-time budget)

**A5 (MUST).** Meaningful exit codes: `0` = output file written and valid (even if some tokens failed — per-token status lives inside the output, see C4); non-zero = run failed, output unusable. Never exit `0` with a missing, truncated, or unparseable output file. This is the trigger for the scout's retry-then-REST-fallback rule. (→ §5 rule 2)

**A6 (MUST).** Strictly read-only and credential-free: market channel only, no API keys, no wallet material, no order endpoints, ever. (→ §9)

**A7 (MUST).** Logs go to stderr (or a log file), never mixed into the data output. stdout is either silent or a single final summary line (see F3). (→ §5 rule 2)

**A8 (SHOULD).** Idempotent and re-runnable: a retry immediately after a failure needs no cleanup and cannot corrupt the previous output (see C1 atomicity).

## B. Websocket protocol correctness

**B1 (MUST).** Connects to `wss://ws-subscriptions-clob.polymarket.com/ws/market` and subscribes with `{"assets_ids": [...], "type": "market", "custom_feature_enabled": true}`. The custom feature flag matters — it unlocks `best_bid_ask`, `new_market`, and `market_resolved` events the scout uses. (→ §3.3, §7.4)

**B2 (MUST).** Sends a `PING` text frame every ~10 s; treats a missed `PONG` (or silence > ~30 s) as a dead connection and reconnects. (→ §3.3)

**B3 (MUST).** Correct book maintenance: seed each token's book from its `book` snapshot, then apply `price_change` deltas in order. A delta received before any snapshot is not applied — wait for or request the snapshot.

**B4 (MUST).** Resync discipline: after any disconnect, reconnect, or suspected gap, the token's book is untrusted until re-seeded — either from a fresh `book` snapshot or from REST `GET https://clob.polymarket.com/book?token_id=…`. If re-seeding fails, the token is reported with `status: "resync_failed"`, **never** with its pre-gap book presented as current. This is the single most important requirement: a silently stale book turns directly into a fake arbitrage candidate. (→ §3.3, §7.5)

**B5 (MUST).** Applies `tick_size_change` events and reports the final `tick_size` per token (slippage buffers are computed in ticks). (→ §6.1)

**B6 (MUST).** Captures `new_market` and `market_resolved` events seen during the window and reports them in the output (the 5m/15m sweep depends on `new_market`; `market_resolved` prevents recommending a dead market). Subscribing to newly seen tokens mid-window is a MAY; reporting the events is a MUST. (→ §7.4, §3.2)

**B7 (SHOULD).** Shards across multiple websocket connections when the token count is large, rather than assuming one connection accepts everything; supports the `{"operation": "subscribe"/"unsubscribe"}` message for dynamic changes.

**B8 (SHOULD).** Records `last_trade_price` events per token, including the `fee_rate_bps` field — a free cross-check for the scout's fee verification. (→ §6.3)

**B9 (MAY).** Verifies the `hash` field on book snapshots as an integrity check.

## C. Output contract

**C1 (MUST).** Single JSON document, UTF-8, written **atomically**: write to `<out>.tmp`, validate it parses, then rename to `<out>`. The scout may read the file the moment the process exits. (→ §5)

**C2 (MUST).** Carries a `schema_version` string. Any breaking change to the shape bumps it (and §5 of the scout prompt gets updated in the same commit).

**C3 (MUST).** Run metadata block: `started_at`, `finished_at`, `window_seconds`, `tokens_requested`, `tokens_ok`, and connection stats (`reconnects`, `rest_resyncs`, connections used). The scout copies these into its `scan` block and `data_issues`. (→ §10.2)

**C4 (MUST).** **Every requested token appears in the output, exactly once, with an explicit status** — e.g. `"ok" | "no_data" | "subscribe_failed" | "resync_failed"`. An empty book on a live token is `status: "ok"` with empty `bids`/`asks` arrays. Claude must always be able to distinguish "genuinely no liquidity" from "scrape failed" — a missing key makes that impossible. (→ §2, §9)

**C5 (MUST).** Per-token payload (status `"ok"`):

- `condition_id`, full final `bids[]` / `asks[]` as `{price, size}` (bids descending, asks ascending — at minimum the top 10 levels per side), `tick_size`, `min_order_size`, `neg_risk`;
- `exchange_timestamp` (from the feed) **and** `received_at` (local wall clock, UTC) of the last update — both, so the scout can compute `book_age_seconds_at_compute` and enforce `stale_book_seconds`; (→ §4, §5 rule 3, §10.2)
- `source`: `"ws" | "ws+rest_resync" | "rest_only"` — feeds the scout's confidence rubric; (→ §8)
- `updates_applied` count.

**C6 (MUST).** Values are passed through **verbatim as the API's decimal strings** (`"0.982"`, `"1500"`), not re-parsed into floats. Claude does the numeric conversion; the scraper must not introduce float artifacts (`0.9819999...`) or rounding. Timestamps in ISO-8601 UTC or epoch milliseconds — one convention, documented, used everywhere. (→ §2, §9)

**C7 (MUST).** An `events` block: `new_markets[]` (question, assets_ids, outcomes, received_at) and `resolved[]` (asset/condition, winning outcome, received_at), plus a top-level `errors[]` of human-readable strings for anything abnormal. (→ §7.4, §10.2 `data_issues`)

**C8 (SHOULD).** Anomaly flags per token rather than silent correction: e.g. `"crossed_book"` (bid ≥ ask at close), `"delta_gap_resynced"`, `"snapshot_only"`. The scraper never "fixes" suspicious data — it labels it and lets the scout apply §7.5. (→ §7.5)

**C9 (MAY).** Compact per-token window summary for volatility context: updates count, high/low mid-price during the window, best bid/ask time-weighted spread. Useful for IPA confidence, not required.

### Reference output shape (illustrative, matches C1–C8)

```json
{
  "schema_version": "1.0",
  "started_at": "2026-08-14T15:00:12Z",
  "finished_at": "2026-08-14T15:01:44Z",
  "window_seconds": 90,
  "tokens_requested": 400,
  "tokens_ok": 396,
  "connection": { "ws_connections": 2, "reconnects": 1, "rest_resyncs": 3 },
  "books": {
    "71321045679252212594626385532706912750332728571942532289631379312455583992563": {
      "status": "ok",
      "condition_id": "0xabc…",
      "source": "ws",
      "bids": [ { "price": "0.978", "size": "1500" } ],
      "asks": [ { "price": "0.982", "size": "1900" } ],
      "tick_size": "0.001",
      "min_order_size": "5",
      "neg_risk": false,
      "exchange_timestamp": "1786554102412",
      "received_at": "2026-08-14T15:01:42.410Z",
      "updates_applied": 41,
      "last_trade": { "price": "0.98", "side": "BUY", "size": "120", "fee_rate_bps": "0" },
      "flags": []
    },
    "83191…": { "status": "resync_failed", "flags": ["delta_gap"], "bids": [], "asks": [] }
  },
  "events": {
    "new_markets": [
      { "question": "Bitcoin Up or Down — Aug 14, 3:15PM ET", "assets_ids": ["…", "…"], "outcomes": ["Up", "Down"], "received_at": "2026-08-14T15:01:05Z" }
    ],
    "resolved": [
      { "condition_id": "0xdef…", "winning_outcome": "Up", "received_at": "2026-08-14T15:00:58Z" }
    ]
  },
  "errors": [ "1 reconnect on connection 2 at 15:01:12Z; 3 tokens resynced via REST" ]
}
```

## D. Data quality guarantees

**D1 (MUST).** No filtering, no opinions: the scraper never drops, ranks, or pre-selects tokens because they "look uninteresting". Every requested token, raw state, full stop. Selection is the scout's job. (→ §1)

**D2 (MUST).** No stale mixing: a book is either current (post-resync, per B4) or explicitly failed. There is no third state where old data masquerades as fresh.

**D3 (MUST).** No invented values: fields the feed didn't provide are `null` or absent per the documented schema — never defaulted to `0`, `0.5`, or a previous run's value. (`"0"` as a real traded price/size from the API is of course legitimate.)

**D4 (SHOULD).** Monotonicity guard: out-of-order or duplicate deltas (same timestamp/hash) are dropped, and if ordering cannot be established the token gets resynced (B4) rather than guessed.

## E. Performance and limits

**E1 (MUST).** Handles 400+ tokens for a 90 s window (scout config defaults) finishing within the A4 grace budget, with bounded memory (books are bounded; don't buffer every raw frame in RAM unless asked to).

**E2 (MUST).** REST calls (seeding/resync) are rate-limited to ~10 req/s with exponential backoff on 429/5xx, max 3 attempts, then `resync_failed`. (→ §9)

**E3 (SHOULD).** Subscribes in batches with connection sharding (B7) sized well under any observed per-connection limits, rather than one giant subscribe that fails opaquely.

## F. Operational comfort (what makes it "comfy" for Claude)

**F1 (MUST).** `--help` is accurate and complete — Claude reads it before first use, and it is the ground truth for filling `{{SCRAPER_COMMAND}}` in §5. A `--version` flag SHOULD exist.

**F2 (MUST).** The output schema is documented in one place the agent can read (a `SCHEMA.md` next to the script, or a header comment in the script itself), and that document is what gets pasted into `{{SCRAPER_OUTPUT_FORMAT}}` in §5. If schema and docs disagree, that is a scraper bug, not a Claude problem.

**F3 (SHOULD).** Final one-line summary on stdout, grep-friendly: `OK tokens=396/400 reconnects=1 resyncs=3 duration=92s out=books.json`. Lets the scout confirm health without parsing the whole file first.

**F4 (SHOULD).** Errors are specific and actionable ("connection 2: no PONG for 30s, reconnecting", not "error: something went wrong"). These strings flow into the scout's `data_issues` and the human digest verbatim.

**F5 (SHOULD).** Single-file script with boring, pinned dependencies (e.g., Python + `websockets`/`aiohttp`, or Node + `ws`). The scout is forbidden from editing it (§5 rule 2), but a human will maintain it, and simple beats clever when it breaks at 3am.

**F6 (MAY).** A `--rest-only` mode that skips websockets and snapshots every token via `GET /book` — a debugging aid that mirrors the scout's own fallback path.

## G. Explicitly out of scope (keep it a dumb pipe)

The scraper does **not**: fetch Gamma metadata (discovery is the scout's job, §7.2); call `/fee-rate` or compute fees (§6.3 is the scout's job; passing through `fee_rate_bps` from trades per B8 is a bonus, not a substitute); compute edges, yields, or any strategy math; filter or rank markets (D1); place orders or authenticate (A6); retry forever (A4).

## H. Acceptance checklist

Run these before wiring a new/changed scraper into the scout:

1. **Happy path:** 5 known-liquid token IDs, 30 s window → exit 0; output parses with `jq .`; all 5 tokens present with `status: "ok"`; bids descending, asks ascending; both timestamps present and fresh.
2. **Bad token:** include one malformed token ID → exit 0; that token appears with a failure status; the other 4 unaffected.
3. **Scale:** 400 tokens, 90 s → completes within duration + 30 s; memory sane; `tokens_ok` ≥ ~95%.
4. **Kill the network mid-run** (drop connectivity ~20 s, restore): affected tokens end as `ok` with `source: "ws+rest_resync"` or as `resync_failed` — never as silently stale `"ws"` books. Reconnects counted in metadata.
5. **Atomicity:** `kill -9` the process mid-write → the previous `<out>` (if any) is intact or absent; no truncated JSON at the final path ever exists.
6. **Determinism of the contract:** run twice back-to-back → identical schema, identical field names/types (values differ, shape never).
7. **Fresh 5m/15m churn:** run during an active crypto up/down window → `new_markets` is populated when new instances launch.
8. **Verbatim values:** spot-check 3 prices in the output against the live REST `/book` for the same tokens — string-identical, no float drift (C6).

When all eight pass, paste the literal invocation into `{{SCRAPER_COMMAND}}`, the token-input convention into `{{HOW_TO_PASS_TOKEN_IDS}}`, and the schema doc (F2) into `{{SCRAPER_OUTPUT_FORMAT}}` in §5 of the scout prompt — the scout needs nothing else about the scraper to be true.
