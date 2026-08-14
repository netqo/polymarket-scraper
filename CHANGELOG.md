# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Nothing has been released yet. The first release will be v0.1.0, cut once every
acceptance check in the conformance specification passes.

## [Unreleased]

### Added
- Project scaffold: pinned Nix devShell, Makefile, golangci-lint v2
  configuration, CI workflow, PR and issue templates, CODEOWNERS.
- Nix package and release container image built from the same pinned toolchain
  as the devShell, replacing the scaffold's placeholder toolchain image.
- CLI entry point with `--version`, structured logging to stderr via `slog`.
- `internal/decimal`: a value type that keeps the API's decimal text byte for
  byte while exposing a fixed-point integer for comparison only, so no price or
  size can pick up a floating-point artifact on its way through the scraper.
- `internal/tokenlist`: token file loading in both supported formats, with
  duplicates collapsed once at the source and malformed ids kept rather than
  dropped, so they can fail visibly instead of vanishing.
- `internal/config`: the full flag set, the hand-written `--help` text, and the
  shutdown timeline as a pure function of the window and grace period.
- `internal/book`: a two-sided order book that sorts on ingest rather than
  trusting the wire order, keeps both sides in output order at all times, and
  treats a size of zero as removing a level rather than zeroing it.
- `internal/wire`: decoding for every market channel event in both the object
  and array framings, the subscription messages, and the REST book response,
  with unknown event types and unknown fields tolerated rather than fatal.
- `internal/tracker`: the per-token trust state machine, which keeps a book out
  of the output entirely unless it is current, plus constant-space volatility
  statistics over the collection window.
- `internal/report` and `SCHEMA.md`: the output contract at `schema_version`
  1.0, with the atomic write, the stdout summary line, and tests that hold the
  documentation and the code to each other.
- `internal/restclient`: batched and single book fetches, paced by a shared
  limiter, with bounded retries and a distinct answer for a token the exchange
  does not recognise.
- `internal/testsupport`: an in-process stand-in for the REST endpoints, so the
  suite never touches the network.
- `--rest-only` collection end to end. The binary now produces a real output
  document.
- `internal/wsclient`: one websocket connection, with the literal-text
  keepalive, the byte-exact subscription, per-read idle detection, and a count
  of snapshots received so a silently incomplete subscription is visible.
- A scripted fake market channel in `internal/testsupport`, able to drop a
  connection, go silent, or accept a subscription and never answer it.

- Websocket collection end to end: reconnection, per-shard single-writer book
  state, batched re-seeding, the deterministic shutdown timeline, and the
  watchdog that terminates a run which will not stop.
- Connection sharding, so a token list wider than one subscription is split
  across connections rather than silently truncated.
- Market announcements and resolutions are reported in the document's `events`
  block, deduplicated across connections.
- Tokens announced mid-window are subscribed to and reported, bounded by
  `--discover-limit` and by the width of the connection.

### Changed
- `make test` and the CI test gate now run under the race detector.
- The drain after a collection window ends as soon as no REST work is
  outstanding, rather than always waiting its full allowance.

### Deprecated
### Removed
### Fixed
### Security

[Unreleased]: https://github.com/netqo/polymarket-scraper/commits/dev
