package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/netqo/polymarket-scraper/internal/config"
	"github.com/netqo/polymarket-scraper/internal/engine"
	"github.com/netqo/polymarket-scraper/internal/logging"
	"github.com/netqo/polymarket-scraper/internal/report"
	"github.com/netqo/polymarket-scraper/internal/stream"
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

	// exitWatchdog means the run would not shut down and the process had to be
	// terminated. No output document exists at that point, which is the point:
	// a run that overran its budget has not produced trustworthy data and must
	// not look as though it has.
	exitWatchdog = 3
)

// run is the real entry point: main does nothing but call it and exit, so that
// deferred cleanup actually runs.
func run(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Parse(args, os.LookupEnv)
	switch {
	case errors.Is(err, config.ErrHelp):
		fmt.Fprint(stdout, config.Usage())
		return exitOK

	case errors.Is(err, config.ErrHelpJSON):
		document, encodeErr := config.UsageJSON()
		if encodeErr != nil {
			fmt.Fprintf(stderr, "%s\n", encodeErr)
			return exitFailed
		}
		fmt.Fprintln(stdout, document)
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

	// The log file is opened before any work starts, so a path that cannot be
	// written is a usage error rather than something discovered at the end of a
	// run that has already spent its window.
	proc, err := newProcessLogger(stderr, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return exitUsage
	}
	defer proc.Close()

	logger := proc.logger

	// Recorded before anything can fail, so that even a run that dies loading
	// its token list leaves behind what it was asked to do.
	logging.Step(logger, "starting", logging.Cat(logging.CategoryStartup),
		"version", buildVersion(), "config", cfg)

	tokens, err := tokenlist.Load(cfg.TokensPath)
	if err != nil {
		logger.Error("cannot read the token list", "path", cfg.TokensPath, "error", err)
		return exitUsage
	}
	logTokenListAnomalies(logger, tokens)

	// Opened alongside the log file and closed with it: both outlive the
	// collection they describe, and both are a usage error if the path cannot
	// be written rather than something discovered at the end of a run.
	changes, err := openStream(cfg, proc.runID())
	if err != nil {
		logger.Error("cannot open the change stream", "path", cfg.StreamPath, "error", err)
		return exitUsage
	}
	defer closeStream(logger, changes)

	collector, err := engine.New(engine.Options{
		Config: cfg,
		Tokens: tokens,
		Logger: logger,
		Stream: changes,
		// The watchdog is the only hard guarantee that the process ends on
		// time: a goroutine blocked in a syscall will never observe a cancelled
		// context, so nothing short of terminating can be relied upon.
		Halt: func() { os.Exit(exitWatchdog) },
	})
	if err != nil {
		logger.Error("cannot start the collector", "error", err)
		return exitUsage
	}

	// A run that is interrupted still writes an honest document rather than
	// nothing: the tokens it did not reach say so in their own status, which is
	// more useful to a consumer than an empty output path.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	document, err := collector.Run(ctx)
	if err != nil {
		logger.Error("the run failed", "error", err)
		return exitFailed
	}

	if err := report.WriteAtomic(cfg.OutPath, document); err != nil {
		logger.Error("cannot write the output document", "path", cfg.OutPath, "error", err)
		return exitFailed
	}

	// The summary line goes to stdout only here, on the success path, which is
	// what makes non-empty stdout a reliable success signal on its own.
	fmt.Fprintln(stdout, report.SummaryLine(document, cfg.OutPath))

	return exitOK
}

// logTokenListAnomalies reports what was odd about the token list.
//
// Neither condition is fatal. Duplicates are collapsed because requirement C4
// wants each token reported exactly once, and an id that does not look like a
// token id is still collected so that it can fail visibly rather than vanish.
func logTokenListAnomalies(logger *slog.Logger, tokens tokenlist.List) {
	if tokens.Duplicates > 0 {
		logger.Warn("collapsed duplicate token ids", logging.Cat(logging.CategoryStartup),
			"duplicates", tokens.Duplicates, "unique", len(tokens.IDs))
	}
	if len(tokens.Suspicious) > 0 {
		logger.Warn("some ids do not look like token ids and will probably fail",
			logging.Cat(logging.CategoryStartup),
			"count", len(tokens.Suspicious), "first", tokens.Suspicious[0])
	}
}

// openStream opens the change stream, or returns nil when none was configured.
func openStream(cfg config.Config, runID string) (*stream.Writer, error) {
	if cfg.StreamPath == "" {
		return nil, nil
	}

	return stream.New(stream.Options{
		Path:      cfg.StreamPath,
		Run:       runID,
		StartedAt: time.Now(),
	})
}

// closeStream closes the stream and says so if anything was lost.
//
// A dropped record is reported rather than swallowed: a reader that does not
// know the stream is incomplete will assume it is complete, which is the same
// mistake the output document goes to some length to prevent.
func closeStream(logger *slog.Logger, changes *stream.Writer) {
	if changes == nil {
		return
	}

	if dropped := changes.Dropped(); dropped > 0 {
		logger.Warn("the change stream is incomplete", "dropped", dropped)
	}
	if err := changes.Close(); err != nil {
		logger.Error("cannot close the change stream", "error", err)
	}
}
