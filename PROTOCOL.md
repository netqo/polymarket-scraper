# The Polymarket CLOB market channel

What the scraper reads, and how. `SCHEMA.md` is the contract for what it
*writes*; this is the input side.

It is written down because several things here contradict Polymarket's published
documentation, and every one of those contradictions fails **silently**. A client
that gets them wrong keeps running, keeps looking healthy, and quietly holds
wrong data. Those cases are marked below.

Everything described here is public market data. No endpoint used by this
scraper requires authentication, and none of them place orders.

The payloads quoted below were captured from the live API. They live as fixtures
in `internal/wire/frame_test.go` and `internal/wire/rest_test.go`, kept verbatim
including field order.

## Websocket

### Connecting

```
wss://ws-subscriptions-clob.polymarket.com/ws/market
```

No authentication, no headers. Compression is not negotiated; asking for it only
adds a way for the handshake to fail.

### Subscribing

One message, sent immediately after the handshake:

```json
{"assets_ids":["111","222"],"type":"market","custom_feature_enabled":true}
```

`custom_feature_enabled` **is not optional in practice**. Without it the
connection never delivers `best_bid_ask`, `new_market` or `market_resolved`, and
nothing reports that they are missing. The scraper's output document is required
to report all three, so this flag is checked byte for byte by a test.

Tokens can be added to a live connection later. The update message is a
different shape from the opening subscription rather than a repeat of it: it
carries an operation and no `type`.

```json
{"operation":"subscribe","assets_ids":["333"]}
```

The protocol also defines an unsubscribe operation. This build never sends it: a
connection's width is bounded by not taking tokens on rather than by giving them
back, and a settled market's book simply stops changing.

**A subscription does not survive a reconnect.** Anything added mid-window has to
be asked for again when the socket is redialled, or its updates quietly stop
while its book still looks current.

### The silent subscribe ceiling

> **Contradicts the documentation, and fails silently.**

Past roughly **750 assets** on one connection, the server accepts the
subscription, keeps delivering incremental updates, and never sends the initial
book snapshot. No error, no close, nothing in any message says so.

A client that does not count what it received looks perfectly healthy while
holding no book state at all. The only way to detect it is to compare snapshots
received against assets subscribed, which is what `wsclient.Shard.SnapshotsSeen`
exists for.

The scraper shards connections at 400 assets by default and refuses any setting
above 700, a deliberate margin below the observed cliff. Finding the exact edge
is not worth discovering in production.

### Keepalives

> **Contradicts the documentation, and fails silently.**

The keepalive is an **application-level text frame containing the literal word
`PING`**, answered with the literal word `PONG`. It is not a websocket protocol
ping.

Calling a websocket library's `Ping` method talks to the wrong layer. The
connection then dies about half a minute later for no visible reason.

Because these are raw text, they have to be recognised *before* anything tries to
parse a frame as JSON. Otherwise every keepalive surfaces as a decode error and
takes its tokens out of trust.

### Frame shapes

> **Contradicts the documentation, and fails silently.**

A frame is either a bare JSON object or **an array of them**. The initial book
snapshot uses the array form, carrying every subscribed asset in a single frame;
every other event is a bare object.

A decoder that assumes objects throws away all of its initial state and then
looks healthy, because deltas keep arriving on top of nothing.

That first frame is also the largest thing the server ever sends, and it grows
with the number of assets. The read limit defaults to 32 MiB for this reason; set
it too low and the one message the whole run depends on is dropped.

Within an array, one bad element does not invalidate the rest. The scraper
decodes what it can and reports the failures alongside, because discarding a
whole frame over one element would throw away updates for tokens that are fine.

### Routing

Every message carries `event_type`, but **it is not the first field**. It has to
be found by parsing, not by looking at the start of the bytes.

An `event_type` this build does not recognise is reported rather than rejected: a
protocol addition is something to notice, not a reason to distrust a token.
Unknown *fields* are ignored, so the feed can grow without breaking this build.

### Events

#### `book` -- a full snapshot for one token

```json
{"market":"0x5dacb391...","asset_id":"63691357...","timestamp":"1786728432337",
 "hash":"924ec9bc...","bids":[{"price":"0.001","size":"2200.8"}],
 "asks":[{"price":"0.999","size":"50"}],
 "tick_size":"0.001","last_trade_price":"0.004","event_type":"book"}
```

`tick_size` and `last_trade_price` appear on the initial snapshot but are
**absent from the refreshes sent after a trade**, so tick size must be harvested
and cached rather than expected every time. `min_order_size` and `neg_risk` never
appear on the websocket at all; see REST below.

#### `price_change` -- a batch of incremental updates

```json
{"market":"0x3bb59c17...","price_changes":[
  {"asset_id":"40600137...","price":"0.06","size":"374.34","side":"BUY",
   "hash":"f3285de0...","best_bid":"0.07","best_ask":"0.08"},
  {"asset_id":"79597156...","price":"0.94","size":"0","side":"SELL",
   "hash":"bc67a217...","best_bid":"0.92","best_ask":"0.93"}],
 "timestamp":"1786728438403","event_type":"price_change"}
```

> **Contradicts the documentation.**

The field is `price_changes` and it is a **batch**. The envelope carries **no
asset id at all**: each element names its own token, and one message routinely
covers both legs of a binary market. The best quotes ride on each element rather
than the envelope.

Two semantics matter more than anything else in this file:

- **`size` is the new aggregate size resting at that price, not a delta** against
  the previous one.
- **`size` of `0` removes the level.** It does not set the level to zero. Getting
  this backwards leaves phantom liquidity in the book, which turns directly into
  a trade against depth that is not there.

