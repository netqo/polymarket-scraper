package main

import (
	"io"
	"log/slog"
	"strings"
)

// Log levels accepted in LOG_LEVEL, lowercased.
const (
	levelDebug = "debug"
	levelInfo  = "info"
	levelWarn  = "warn"
	levelError = "error"
)

// newLogger builds the process logger.
//
// Every record goes to w, which is always stderr in production (requirement
// A7): stdout is reserved for the single machine-readable summary line, so a
// consumer can treat "stdout is non-empty" as a reliable success signal.
//
// The handler is text, not JSON, on purpose. These strings are read by a human
// when something breaks and are copied verbatim into the consuming agent's
// data_issues list (F4), so they need to be legible first and parseable second.
func newLogger(w io.Writer, levelName string) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: parseLevel(levelName),
	}))
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
