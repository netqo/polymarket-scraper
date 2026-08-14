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

### Changed
### Deprecated
### Removed
### Fixed
### Security

[Unreleased]: https://github.com/netqo/polymarket-scraper/commits/dev
