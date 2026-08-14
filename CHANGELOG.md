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

### Changed
### Deprecated
### Removed
### Fixed
### Security

[Unreleased]: https://github.com/netqo/polymarket-scraper/commits/dev
