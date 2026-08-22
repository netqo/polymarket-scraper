# Uniform interface over each language's real commands: the same target names
# work in every project regardless of toolchain. Prefer running inside the Nix
# devShell (direnv activates it automatically).
#
# This base file holds only what is language-agnostic. create-project.sh appends
# the language's own targets from scaffolding/make/<lang>.mk below, which is
# where build/test/lint/fmt are defined. Those recipes deliberately mirror the
# CI gates in scaffolding/github-workflows/gates/<lang>.yml, so a green `make
# lint && make test` locally means a green pipeline.

.DEFAULT_GOAL := help
.PHONY: help dev run clean image

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

dev: ## Enter the pinned dev shell
	nix develop

# There is no `migrate` target: this project has no database. It reads public
# market data and writes one JSON file.

run: ## Run the scraper locally, e.g. make run ARGS="--tokens t.txt --duration 30 --out books.json"
	go run ./cmd/polymarket-scraper $(ARGS)

image: ## Build the release container image from Nix (never docker build)
	nix build .#dockerImage

clean: ## Remove build artifacts
	rm -rf result result-* dist build target

# Go targets (tools pinned by nix/flakes/go.nix). build/test/lint/fmt mirror the
# CI gates in github-workflows/gates/go.yml.
.PHONY: build test test-live soak acceptance-kill lint fmt vuln watch mocks

build: ## Build all packages
	go build ./...

# -race is not optional here. The collector runs a goroutine per connection and
# per shard, and a data race in book state is exactly the kind of bug that
# survives review, passes every run on a developer machine, and corrupts one
# token's book in production.
test: ## Run tests under the race detector
	gotestsum -- -race ./...

# Live tests reach the real Polymarket API and are a manual gate, never a CI
# gate. They skip themselves unless POLYMARKET_LIVE_TOKENS points at a token
# file. -count=1 defeats the test cache, which is meaningless for live data.
test-live: ## Run the //go:build live acceptance tests against the real API
	@test -n "$$POLYMARKET_LIVE_TOKENS" || \
		{ echo "set POLYMARKET_LIVE_TOKENS to a token file first" >&2; exit 1; }
	gotestsum -- -tags live -count=1 -timeout 10m ./...

# One live run, checked against what a healthy run looks like. Neither a CI gate
# nor a test: it is anomaly detection against a measured baseline, meant to be
# run repeatedly, by hand or on a timer, and to say nothing unless something
# deviates. Fetches its own token ids unless POLYMARKET_LIVE_TOKENS names a file.
soak: ## Run one live collection and check it against the healthy baseline
	scripts/soak.sh

# Kills the process with SIGKILL through a run and checks that the output path
# is never observed truncated. SIGTERM is handled and would prove nothing; the
# uncatchable signal is what tests the filesystem-level guarantee.
acceptance-kill: ## Check that a killed run never leaves a partial document
	nix build .#default -o result-bin
	BINARY=./result-bin/bin/polymarket-scraper scripts/acceptance-kill.sh

lint: ## CI gates: gofmt diff, go vet, golangci-lint
	@test -z "$$(gofmt -l .)" || { echo "gofmt diff in:" >&2; gofmt -l . >&2; exit 1; }
	go vet ./...
	golangci-lint run

fmt: ## Format in place
	gofmt -w .

vuln: ## Go: scan dependencies for known vulnerabilities
	govulncheck ./...

watch: ## Go: live-reload the app on file changes
	air

mocks: ## Go: (re)generate interface mocks (runs //go:generate directives)
	go generate ./...
