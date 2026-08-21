// Package logging renders the scraper's log records for two audiences at once.
//
// A run is watched by a person while it happens and read by an agent after it
// finishes, and those want different things. A person wants a prefix they can
// scan for and a colour they can ignore; an agent wants a stable line it can
// parse and a file it can tail without waiting for the process to exit. The
// handlers here compose to serve both from one logger: Handler renders, Tee
// fans out to several destinations with different settings, and Coalescer stops
// a repeating failure from burying everything else.
//
// slog has four levels and the prefix scheme names six symbols, so the two are
// deliberately kept apart. Levels decide what is written, which is what
// LOG_LEVEL and --log-level control. A Kind decides which symbol a record is
// written with, and is carried as an ordinary attribute. Inventing custom
// slog.Level values instead would have put "success" somewhere in the ordering
// between info and error, where it would silently change what a given
// LOG_LEVEL filters out.
package logging

import "log/slog"

// KindKey is the attribute that selects a record's prefix. It is removed from
// the rendered output, since the prefix already says what it says.
const KindKey = "kind"

// Kind is a presentational category that has no level of its own.
type Kind string

// The kinds. Records without one are rendered from their level alone.
const (
	// KindStep marks progress through a run: connecting, subscribing, sweeping.
	KindStep Kind = "step"

	// KindSuccess marks something completing as intended. It is deliberately
	// rare; most things working is the unremarkable case and says nothing.
	KindSuccess Kind = "success"

	// KindQuestion marks something the run cannot decide for itself and wants
	// a human or an agent to look at.
	KindQuestion Kind = "question"
)

// Step records progress at info level.
func Step(logger *slog.Logger, msg string, args ...any) {
	log(logger, KindStep, msg, args...)
}

// Success records that something completed as intended, at info level.
func Success(logger *slog.Logger, msg string, args ...any) {
	log(logger, KindSuccess, msg, args...)
}

// Question records something that needs a decision from outside the run, at
// info level.
func Question(logger *slog.Logger, msg string, args ...any) {
	log(logger, KindQuestion, msg, args...)
}

// log emits at info level with the kind attached.
//
// All three helpers are info records: a kind describes what a line is about,
// not how urgent it is, and giving them levels of their own would mean a
// consumer raising LOG_LEVEL to hide noise lost the successes as well.
func log(logger *slog.Logger, kind Kind, msg string, args ...any) {
	if logger == nil {
		return
	}

	logger.Info(msg, append(args, slog.String(KindKey, string(kind)))...)
}
