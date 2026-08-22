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
wallet material, no order endpoints, ever.

Settings come from four places, each overriding the one before it: the mode's
bundle of defaults, a TOML file, the environment, then command line flags. A
flag always wins, because the file is the considered configuration and a flag is
what someone typed for one run.

```bash
cp polymarket-scraper.example.toml polymarket-scraper.toml
```

A file with that name in the working directory is picked up automatically;
anywhere else, name it with `--config FILE`. The example is fully commented and
every value in it is the default, so a copy with nothing changed behaves exactly
like running with no file at all -- there is a test that keeps that true. An
unknown setting is an error rather than a warning, because a typo that was
silently ignored looks identical to a setting that had no effect.

Tuning that has no flag lives in the file alone: retry counts and timeouts,
backoffs, worker counts, and the bounds on the output document's lists. Those
were constants compiled into the binary until they were consolidated here, which
is the point: changing one no longer means rebuilding.

| Variable                 | Required | Description                                       |
|--------------------------|----------|---------------------------------------------------|
| `LOG_LEVEL`              | no       | `debug`/`info`/`warn`/`error` (default `info`)    |
| `POLYMARKET_MODE`        | no       | `production` or `debug`; same as `--mode`         |
| `POLYMARKET_CONFIG`      | no       | Settings file path; same as `--config`            |
| `POLYMARKET_LIVE_TOKENS` | no       | Token file path; enables `make test-live`         |

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
consuming agent's prompt. `STREAM.md` documents the optional
real-time output. `PROTOCOL.md` describes the other side: what the exchange
sends, and the several places where it disagrees with its own published
documentation.

### Reading a run as it happens

`--stream` appends the run's changes to a file, one JSON object per line, so
something can act on them during the window rather than waiting for the document:

```bash
polymarket-scraper --tokens tokens.txt --out books.json --stream changes.jsonl &
tail -f changes.jsonl
```

Quotes are written when a top of book moves, alongside trades, flags and
announcements. The document remains the contract and carries the guarantees; the
stream is the same run told as it happens and carries none of them.
`STREAM.md` is its contract.

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

Records are grouped into categories that can be switched off independently in
the settings file, under `[logging.categories]`: `startup`, `progress`,
`connection`, `flags`, `rest`, `decode` and `discovery`. Level is the wrong axis
for that choice, because wanting every per-token flag and none of the keepalive
chatter is a question about subject matter, and both of those records sit at the
same level. **Errors are never affected by these switches**, so a run that has
been quietened still says when it breaks.

## Quality

```bash
make fmt             # format
make lint            # gofmt diff, go vet, golangci-lint (fails on findings)
make test            # offline suite under the race detector, no network
make test-live       # //go:build live tests against the real API, manual gate
make soak            # one live run, checked against the healthy baseline
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

`soak` runs one live collection and checks it against what a healthy run looks
like: every requested token current, no errors, no reconnects, and no flag
outside a set that was measured rather than guessed. It says nothing unless
something deviates, which makes it usable on a timer. It is anomaly detection
against a baseline, not a test: it catches "the exchange changed", and the
offline suite catches "the logic is wrong". Neither replaces the other.

## Conventions

- Conventional Commits. Git Flow simplified: `dev` integrates work, `main`
  receives tagged release merges, and PRs to `dev` need green CI.
- Dependencies pinned exactly; `go.sum` and `flake.lock` committed.
- Connections are sharded well below the width at which the server silently
  stops honouring a subscription, and another is opened if discovery fills the
  ones that exist.
- Structured logging with `log/slog`, always to stderr, rendered by
  `internal/logging`: one prefixed line per record, colour only on a terminal,
  and repeats collapsed into a count.
- ASCII-only in code, comments, and commit messages.
- Nix builds, Docker runs: the release image is produced by `nix build`, and
  there is no Dockerfile.

## License

MIT. See `LICENSE`.
