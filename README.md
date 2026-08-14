# polymarket-scraper

<!-- Badges: CI, language, license, and any infra the project uses. -->
![CI](https://github.com/net/polymarket-scraper/actions/workflows/ci.yml/badge.svg)
![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)
![Conventional Commits](https://img.shields.io/badge/Conventional%20Commits-1.0.0-yellow.svg)

## Overview

One or two sentences on what this does and why it exists.

## Build / Configuration

Requires Nix with flakes and direnv (see the dev-setup repo). The pinned
toolchain loads automatically on `cd` via direnv, or manually with `nix develop`.

```bash
direnv allow      # first time
# or
nix develop
```

Configuration is via environment variables. Copy `.env.example` to `.env`:

| Variable       | Required | Description                    |
|----------------|----------|--------------------------------|
| DATABASE_URL   | yes      | Postgres connection string     |
| LOG_LEVEL      | no       | debug/info/warn/error          |

## Usage

```bash
make run          # run locally
# real example of invoking the app goes here
```

## Quality

```bash
make fmt          # format
make lint         # lint (fails on findings)
make test         # tests
```

CI runs the same gates and fails on any formatter diff.

## Conventions

- Conventional Commits; `main` + topic branches (Git Flow simplified: dev integrates work, main receives release merges (tagged); PRs to dev need green CI + 1 approval).
- Secrets via env var with a committed `.env.example` (never hardcoded).
- Dependencies pinned exactly (no `^` ranges); lockfile committed.
- Structured logging.

## Roadmap

- [ ] ...

## License

MIT. See `LICENSE`.
