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

Status: released, v0.1.0. The output contract is frozen at `schema_version` 1.0.

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

### Watching a run

`--log-file` appends every record to a file as the run happens, so a run can be
followed from somewhere other than the terminal that launched it:

```bash
polymarket-scraper --tokens tokens.txt --out books.json --log-file run.log &
tail -f run.log
```

Each line carries a timestamp, a prefix and the run's identifier: `[!]` error or
warning, `[*]` info, `[~]` step or detail, `[+]` success, `[?]` something needing
a decision. Colour is used only when stderr is a terminal, so piped output and
the file itself stay plain. A message that repeats is written once and then
summarised as `message (xN)` when the repeats stop, which is what keeps a
connection failing in a loop from burying everything else.

The file is opened for appending at mode `0600`, so successive runs accumulate
in it and the run identifier is what tells them apart. It is the place to look
for anything the output document had to shorten.

## Quality

```bash
make fmt             # format
make lint            # gofmt diff, go vet, golangci-lint (fails on findings)
make test            # offline suite under the race detector, no network
make test-live       # //go:build live tests against the real API, manual gate
make acceptance-kill # SIGKILL a run repeatedly, check the output is never partial
make image           # build the release container image from Nix
```

CI runs `lint` and `test` and fails on any formatter diff. The test suite runs
under the race detector, because a data race in book state would corrupt one
token's book while every other signal stayed green.

The other two are manual gates. `test-live` reaches the real Polymarket API and
skips itself unless `POLYMARKET_LIVE_TOKENS` names a token file; it exists
because the offline suite can only prove the scraper behaves as designed against
a server that behaves as expected, and every expensive surprise so far has been
a place where the exchange differs from its own documentation.
`acceptance-kill` kills the process with SIGKILL at a spread of moments through
a run and checks that the output path is never observed truncated.

## Conventions

- Conventional Commits. Git Flow simplified: `dev` integrates work, `main`
  receives tagged release merges, and PRs to `dev` need green CI.
- Dependencies pinned exactly; `go.sum` and `flake.lock` committed.
- Structured logging with `log/slog`, always to stderr, rendered by
  `internal/logging`: one prefixed line per record, colour only on a terminal,
  and repeats collapsed into a count.
- ASCII-only in code, comments, and commit messages.
- Nix builds, Docker runs: the release image is produced by `nix build`, and
  there is no Dockerfile.

## License

MIT. See `LICENSE`.