`side` is `BUY` or `SELL`, uppercase. A buy rests on the bid side, a sell on the
ask side. An unrecognised value is not guessed at: putting liquidity on the wrong
half of the book is worse than not applying the update.

`hash` repeats when the exchange resends an update. Live runs confirm this
happens routinely, so it is worth deduplicating on.

#### `last_trade_price` -- a fill

```json
{"market":"0x12dc2b61...","asset_id":"72710166...","price":"0.585","size":"2.46",
 "fee_rate_bps":"0","side":"SELL","timestamp":"1786728467101",
 "event_type":"last_trade_price","transaction_hash":"0x6c83cdd2..."}
```

`fee_rate_bps` is the reason this event is collected at all: a free cross-check
on the fee rate a consumer verifies through other means.

#### `best_bid_ask` -- the published top of book

```json
{"market":"0x12dc2b61...","asset_id":"72710166...","best_bid":"0.585",
 "best_ask":"0.587","spread":"0.002","timestamp":"1786728467006",
 "event_type":"best_bid_ask"}
```

**Not guaranteed to be delivered**, so it must never be the source of a book.
The scraper computes its own top of book from snapshots and deltas and uses this
only as an independent cross-check; a disagreement means the locally maintained
book has diverged, almost always because a delta was missed or misapplied.

#### `tick_size_change` -- the minimum price increment changed

```json
{"event_type":"tick_size_change","asset_id":"658186","market":"0xbd31",
 "old_tick_size":"0.01","new_tick_size":"0.001","timestamp":"1757908892351"}
```

#### `new_market` and `market_resolved` -- announcements

```json
{"id":"5551","market":"0xabc","condition_id":"0xdef",
 "question":"Bitcoin Up or Down - Aug 14, 3:15PM ET","slug":"btc-up-or-down",
 "assets_ids":["111","222"],"outcomes":["Up","Down"],
 "timestamp":"1786728400000","event_type":"new_market"}
```

```json
{"event_type":"market_resolved","id":"1031769","market":"0x311d0c",
 "assets_ids":["7604307","3169093"],"winning_asset_id":"7604307",
 "winning_outcome":"Yes","timestamp":"1766790415550","tags":["stocks"]}
```

**These feeds are global, not filtered to your subscription.** Every connection
receives every announcement, which has two consequences: a run with four shards
sees each announcement four times and must deduplicate, and a run that subscribes
to what it sees announced needs a hard budget or it will gradually subscribe to
every market on the exchange.

The upside is that this doubles as a discovery feed, which is what makes a run
useful during the short-duration crypto series, where instances are created
minutes before they start.

### Inconsistent list encoding

> **Contradicts itself between endpoints.**

List-valued fields arrive **either** as a real array **or** as a string
containing an encoded array:

```json
"outcomes": ["Yes","No"]
"outcomes": "[\"Yes\",\"No\"]"
```

Both mean the same thing. Which one arrives is not predictable, so the tolerance
lives in the decoder rather than at every use site.

## REST

Base: `https://clob.polymarket.com`. No authentication.

### `GET /book?token_id=...`

One token's book.

### `POST /books`

Many books in one request, which is what makes a whole-shortlist pass cheap:
several hundred tokens in one call rather than several hundred calls.

```json
[{"token_id":"111"},{"token_id":"222"}]
```

The payload limit is on **bytes rather than count** and rejects somewhere past
roughly 500 ids, so the batch size default of 250 leaves room for longer ids.

**The response may omit tokens.** A token id the exchange does not recognise is
simply absent rather than reported, so a caller must not assume one book back per
id it asked for. The scraper treats an omitted token as a failed re-seed rather
than leaving it with a stale book, and falls back to individual requests to turn
"absent from the batch" into a specific per-token answer.

### Response body

```json
{"market":"0x7d0aaf81...","asset_id":"27146956...","timestamp":"1786728198766",
 "hash":"4514cbfc...","bids":[{"price":"0.001","size":"28195.33"}],
 "asks":[{"price":"0.999","size":"231.08"}],
 "min_order_size":"5","tick_size":"0.001","neg_risk":true,
 "last_trade_price":"0.004"}
```

`min_order_size` and `neg_risk` appear **here and nowhere else**. The websocket
never sends either, and the output document reports both, so a REST fetch is part
of every run rather than a fallback for when the websocket fails.

`neg_risk` absent and `neg_risk: false` are different answers. Defaulting a
missing field to false would be inventing a value that a consumer then acts on.

### Statuses

`404` means the exchange does not recognise the token id. That is a fact about
the token rather than a setback, so it is never retried; every other failure
might succeed on the next attempt.

`Retry-After`, when present, is in seconds rather than the HTTP-date form.

## Book ordering

> **Contradicts the documentation, and fails silently.**

The published documentation says bids come back descending and asks ascending.
**The live API does the exact opposite, on both REST and the websocket.**

Taking the first element of either array as the best price therefore gives you
the *worst* one. Neither claim is trusted: both sides are sorted on ingest.

## Decimal values

Every price and size is a **string**, and must stay one.

Re-parsing them into floats and serializing back turns `0.982` into
`0.9819999999999999`. Token ids are worse: they are 77-digit decimal strings and
cannot survive a float64 at all, which is why a JSON array of *numbers* is
rejected outright rather than silently truncated into plausible wrong ids.

Timestamps from the feed are epoch milliseconds, as strings. They are passed
through verbatim for the same reason.

## Open questions

Things this document cannot answer without more time against the live API are
tracked separately, in the working notes rather than here: whether the feed ever
sends values that are not plain decimals, what the real keepalive tolerance is,
and what error bodies accompany each status.
