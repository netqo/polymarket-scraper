# Output schema

`schema_version` `1.0`

The document `polymarket-scraper` writes to `--out`. This file is the contract:
if it and the code disagree, that is a bug in the scraper, and a test in
`internal/report` fails when a field exists in one and not the other.

## Guarantees

These hold for every run, and they are the reason the document is shaped the way
it is.

1. **Every requested token appears exactly once**, whatever went wrong. A token
   that produced nothing says so; it does not disappear.
2. **A book is never reported as current unless it is.** After a disconnect or a
   suspected gap a token is re-seeded, and if that fails the token is reported as
   `resync_failed` with an empty book. The pre-gap book is never shown.
3. **No field is ever omitted.** A value that is not known is `null`. An empty
   book on a live token is `status: "ok"` with empty arrays, which is a
   different statement from a failure.
4. **No value is re-rounded.** Prices and sizes are the API's own decimal
   strings, passed through byte for byte. Statistics the scraper computes itself
   are produced by integer arithmetic and rendered as strings for the same
   reason. There is no floating-point number anywhere in the document.
5. **The write is atomic.** The output path holds either the previous run's
   document or this one's, never a partial file. The file is staged in the
   destination directory and renamed into place, so it is created with mode
   `0600`: it is readable by the user that ran the scraper, and a consumer
   running as a different user needs the permissions widened deliberately.

## Conventions

| Kind | Convention |
|---|---|
| Prices, sizes, tick sizes | JSON strings, exactly as the API sent them. `"0.980"` stays `"0.980"`. |
| Timestamps the scraper generates | ISO-8601 UTC with milliseconds and a `Z`, e.g. `"2026-08-14T15:01:42.410Z"`. |
| Timestamps from the feed | The exact string the API sent, which is epoch milliseconds, e.g. `"1786554102412"`. Not reformatted. |
| Unknown values | `null`. Never `0`, `false`, `""` or a previous run's value. |
| Empty lists | `[]`. Never `null`. |

The two timestamp conventions are deliberate. Re-rendering the feed's own
timestamp would be the same lossy round trip that the decimal handling exists to
avoid, so it is passed through and the scraper's own clock is reported beside it.
Together they let a consumer compute how old a book is and see whether the two
clocks agree.

## Top level

| Field | Type | Meaning |
|---|---|---|
| `schema_version` | string | The version of this contract. |
| `started_at` | string | When the run began. |
| `finished_at` | string | When the document was built. |
| `window_seconds` | number | The configured collection window, in seconds. Not the same as `finished_at` minus `started_at`, which also includes shutdown. |
| `tokens_requested` | number | How many distinct tokens were asked for. |
| `tokens_ok` | number | How many of the **requested** tokens have status `ok`. Never more than `tokens_requested`, so `tokens_ok / tokens_requested` is a usable health ratio. Tokens the run picked up on its own are counted by `tokens_discovered` instead. |
| `tokens_discovered` | number | How many tokens were added from announcements during the run. |
| `connection` | object | How the data was obtained. |
| `books` | object | Keyed by token id. Contains every requested token, and possibly more. |
| `events` | object | Announcements seen during the window. |
| `errors` | array of strings | Anything abnormal, in words. Meant to be quoted verbatim. |

### `connection`

| Field | Type | Meaning |
|---|---|---|
| `ws_connections` | number | How many websocket connections were used. |
| `reconnects` | number | How many times a connection had to be re-established. |
| `rest_requests` | number | How many REST requests were made in total. |
| `rest_resyncs` | number | How many tokens were re-seeded over REST. |

## `books.<token_id>`

| Field | Type | Meaning |
|---|---|---|
| `status` | string | One of `ok`, `no_data`, `subscribe_failed`, `resync_failed`. See below. |
| `source` | string or null | Where the reported book came from: `ws`, `ws+rest_resync`, or `rest_only`. Null unless the status is `ok`. |
| `condition_id` | string or null | The market the token belongs to. |
| `bids` | array | Price levels, highest first. Empty unless the status is `ok`. |
| `asks` | array | Price levels, lowest first. Empty unless the status is `ok`. |
| `tick_size` | string or null | Minimum price increment. |
| `min_order_size` | string or null | Minimum order size. Only ever available over REST. |
| `neg_risk` | boolean or null | Whether the market is part of a mutually exclusive set. Null means not learned, which is not the same as false. |
| `exchange_timestamp` | string or null | The feed's timestamp for the last update. Null unless the status is `ok`. |
| `received_at` | string or null | When that update reached the scraper. Null unless the status is `ok`. |
| `updates_applied` | number | Incremental updates actually applied. |
| `last_trade` | object or null | The most recent fill seen. |
| `flags` | array of strings | Observations the scraper made and deliberately did not act on. |
| `window` | object | Volatility context over the window. |

### `status`

| Value | Meaning |
|---|---|
| `ok` | The book is current. Empty `bids` and `asks` with this status mean the token genuinely has no liquidity. |
| `no_data` | Nothing was ever received for this token. |
| `subscribe_failed` | No subscription was established. |
| `resync_failed` | The book became untrusted and could not be re-seeded. The stale book is not reported. |

This set is closed. Anything else worth saying is said with a flag.

### `bids[]` and `asks[]`

| Field | Type | Meaning |
|---|---|---|
| `price` | string | The price, as the API sent it. |
| `size` | string | Aggregate size resting at that price. |

A level whose price could not be interpreted numerically is still reported, at
the end of its side, and the token carries `unparsable_price`.

### `last_trade`

