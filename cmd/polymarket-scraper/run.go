package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/netqo/polymarket-scraper/internal/config"
	"github.com/netqo/polymarket-scraper/internal/tokenlist"
)

// Process exit codes.
//
// Requirement A5 makes this contract binary for the consuming agent: 0 means
// the output document was written and is valid, and any non-zero code means the
// run failed and the output is unusable. Codes are added here as the paths that
// return them are implemented, so there is never a documented code that nothing
// can actually produce.
const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

// run is the real entry point: main does nothing but call it and exit, so that
// deferred cleanup actually runs.
func run(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Parse(args, os.LookupEnv)
	switch {
	case errors.Is(err, config.ErrHelp):
		fmt.Fprint(stdout, config.Usage())
		return exitOK

	case errors.Is(err, config.ErrVersion):
		fmt.Fprintln(stdout, buildVersion())
		return exitOK

	case err != nil:
		// Usage problems are reported as plain text rather than through the
		// logger: they happen before logging is configured, and the reader is
		// a person who just mistyped a flag.
		fmt.Fprintf(stderr, "%s\n\nRun with --help for usage.\n", err)
		return exitUsage
	}

	logger := newLogger(stderr, cfg.LogLevel)

	tokens, err := tokenlist.Load(cfg.TokensPath)
	if err != nil {
		logger.Error("cannot read the token list", "path", cfg.TokensPath, "error", err)
		return exitUsage
	}

	logTokenListAnomalies(logger, tokens)

	// TODO(phase-5): hand the token list and configuration to the engine.
	logger.Error("collection is not implemented yet",
		"tokens", len(tokens.IDs),
		"detail", "this build parses its configuration and token list; the collector lands in a later change")

	return exitFailed
}

// logTokenListAnomalies reports what was odd about the token list.
//
// Neither condition is fatal. Duplicates are collapsed because requirement C4
// wants each token reported exactly once, and an id that does not look like a
// token id is still collected so that it can fail visibly rather than vanish.
func logTokenListAnomalies(logger *slog.Logger, tokens tokenlist.List) {
	if tokens.Duplicates > 0 {
		logger.Warn("collapsed duplicate token ids",
			"duplicates", tokens.Duplicates, "unique", len(tokens.IDs))
	}
	if len(tokens.Suspicious) > 0 {
		logger.Warn("some ids do not look like token ids and will probably fail",
			"count", len(tokens.Suspicious), "first", tokens.Suspicious[0])
	}
}
