# The change stream

`stream_version` `1.0`

The optional file `polymarket-scraper` appends to `--stream`. Its companion is
`SCHEMA.md`, which describes the output document and is the contract; this is
the same run told as it happens.

The document is complete but arrives at the end, which is no use to anything
deciding what to do during the window, or watching to see whether a run is going
wrong. That is what this is for.

**One JSON object per line**, appended and flushed per record. The framing is
chosen for the reader: a single JSON array would not be parseable until the run
finished, which is exactly the problem being solved. Tail it and parse each line
as it lands.

```bash
polymarket-scraper --tokens tokens.txt --out books.json --stream changes.jsonl &
tail -f changes.jsonl
```

## What this is not

A second view, never a replacement. **The document is the contract.** It is
written atomically, every requested token appears in it exactly once, and a book
is never reported as current unless it is.

The stream carries none of those guarantees:

- It can stop mid-record if the process is killed, so a consumer must be willing
  to discard a trailing partial line.
- It says nothing about tokens that never changed. Absence here is not a
  statement; the document's is.
- If a write fails the record is dropped and counted, and the run says so in its
  log rather than in this file.

`stream_version` is separate from the document's `schema_version` because the two
change for different reasons and a consumer may well read one and not the other.

## Units

The same rule as the document: a name says its unit, or the value is a string
that carries its own. Prices, sizes, spreads, mids and fee rates are the API's
own decimal strings, never re-rounded and never floats. Timestamps the scraper
generates are ISO-8601 UTC with milliseconds; timestamps from the feed are the
API's own string, epoch milliseconds, not reformatted.

## Every record

| Field | Type | Meaning |
|---|---|---|
| `kind` | string | `header`, `quote`, `trade`, `flag`, `market` or `resolved`. |
| `at` | string | When the scraper wrote the record. |

A field that does not apply to a kind is **absent** rather than null. This is the
one place the project omits fields, and it means something different from the
document's nulls: there, absent would mean "we did not find out"; here it means
"this kind of record does not have one".

## `header`

Written when the stream opens, once per run. Runs append to the same file, so a
header is what separates them.

| Field | Type | Meaning |
|---|---|---|
| `version` | string | The version of this format. |
| `run` | string | Identifies the run. The same identifier appears on every line of the log file, so the two can be lined up. |
| `started_at` | string | When collection began. |

## `quote`

A token's top of book, **written only when it moves**. Most updates change a
level nobody is looking at; reporting every one would bury the handful that
moved the price, and at several hundred tokens it would be the bulk of the file.
A price respelled from `0.98` to `0.980` is not a move.

| Field | Type | Meaning |
|---|---|---|
| `token` | string | The outcome token. |
| `bid` | string | Best bid. Absent if that side is empty. |
| `ask` | string | Best ask. Absent if that side is empty. |
| `spread` | string | `ask` minus `bid`. Absent unless both sides had a usable price. |
| `mid` | string | Midpoint. Absent on the same condition. |
| `crossed` | bool | Present and true when the best bid is at or above the best ask, which usually means the data is stale. |
| `exchange_timestamp` | string | The feed's timestamp for the update that moved it. |

A quote is only ever written for a token the scraper currently trusts. One that
has lost trust reports nothing until it has been re-seeded, which is the same
promise the document makes.

## `trade`

| Field | Type | Meaning |
|---|---|---|
| `token` | string | The outcome token. |
| `price` | string | Fill price. |
| `size` | string | Fill size. |
| `side` | string | `BUY` or `SELL`. |
| `fee_rate_bps` | string | The fee rate the exchange reported for the fill. |
| `exchange_timestamp` | string | The feed's timestamp for the fill. |

## `flag`

An observation raised against a token, written the first time it is raised and
never repeated. The values are the set `SCHEMA.md` documents for the document's
own `flags` field.

| Field | Type | Meaning |
|---|---|---|
| `token` | string | The outcome token. |
| `flag` | string | The observation. |

## `market` and `resolved`

Announcements, written as they arrive. The same events the document's `events`
block collects at the end.

| Field | Type | Meaning |
|---|---|---|
| `question` | string | The market question. `market` only. |
| `condition_id` | string | The market identifier. |
| `assets_ids` | array of strings | The outcome tokens. |
| `outcomes` | array of strings | The outcome names. `market` only. |
| `winning_asset_id` | string | The token that pays out. `resolved` only. |
| `winning_outcome` | string | The outcome that won. `resolved` only. |
| `sports_market_type` | string | What kind of sports market this is. `market` only, and absent for anything that is not a sports market. |
| `starts_at` | string | When the underlying event begins, in the exchange's own spelling, for example `2026-08-23 19:00:00+00`. `market` only. |
| `min_tick_size` | string | The market's minimum price increment. `market` only. |
| `exchange_timestamp` | string | The feed's timestamp. |

`sports_market_type` is the only categorisation the feed offers. The
short-duration crypto series carries none and is identifiable by its slug
instead, which is structured as `eth-updown-5m-1787458200`. The scraper groups
nothing itself; see `SCHEMA.md` for the same note against the document.

## Example

```jsonl
{"kind":"header","at":"2026-08-14T15:00:12.000Z","version":"1.0","started_at":"2026-08-14T15:00:12.000Z","run":"9f2c1a44"}
{"kind":"quote","at":"2026-08-14T15:00:12.412Z","token":"71321045...","bid":"0.978","ask":"0.982","spread":"0.004","mid":"0.98","exchange_timestamp":"1786554102412"}
{"kind":"trade","at":"2026-08-14T15:00:14.001Z","token":"71321045...","price":"0.98","size":"120","side":"BUY","fee_rate_bps":"0","exchange_timestamp":"1786554104000"}
{"kind":"flag","at":"2026-08-14T15:00:31.870Z","token":"83191","flag":"delta_gap"}
{"kind":"market","at":"2026-08-14T15:00:44.100Z","question":"Bitcoin Up or Down - Aug 14, 3:15PM ET","condition_id":"0xdef","assets_ids":["111","222"],"outcomes":["Up","Down"],"exchange_timestamp":"1786554144000"}
```
