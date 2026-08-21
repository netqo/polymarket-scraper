package logging

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// failingHandler reports an error from Handle, for checking that one broken
// destination neither hides the others nor swallows its own failure.
type failingHandler struct{ err error }

func (failingHandler) Enabled(context.Context, slog.Level) bool    { return true }
func (f failingHandler) Handle(context.Context, slog.Record) error { return f.err }
func (f failingHandler) WithAttrs([]slog.Attr) slog.Handler        { return f }
func (f failingHandler) WithGroup(string) slog.Handler             { return f }

func TestTeeDeliversToEveryDestination(t *testing.T) {
	var first, second bytes.Buffer

	logger := slog.New(NewTee(
		New(&first, Options{}),
		New(&second, Options{}),
	))
	logger.Info("collecting", "tokens", 400)

	for name, buf := range map[string]*bytes.Buffer{"first": &first, "second": &second} {
		if !strings.Contains(buf.String(), "collecting tokens=400") {
			t.Errorf("%s destination = %q, want the record", name, buf.String())
		}
	}
}

// The whole reason a Tee exists rather than an io.MultiWriter: the file an agent
// reads afterwards can afford to be verbose while the terminal stays quiet.
func TestTeeAppliesEachDestinationsOwnLevel(t *testing.T) {
	var terminal, file bytes.Buffer

	logger := slog.New(NewTee(
		New(&terminal, Options{Level: slog.LevelInfo}),
		New(&file, Options{Level: slog.LevelDebug}),
	))

	logger.Debug("applying a delta", "token", "111")

	if terminal.Len() != 0 {
		t.Errorf("the info destination took a debug record: %q", terminal.String())
	}
	if !strings.Contains(file.String(), "applying a delta") {
		t.Errorf("the debug destination missed the record: %q", file.String())
	}
}

// slog.Logger consults Enabled once before building a record, so a Tee has to
// answer for the union or the verbose destination never sees anything.
func TestTeeEnabledIsTheUnionOfItsDestinations(t *testing.T) {
	var terminal, file bytes.Buffer

	tee := NewTee(
		New(&terminal, Options{Level: slog.LevelError}),
		New(&file, Options{Level: slog.LevelDebug}),
	)

	if !tee.Enabled(t.Context(), slog.LevelDebug) {
		t.Error("Enabled(debug) = false, so the debug destination would never be reached")
	}
}

func TestTeeDropsNilDestinations(t *testing.T) {
	var buf bytes.Buffer

	// A caller with an optional log file passes nil rather than branching.
	logger := slog.New(NewTee(New(&buf, Options{}), nil))
	logger.Info("hello")

	if !strings.Contains(buf.String(), "hello") {
		t.Errorf("destination = %q, want the record", buf.String())
	}
}

func TestTeePropagatesAttributesAndGroups(t *testing.T) {
	var first, second bytes.Buffer

	logger := slog.New(NewTee(
		New(&first, Options{}),
		New(&second, Options{}),
	)).With("run", "abc").WithGroup("ws")
	logger.Info("connected", "assets", 400)

	for name, buf := range map[string]*bytes.Buffer{"first": &first, "second": &second} {
		if got := buf.String(); !strings.Contains(got, "run=abc ws.assets=400") {
			t.Errorf("%s destination = %q, want the derived attributes", name, got)
		}
	}
}

// One destination failing must not stop another from receiving the record, or a
// full disk would take the terminal output down with it.
func TestTeeReportsFailuresWithoutSkippingOtherDestinations(t *testing.T) {
	var buf bytes.Buffer
	sentinel := errors.New("disk full")

	tee := NewTee(failingHandler{err: sentinel}, New(&buf, Options{}))

	err := tee.Handle(t.Context(), slog.Record{Level: slog.LevelInfo, Message: "hello"})
	if !errors.Is(err, sentinel) {
		t.Errorf("Handle returned %v, want it to report the failing destination", err)
	}
	if !strings.Contains(buf.String(), "hello") {
		t.Errorf("the working destination = %q, want the record delivered anyway", buf.String())
	}
}