| Field | Type | Meaning |
|---|---|---|
| `price` | string | Fill price. |
| `size` | string | Fill size. |
| `side` | string | `BUY` or `SELL`. |
| `fee_rate_bps` | string | The fee rate the exchange reported for the fill. |
| `timestamp` | string | The feed's timestamp for the fill. |

### `window`

| Field | Type | Meaning |
|---|---|---|
| `updates` | number | Incremental updates applied during the window. |
| `mid_high` | string or null | Highest mid price observed. Null if the book never had two sides. |
| `mid_low` | string or null | Lowest mid price observed. |
| `spread_time_weighted` | string or null | Spread averaged over the time the book had two sides, not over the number of updates. |
| `two_sided_millis` | number | How long the book had two sides, so you can see how much of the window the average covers. |

### `flags`

Flags are observations, never corrections. The scraper labels suspicious data
and reports it as it stands, because repairing it would destroy the evidence
that something was wrong.

| Flag | Meaning |
|---|---|
| `crossed_book` | The best bid was at or above the best ask at the end of the window. Usually means the data is stale. |
| `delta_gap` | An update was missed, or arrived too far out of order to trust. |
| `delta_gap_resynced` | A gap happened and was recovered from by re-seeding. |
| `disconnected` | The connection carrying this token dropped at least once. |
| `snapshot_only` | The book was seeded and no incremental update ever arrived. |
| `pre_snapshot_delta_dropped` | Updates arrived before any snapshot and were discarded rather than applied to an empty book. |
| `duplicate_delta_dropped` | The same update was received more than once. |
| `timestamp_regression` | A timestamp went backwards, but within tolerance, so the update was applied. |
| `decode_error` | A message for this token could not be decoded. |
| `unknown_side` | An update named a side this build does not recognise and was not applied. |
| `best_bid_ask_mismatch` | The published top of book disagreed with the maintained one. |
| `unparsable_price` | A level carried a price that could not be interpreted numerically. It is still reported. |
| `token_not_found` | The exchange does not recognise this token id. |
| `tick_size_changed` | The minimum price increment changed during the window. |
| `market_resolved` | The market settled during the window. |
| `discovered_mid_window` | The token was not requested; it was picked up from an announcement. |

## `events`

| Field | Type | Meaning |
|---|---|---|
| `new_markets` | array | Markets announced during the window. |
| `resolved` | array | Markets that settled during the window. |

### `events.new_markets[]`

| Field | Type | Meaning |
|---|---|---|
| `question` | string | The market question. |
| `condition_id` | string or null | The market identifier. |
| `assets_ids` | array of strings | The outcome tokens. |
| `outcomes` | array of strings | The outcome names. |
| `received_at` | string | When the announcement arrived. |

The announcement feed is global rather than filtered to the subscription, so
this is a discovery feed as much as a status report.

### `events.resolved[]`

| Field | Type | Meaning |
|---|---|---|
| `condition_id` | string or null | The market that settled. |
| `assets_ids` | array of strings | Its outcome tokens. |
| `winning_asset_id` | string or null | The token that pays out. |
| `winning_outcome` | string or null | The outcome that won. |
| `received_at` | string | When the announcement arrived. |

## Exit status

| Code | Meaning |
|---|---|
| 0 | The document was written and is valid. Individual tokens may still have failed; their `status` says so. |
| 1 | The run failed and no usable document was produced. |
| 2 | The command line was wrong. Nothing was written. |
| 3 | The watchdog terminated a run that would not shut down. |

On success, and only on success, one summary line is written to stdout:

```
OK tokens=396/400 discovered=4 connections=2 reconnects=1 resyncs=3 errors=1 duration=1m32s out=books.json
```

Non-empty stdout is therefore a reliable success signal on its own.

## Example

```json
{
  "schema_version": "1.0",
  "started_at": "2026-08-14T15:00:12.000Z",
  "finished_at": "2026-08-14T15:01:44.120Z",
  "window_seconds": 90,
  "tokens_requested": 2,
  "tokens_ok": 1,
  "tokens_discovered": 0,
  "connection": {
    "ws_connections": 1,
    "reconnects": 1,
    "rest_requests": 4,
    "rest_resyncs": 1
  },
  "books": {
    "71321045679252212594626385532706912750332728571942532289631379312455583992563": {
      "status": "ok",
      "source": "ws",
      "condition_id": "0xabc",
      "bids": [{ "price": "0.978", "size": "1500" }],
      "asks": [{ "price": "0.982", "size": "1900" }],
      "tick_size": "0.001",
      "min_order_size": "5",
      "neg_risk": false,
      "exchange_timestamp": "1786554102412",
      "received_at": "2026-08-14T15:01:42.410Z",
      "updates_applied": 41,
      "last_trade": {
        "price": "0.98",
        "size": "120",
        "side": "BUY",
        "fee_rate_bps": "0",
        "timestamp": "1786554100000"
      },
      "flags": [],
      "window": {
        "updates": 41,
        "mid_high": "0.9815",
        "mid_low": "0.9795",
        "spread_time_weighted": "0.004",
        "two_sided_millis": 89000
      }
    },
    "83191": {
      "status": "resync_failed",
      "source": null,
      "condition_id": null,
      "bids": [],
      "asks": [],
      "tick_size": null,
      "min_order_size": null,
      "neg_risk": null,
      "exchange_timestamp": null,
      "received_at": null,
      "updates_applied": 0,
      "last_trade": null,
      "flags": ["disconnected", "delta_gap"],
      "window": {
        "updates": 0,
        "mid_high": null,
        "mid_low": null,
        "spread_time_weighted": null,
        "two_sided_millis": 0
      }
    }
  },
  "events": {
    "new_markets": [],
    "resolved": []
  },
  "errors": ["connection 1: no frame for 30s, reconnecting"]
}
```
