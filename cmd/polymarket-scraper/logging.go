package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/netqo/polymarket-scraper/internal/config"
	"github.com/netqo/polymarket-scraper/internal/logging"
)

// Log levels accepted in LOG_LEVEL, lowercased.
const (
	levelDebug = "debug"
	levelInfo  = "info"
	levelWarn  = "warn"
	levelError = "error"
)

// logFileMode matches the output document's permissions. A log names the tokens
// a run collected and the errors it hit, so there is no reason for it to be
// readable more widely than the document it explains.
const logFileMode = 0o600

// consoleValueLimit is how much of one attribute value reaches the terminal.
//
// Chosen to be about two lines on a normal terminal: long enough for a file
// path or the start of a frame, short enough that one enormous value cannot
// scroll the rest of the run out of view. The log file applies no limit, so
// nothing is actually lost by cutting it here.
const consoleValueLimit = 300

// processLogger is the run's logger together with the cleanup its destinations
// need.
//
// It is a type rather than a bare *slog.Logger because two things now outlive
// the last log call: a repeated message may still be waiting for its count, and
// a log file may still be open. Both have to be dealt with before the process
// exits, and a caller cannot be expected to know that.
type processLogger struct {
	logger    *slog.Logger
	coalescer *logging.Coalescer
	file      *os.File
}

// newProcessLogger builds the process logger.
//
// Records always go to stderr (requirement A7): stdout is reserved for the
// single machine-readable summary line, so a consumer can treat "stdout is
// non-empty" as a reliable success signal.
//
// When a log file is configured the same records go to both, rendered
// differently. The terminal gets colour when it is a terminal; the file never
// does, because escape codes in a file an agent parses are noise it has to
// strip back out.
func newProcessLogger(stderr io.Writer, cfg config.Config) (*processLogger, error) {
	level := parseLevel(cfg.LogLevel)

	console := logging.New(stderr, logging.Options{
		Level: level,
		// A decode failure quotes the frame it could not read, and a frame runs
		// to kilobytes. On a terminal that scrolls everything else away, so the
		// value is cut to roughly two lines here and left whole in the file.
		MaxValueLength: consoleValueLimit,
	})

	// Declared as the interface, not as *logging.Handler: a nil pointer in a
	// non-nil interface would defeat the nil check inside NewTee and panic on
	// the first record.
	var (
		file        *os.File
		fileHandler slog.Handler
	)
	if cfg.LogFile != "" {
		opened, err := openLogFile(cfg.LogFile)
		if err != nil {
			return nil, err
		}

		file = opened
		fileHandler = logging.New(file, logging.Options{
			Level:  level,
			Colour: logging.ColourNever,
		})
	}

	// Coalescing wraps the fan-out rather than each destination, so a repeat is
	// counted once and both destinations agree about how many there were.
	coalescer := logging.NewCoalescer(logging.NewTee(console, fileHandler))

	return &processLogger{
		logger:    slog.New(coalescer).With(runKey, newRunID()),
		coalescer: coalescer,
		file:      file,
	}, nil
}

// runKey is the attribute identifying which run a record belongs to.
const runKey = "run"

// runIDBytes is how much randomness a run identifier carries. Four bytes is
// eight hex characters: short enough not to crowd every line, and far more than
// enough to tell apart the handful of runs that might share one log file.
const runIDBytes = 4

// newRunID returns a short random identifier for this run.
//
// Every record carries it, which is what makes an appended log file usable:
// successive runs write into the same file, and without this their lines would
// be indistinguishable once the timestamps got close together.
//
// Random rather than a counter or a timestamp. A counter would need state kept
// between invocations, which the process does not have, and two runs launched
// by the same script can share a millisecond.
func newRunID() string {
	var raw [runIDBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// Not being able to name the run is not a reason to refuse to do it.
		return "unknown"
	}

	return hex.EncodeToString(raw[:])
}

// openLogFile opens the run's log for appending.
//
// Appending rather than truncating: a run is often one of a series, and losing
// the previous run's log is exactly the wrong thing to do to whoever is trying
// to work out when a problem started.
func openLogFile(path string) (*os.File, error) {
	// #nosec G304 -- the path is an operator-supplied command line argument;
	// writing to an arbitrary file is the entire purpose of the flag.
	file, err := os.OpenFile(filepath.Clean(path), os.O_APPEND|os.O_CREATE|os.O_WRONLY, logFileMode)
	if err != nil {
		return nil, fmt.Errorf("cannot open the log file %s: %w", path, err)
	}

	return file, nil
}

// Close releases anything still held and closes the log file.
//
// The flush matters: a failure that repeated until the moment the run ended
// would otherwise be reported without its count, which is the one case where
// the count is most worth having.
func (p *processLogger) Close() {
	if p == nil {
		return
	}

	_ = p.coalescer.Flush()
	if p.file != nil {
		_ = p.file.Close()
	}
}

// parseLevel maps a LOG_LEVEL string to a slog level, defaulting to info.
//
// An unrecognised value is not an error: refusing to start because of a typo in
// an optional environment variable would be a worse failure than logging at the
// default level.
func parseLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case levelDebug:
		return slog.LevelDebug
	case levelWarn:
		return slog.LevelWarn
	case levelError:
		return slog.LevelError
	default:
		// levelInfo lands here too: naming it separately would be a second
		// branch returning the same thing as the default.
		return slog.LevelInfo
	}
}
