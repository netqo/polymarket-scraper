# polymarket-scraper -- Claude Code instructions

Project-specific rules for Claude Code. These extend the user's global rules.

## Environment

- Toolchain is pinned by Nix (`flake.nix`) and loaded via direnv (`use flake`).
  Do NOT suggest asdf/mise/nvm/pyenv or global installs; add tools to the flake.
- Language: go. Runtime (if TS): bun. Backend (if any):
  none.
- Uniform commands via the Makefile: `make dev|build|test|lint|fmt|migrate|run`.

## Conventions (follow exactly)

- **Commits:** Conventional Commits (`feat`, `fix`, `refactor`, `docs`, `chore`,
  `ci`, `test`, `perf`, `build`, `style`, `revert`).
- **Branching:** Git Flow simplified: dev integrates work, main receives release merges (tagged); PRs to dev need green CI + 1 approval.
- **Secrets:** always via environment variables with a committed `.env.example`.
  Never hardcode secrets, API keys, or tokens.
- **Dependencies:** pin exact versions (never `^`/`~` ranges). Commit the
  lockfile.
- **Character set:** ASCII-only in code, comments, and commits (no em-dashes, emojis, or smart quotes).

## SQL / data access

- Default to hand-written, parameterized SQL. This is a preference, NOT a
  prohibition: use an ORM when it is genuinely necessary or more optimal in a
  specific case. Do not introduce an ORM by default.
- Go: `pgx` + `sqlc` (SQL compiled to typed Go) + `golang-migrate`.
- TypeScript (DBA-focused): `pg` + `node-pg-migrate` (SQL-file mode). NestJS
  product: Drizzle is the documented ORM option.

## Structured logging

- Go: `slog` (`time=... level=... msg=...`).
- Rust: `tracing` + `tracing-subscriber`, verbosity via `RUST_LOG`, console
  prefixes `[*]` info, `[!]` warning, `[+]` positive, `[-]` error.
- NestJS: `nestjs-pino`, correlated by `x-request-id`.

## NestJS specifics (if applicable)

- Fastify adapter (NOT Express). Validate env with Zod; DTO validation with
  `class-validator`. URI route versioning (`/api/v1`). Global exception filter
  shaped `{statusCode, message, error, requestId}`. Liveness health check.
  Graceful shutdown on `SIGTERM`. Feature-first module structure.

## Quality gates

Run `make lint` and `make test` before proposing a commit. CI must be green
before merge. A formatter diff fails CI.
