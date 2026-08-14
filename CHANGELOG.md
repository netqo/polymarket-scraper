# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- A scale test at 400 tokens, which is the size the consuming agent uses and the
  first case that produces more than one connection, and a test that memory does
  not grow with the number of updates received.

### Changed
### Deprecated

### Removed
- The unused unsubscribe message builder. The protocol defines the operation but
  this build does not send it.

### Fixed
- Mid-window discovery no longer switches itself off when the token list exactly
  fills a connection, which was the case at the consuming agent's documented
  configuration of 400 tokens.

### Security

## [0.1.0] - 2026-08-14

First release. Output contract frozen at `schema_version` 1.0.

### Added

- Collect live Polymarket order books for a list of outcome tokens over a fixed
  window, and write one JSON document describing every token that was asked
  about. Read-only and credential-free throughout.
- **Trust guarantees.** A book is never reported as current unless it is. After
  a disconnect, a decode failure, or an update far enough out of order to
  suggest something was missed, the token is re-seeded; if that fails it is
  reported as `resync_failed` with an empty book rather than having its pre-gap
  book presented as fresh.
- **Every requested token appears exactly once**, with an explicit status,
  whatever went wrong during the run. An empty book on a live token is a
  success, not a failure, and the two are distinguishable.
- **Values are passed through as the exchange's own decimal strings**, byte for
  byte. Statistics the scraper computes itself are produced by integer
  arithmetic. There is no floating-point number anywhere in the document.
- **The write is atomic.** The output path holds either the previous run's
  document or this one's, never a fragment.
- **Bounded runtime.** The process ends within its window plus a grace period
  whatever the network does, with a watchdog behind the cooperative shutdown.
- Connection sharding, reconnection with backoff, and batched re-seeding, so a
  disconnect that distrusts every token at once recovers in a couple of requests
  rather than one per token.
- `--rest-only` mode, which skips the websocket entirely and snapshots every
  token over REST.
- Market announcements and resolutions reported in the document's `events`
  block, and tokens announced mid-window subscribed to and collected, which is
  what makes a run useful during the short-duration crypto series.
- Per-token volatility context over the window: update count, mid-price range,
  and a spread averaged over the time the book actually had two sides.
- `SCHEMA.md`, the output contract, held to the code by tests in both
  directions.

### Fixed

Behaviour where the live API and its published documentation disagree. Each of
these fails silently rather than loudly, and each was found by running against
the exchange rather than by reading the specification.

- **Book ordering.** The documentation states bids come back descending and asks
  ascending. The live API does the exact opposite, on both REST and the
  websocket, so reading the first element of either array gives the *worst*
  price. Both sides are sorted on ingest and neither claim is trusted.
- **Snapshot framing.** The initial book snapshot arrives as a JSON array
  holding every subscribed asset in a single frame, while every other event is a
  bare object. A decoder that assumes objects loses all of its initial state and
  then looks healthy, because deltas keep arriving.
- **Batched price changes.** The field is `price_changes`, the envelope carries
  no asset id, and the best quotes ride on each element. One message routinely
  covers both legs of a binary market.
- **Silent subscribe ceiling.** Past roughly 750 assets the server accepts a
  subscription, keeps delivering updates, and never sends the initial snapshot,
  with no error. Snapshots received are counted and connections are sharded well
  below that width.
- **Keepalives are raw text.** `PING` and `PONG` are uppercase text frames, not
  websocket protocol pings, and are recognised before anything tries to parse
  them.

[Unreleased]: https://github.com/netqo/polymarket-scraper/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/netqo/polymarket-scraper/releases/tag/v0.1.0
