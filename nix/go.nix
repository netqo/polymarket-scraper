# Go development module (CLI utilities, Postgres backends with pgx + sqlc).
#
# Returns a devShell and a Docker image from the same pinned toolchain.
# CI gates for Go projects: gofmt -l ., go vet ./..., golangci-lint run,
# go test ./... (see scaffolding CI template).
{ pkgs }:
let
  toolchain = with pkgs; [
    go
    gopls # LSP
    golangci-lint # CI linter aggregator
    delve # debugger (dlv), backs nvim-dap
    gotools # goimports and friends
    sqlc # SQL compiled to typed Go, no ORM
    govulncheck # vulnerability scanner (on-demand: make vuln)
    gotestsum # nicer test runner (the default `make test` for Go)
    air # live reload for the dev loop (make watch)
    mockgen # generate interface mocks for tests (go generate)
    # golang-migrate is intentionally NOT included: the nixpkgs `go-migrate`
    # build bundles the Snowflake driver, which panics on CA-cert parsing at
    # startup (fails on any invocation). Install it per project with only the
    # driver you need:
    #   go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
  ];
in
{
  devShell = pkgs.mkShell {
    packages = toolchain;
    shellHook = ''
      # Nicer gotestsum output by default (used by `make test`).
      export GOTESTSUM_FORMAT=testname
      echo "[*] go devShell: $(go version)" >&2
      echo "[*] test: gotestsum  |  lint: golangci-lint run  |  vuln: govulncheck ./...  |  watch: air" >&2
    '';
  };

  dockerImage = pkgs.dockerTools.buildImage {
    name = "go-dev";
    tag = "latest";
    copyToRoot = pkgs.buildEnv {
      name = "go-env";
      paths = toolchain;
      pathsToLink = [ "/bin" ];
    };
    config.Cmd = [
      "${pkgs.go}/bin/go"
      "version"
    ];
  };
}
