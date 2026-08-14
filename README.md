# polymarket-scraper

![CI](https://github.com/netqo/polymarket-scraper/actions/workflows/ci.yml/badge.svg)
![Go](https://img.shields.io/badge/Go-1.26-00ADD8.svg)
![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)
![Conventional Commits](https://img.shields.io/badge/Conventional%20Commits-1.0.0-yellow.svg)

## Overview

A headless CLI that connects to the Polymarket CLOB market websocket, maintains
order books for a given list of outcome tokens over a fixed time window, and
writes one atomic JSON document describing the final state of every token it was
asked about.

It is a deliberately dumb, honest pipe. It does not filter, rank, or score
markets, compute edges, yields, or fees, fetch Gamma metadata, authenticate, or
place orders. All judgement belongs to the agent that reads the output.

Its one hard promise is the reason the whole thing exists: **a book reported as
current really is current.** After any disconnect or suspected gap, a token is
untrusted until it has been re-seeded, and if re-seeding fails the token is
reported as `resync_failed` rather than having its pre-gap book passed off as
fresh. A silently stale book turns directly into a fake arbitrage signal.

Status: under construction.

## Build / Configuration

Requires Nix with flakes and direnv. The pinned toolchain loads automatically on
`cd` via direnv, or manually with `nix develop`.

```bash
direnv allow      # first time
# or
nix develop
```

This project is **credential-free by design** (requirement A6): no API keys, no
wallet material, no order endpoints, ever. Configuration is entirely via CLI
flags, with two optional environment variables:

| Variable                 | Required | Description                                    |
|--------------------------|----------|------------------------------------------------|
| `LOG_LEVEL`              | no       | `debug`/`info`/`warn`/`error` (default `info`) |
| `POLYMARKET_LIVE_TOKENS` | no       | Token file path; enables `make test-live`      |

## Usage

```bash
polymarket-scraper --tokens tokens.txt --duration 90 --out books.json
```

`--tokens` accepts one token id per line, or a JSON array of token id strings.
Token ids are long decimal strings and are always treated as strings, never as
numbers. Logs go to stderr; stdout carries only a single summary line on
success, so a non-empty stdout is a reliable success signal.

Run `polymarket-scraper --help` for the authoritative flag list. The output
contract is documented in `SCHEMA.md`, which is the file to paste into a
consuming agent's prompt.

## Quality

```bash
make fmt          # format
make lint         # gofmt diff, go vet, golangci-lint (fails on findings)
make test         # offline test suite, no network
make test-live    # //go:build live tests against the real API, manual gate
make image        # build the release container image from Nix
```

CI runs the same gates and fails on any formatter diff. The live tests never run
in CI: they reach the real Polymarket API and skip themselves unless
`POLYMARKET_LIVE_TOKENS` is set.

## Conventions

- Conventional Commits. Git Flow simplified: `dev` integrates work, `main`
  receives tagged release merges, and PRs to `dev` need green CI.
- Dependencies pinned exactly; `go.sum` and `flake.lock` committed.
- Structured logging with `log/slog`, text handler, always to stderr.
- ASCII-only in code, comments, and commit messages.
- Nix builds, Docker runs: the release image is produced by `nix build`, and
  there is no Dockerfile.

## Deviations from the spec

The scraper is built against a written conformance specification. It is followed
except where noted here, and each deviation is deliberate.

- **F5 (single-file Python or Node script).** Implemented in Go instead. F5's
  stated intent is "boring, pinned dependencies... simple beats clever when it
  breaks at 3am". A statically linked binary with a committed `go.sum` and a
  Nix-pinned toolchain serves that intent better than a virtualenv, at the cost
  of not being readable in a single file.
- **D4 (monotonicity guard), read literally.** The feed carries no sequence
  numbers, only a timestamp and a hash, so "if ordering cannot be established,
  resync" would turn one server clock quirk into a resync storm across every
  token at once. Instead: per-connection delivery order is authoritative, exact
  duplicates are dropped, and timestamp regressions trigger a resync only beyond
  `--reorder-tolerance` (default 5s). Smaller regressions are flagged, not
  hidden.
- **E2 (~10 requests/second).** Kept as the default for politeness, but it is
  far below Polymarket's documented limits, so it is exposed as `--rest-rate`.

## Roadmap

- [ ] Value types, token list loading, flags and shutdown budget
- [ ] Protocol decoding and the order book
- [ ] Per-token resync state machine
- [ ] Output document and `SCHEMA.md`
- [ ] REST client and `--rest-only` mode
- [ ] Websocket transport
- [ ] Engine, reconnection and deterministic shutdown
- [ ] Connection sharding
- [ ] Event capture and error reporting
- [ ] Dynamic mid-window subscription
- [ ] Acceptance suite and v0.1.0

## License

MIT. See `LICENSE`.
