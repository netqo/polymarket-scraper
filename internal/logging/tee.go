package logging

import (
	"context"
	"errors"
	"log/slog"
)

// Tee delivers every record to several handlers.
//
// It exists because the terminal and the log file want the same records
// rendered differently: colour on one and not the other, truncated values on
// one and complete ones on the other, and often different levels, since the
// file is the thing an agent reads afterwards and can afford to be verbose.
// An io.MultiWriter cannot express that, because by the time bytes reach a
// writer every one of those decisions has already been made.
type Tee struct {
	handlers []slog.Handler
}

// NewTee builds a Tee over the given handlers. Nil handlers are dropped, so a
// caller can pass an optional destination without guarding the call.
func NewTee(handlers ...slog.Handler) *Tee {
	kept := make([]slog.Handler, 0, len(handlers))
	for _, handler := range handlers {
		if handler != nil {
			kept = append(kept, handler)
		}
	}

	return &Tee{handlers: kept}
}

// Enabled reports whether any destination wants the level.
//
// slog.Logger consults this once before building a record, so it has to be the
// union. Handle then asks each handler again, which is what allows the file to
// be at debug while the terminal is at info.
func (t *Tee) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range t.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}

	return false
}

// Handle delivers the record to every destination that wants it.
//
// Each handler receives its own clone. slog documents that a handler must not
// retain or modify a record, but a fan-out is exactly where a violation of that
// would turn into one destination corrupting another's output, and a clone is
// cheap next to the write that follows it.
func (t *Tee) Handle(ctx context.Context, record slog.Record) error {
	var failures []error

	for _, handler := range t.handlers {
		if !handler.Enabled(ctx, record.Level) {
			continue
		}
		if err := handler.Handle(ctx, record.Clone()); err != nil {
			failures = append(failures, err)
		}
	}

	return errors.Join(failures...)
}

// WithAttrs implements slog.Handler.
func (t *Tee) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return t
	}

	derived := make([]slog.Handler, len(t.handlers))
	for i, handler := range t.handlers {
		derived[i] = handler.WithAttrs(attrs)
	}

	return &Tee{handlers: derived}
}

// WithGroup implements slog.Handler.
func (t *Tee) WithGroup(name string) slog.Handler {
	if name == "" {
		return t
	}

	derived := make([]slog.Handler, len(t.handlers))
	for i, handler := range t.handlers {
		derived[i] = handler.WithGroup(name)
	}

	return &Tee{handlers: derived}
}
