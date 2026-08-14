package main

import (
	"fmt"
	"io"
	"os"
)

// Process exit codes.
//
// Requirement A5 makes this contract binary for the consuming agent: 0 means
// the output file was written and is valid, and any non-zero code means the run
// failed and the output is unusable. Codes are added here as the paths that
// return them are implemented, so there is never a documented code that nothing
// can actually produce.
const (
	exitOK    = 0
	exitUsage = 2
)

// versionFlags are the spellings of the version flag accepted before any real
// argument parsing, so `--version` works even on a build with no valid config.
var versionFlags = map[string]bool{"--version": true, "-version": true}

// run is the real entry point: main does nothing but call it and exit, so that
// deferred cleanup actually runs.
//
// TODO(phase-1): replace the hand-rolled argument scan with internal/config,
// which owns the full flag set, the --help text (requirement F1), and the
// shutdown budget arithmetic (A4).
func run(args []string, stdout, stderr io.Writer) int {
	logger := newLogger(stderr, os.Getenv("LOG_LEVEL"))

	for _, arg := range args {
		if versionFlags[arg] {
			fmt.Fprintln(stdout, buildVersion())
			return exitOK
		}
	}

	logger.Error("scraping is not implemented yet",
		"detail", "this build only supports --version; the collector lands in the next change")

	return exitUsage
}
