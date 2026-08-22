# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- `PROTOCOL.md`, describing what the exchange sends: connecting and subscribing,
  the keepalive, the two frame shapes, every event type with a payload captured
  from the live API, the REST endpoints, and the decimal handling. The places
  where the live API contradicts its own published documentation are marked,
  because every one of them fails silently. A test holds the document to the
  code, the way `SCHEMA.md` is held.
- `--help=json`, returning the command line contract as data: the flags and
  their defaults, the environment variables, the exit codes with their meanings,
  and the settings file's sections, modes and log categories. The prose is
  included whole rather than chopped into fields. Everything else is derived
  from the code, so a flag cannot exist without appearing in it.
- Log records are grouped into categories that can be switched off
  independently in the settings file, under `[logging.categories]`: `startup`,
  `progress`, `connection`, `flags`, `rest`, `decode` and `discovery`. Level is
  the wrong axis for the question, since wanting every per-token flag and none
  of the keepalive chatter is about subject matter and both sit at the same
  level. Errors are never suppressed by a category switch.
- Settings can come from a TOML file, found via `--config`, `POLYMARKET_CONFIG`,
  or `polymarket-scraper.toml` in the working directory.
  `polymarket-scraper.example.toml` is a fully commented copy in which every
  value is the default, held to that by a test. An unknown setting is an error
  rather than a warning, because a typo that is silently ignored looks identical
  to a setting that had no effect.
- `--mode` and `POLYMARKET_MODE`, selecting `production` or `debug`. A mode is a
  bundle of defaults rather than a setting of its own: `debug` drops the log
  level and stops truncating attribute values on the terminal. Anything set
  explicitly still wins over it.
- The tuning that used to be compiled in is now reachable from the settings
  file: REST attempts, timeout and backoffs, the cap on `Retry-After`, the
  re-seed worker count, reconnection backoffs, the websocket read limit, and the
  bounds on the output document's error and event lists. Changing one no longer
  means rebuilding.
- `--log-file`, which appends every log record to a file as the run happens.
  stderr is only readable by whatever launched the process, so a run in progress
  could not be followed from anywhere else. The file is appended to at mode
  `0600` and written per record, so it can be tailed while the run is still
  going.
- Log records are rendered with a timestamp, a prefix and the run's identifier:
  `[!]` error or warning, `[*]` info, `[~]` step or detail, `[+]` success, `[?]`
  something needing a decision. Colour appears only when the destination is a
  terminal.
- The full resolved configuration is recorded once at startup, so a log can be
  read without guessing whether a number is a symptom or the configured
  behaviour working as asked.
- Flags are reported the moment a tracker raises them, rather than only in the
  document written at the end. Until now a run quietly losing trust in every
  token looked exactly like a healthy one to anything watching it.
- A scale test at 400 tokens, which is the size the consuming agent uses and the
  first case that produces more than one connection, and a test that memory does
  not grow with the number of updates received.

### Changed
- Repeated log records are written once and then summarised with their count,
  so a connection failing in a loop no longer buries everything else.
- The output document's `errors` list collapses identical messages into one
  entry carrying a count, and its cap now counts distinct messages rather than
  occurrences. A connection redialling in a tight loop used to fill all five
  hundred slots with one sentence and discard every later message, including the
  ones explaining why the run ended.
- Each shard now waits only for its own REST work when the window closes, rather
  than for every shard's, so one slow re-seed no longer holds the others through
  their whole drain allowance.
- `--help` states the maximum a connection may be widened to, which validation
  has always enforced but the text never named.
- The REST request timeout is applied per attempt whether or not a transport is
  supplied, instead of being silently ignored when one is.

### Deprecated

### Removed
- The unused unsubscribe message builder. The protocol defines the operation but
  this build does not send it.
- The unreachable `stale` tracker state, the shard's write-only discovered-token
  map, and a websocket accessor no production caller used.

### Fixed
- A frame that could not be decoded was quoted at 120 characters everywhere,
  including in contexts with no reason to shorten it, so the evidence of what
  the exchange actually sent was routinely cut off before the part that
  mattered. Each destination now applies its own bound: the terminal shows a
  couple of lines, the document a sentence, and the log file the whole thing.
  Every one of them reports what it left out.
- Mid-window discovery no longer switches itself off when the token list exactly
  fills a connection, which was the case at the consuming agent's documented
  configuration of 400 tokens.
- A connection that ends no longer waits a full keepalive interval before the
  shard can react. Teardown joined the write loop before cancelling it, which
  delayed both the reconnect and the notice that the tokens on that connection
  had stopped being trustworthy by up to the 10s ping interval.
- Tokens taken on mid-window are resubscribed when the connection is redialled.
  They were dropped from the subscription at the first reconnect while their
  books carried on being reported as current.
- A book snapshot that spells one price two ways, such as `0.98` and `0.980`, no
  longer leaves a second level at that price which no later update can delete.
- The re-seed queue is sized for discovered tokens as well as requested ones, so
  a token the run picked up on its own cannot push a requested one onto the
  failure path when a disconnect fills the queue.
- A token file with an unexpectedly long line reports what is wrong with it
  rather than failing with a scanner internal error.

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
