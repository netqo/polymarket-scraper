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
.PHONY: help dev build test lint fmt migrate run clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

dev: ## Enter the pinned dev shell
	nix develop

# migrate and run stay project-specific: they depend on the app's entrypoint and
# migration tool. They fail loudly rather than printing a TODO and exiting 0,
# so a half-configured project cannot look like a passing one.
migrate: ## Apply database migrations (fill this in for the project)
	@echo "migrate is not configured: edit the migrate target in the Makefile" >&2; exit 1

run: ## Run the app locally (fill this in for the project)
	@echo "run is not configured: edit the run target in the Makefile" >&2; exit 1

clean: ## Remove build artifacts
	rm -rf result result-* dist build target

# Go targets (tools pinned by nix/flakes/go.nix). build/test/lint/fmt mirror the
# CI gates in github-workflows/gates/go.yml.
.PHONY: build test lint fmt vuln watch mocks

build: ## Build all packages
	go build ./...

test: ## Run tests (gotestsum wraps go test; GOTESTSUM_FORMAT set in the devShell)
	gotestsum -- ./...

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
