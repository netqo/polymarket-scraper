// Test data: Invented records. Categories are this program's own vocabulary.

package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// newFilteredLogger builds a logger with the named categories switched off.
func newFilteredLogger(t *testing.T, off ...Category) (*slog.Logger, *bytes.Buffer) {
	t.Helper()

	disabled := make(map[Category]bool, len(off))
	for _, category := range off {
		disabled[category] = true
	}

	var buf bytes.Buffer
	handler := NewFilter(New(&buf, Options{Level: slog.LevelDebug}), disabled)

	return slog.New(handler), &buf
}

func TestFilterDropsASwitchedOffCategory(t *testing.T) {
	logger, buf := newFilteredLogger(t, CategoryFlags)

	logger.Warn("token flagged", Cat(CategoryFlags), "flag", "delta_gap")

	if buf.Len() != 0 {
		t.Errorf("a switched-off record was written: %q", buf.String())
	}
}

func TestFilterKeepsEverythingElse(t *testing.T) {
	logger, buf := newFilteredLogger(t, CategoryFlags)

	logger.Info("connected", Cat(CategoryConnection), "assets", 400)

	if !strings.Contains(buf.String(), "connected") {
		t.Errorf("output = %q, want the record from an enabled category", buf.String())
	}
}

// A category says what a record is about, not whether it matters. Something has
// to reach a reader who has switched everything else off, or a silent run and a
// broken one look alike.
func TestFilterNeverDropsAnError(t *testing.T) {
	logger, buf := newFilteredLogger(t, Categories...)

	logger.Error("the run failed", Cat(CategoryConnection), "error", "everything")

	if !strings.Contains(buf.String(), "the run failed") {
		t.Errorf("an error was suppressed by a category switch: %q", buf.String())
	}
}

// A call site nobody has categorised is never silently lost.
func TestFilterKeepsUncategorisedRecords(t *testing.T) {
	logger, buf := newFilteredLogger(t, Categories...)

	logger.Info("something nobody labelled")

	if !strings.Contains(buf.String(), "something nobody labelled") {
		t.Errorf("an uncategorised record was suppressed: %q", buf.String())
	}
}

// Both spellings have to work, or which one a caller happened to use would
// decide whether the toggle applied.
func TestFilterHonoursACategoryCarriedByTheLogger(t *testing.T) {
	logger, buf := newFilteredLogger(t, CategoryREST)

	scoped := logger.With(Cat(CategoryREST))
	scoped.Warn("batch fetch failed", "tokens", 250)

	if buf.Len() != 0 {
		t.Errorf("a record scoped to a switched-off category was written: %q", buf.String())
	}

	// And the same logger still lets an error through.
	scoped.Error("giving up")
	if !strings.Contains(buf.String(), "giving up") {
		t.Errorf("output = %q, want the error", buf.String())
	}
}

// The category has already decided whether the record is here at all, so
// printing it would put a word on every line that tells its reader nothing.
func TestTheCategoryIsNotRendered(t *testing.T) {
	logger, buf := newFilteredLogger(t)

	logger.Info("connected", Cat(CategoryConnection), "assets", 400)

	got := buf.String()
	if strings.Contains(got, CategoryKey+"=") {
		t.Errorf("output = %q, want the category attribute suppressed", got)
	}
	if !strings.Contains(got, "connected assets=400") {
		t.Errorf("output = %q, want the message and its real attributes", got)
	}
}

func TestFilterWithNothingDisabledChangesNothing(t *testing.T) {
	logger, buf := newFilteredLogger(t)

	for _, category := range Categories {
		logger.Info("hello", Cat(category))
	}

	if got := strings.Count(buf.String(), "hello"); got != len(Categories) {
		t.Errorf("wrote %d records, want all %d", got, len(Categories))
	}
}

// Switching one category off must not disturb the others, which is the whole
// point of having more than one.
func TestFilterIsIndependentPerCategory(t *testing.T) {
	for _, off := range Categories {
		t.Run(string(off), func(t *testing.T) {
			logger, buf := newFilteredLogger(t, off)

			for _, category := range Categories {
				logger.Info(string(category), Cat(category))
			}

			// Each message is its own category's name, so the messages that
			// came out say exactly which categories survived.
			written := writtenMessages(buf.String())

			if len(written) != len(Categories)-1 {
				t.Fatalf("wrote %v, want every category except %q", written, off)
			}
			for _, message := range written {
				if message == string(off) {
					t.Errorf("the switched-off category %q was written anyway", off)
				}
			}
		})
	}
}

// writtenMessages pulls the message out of each rendered line, which sits after
// the timestamp and the prefix.
func writtenMessages(rendered string) []string {
	trimmed := strings.TrimSpace(rendered)
	if trimmed == "" {
		return nil
	}

	var messages []string
	for _, line := range strings.Split(trimmed, "\n") {
		if fields := strings.Fields(line); len(fields) >= 3 {
			messages = append(messages, fields[2])
		}
	}

	return messages
}

// Derived loggers keep the filter, or an engine that adds a run id to its
// logger would lose every switch in the process.
func TestFilterSurvivesWithAttrsAndWithGroup(t *testing.T) {
	logger, buf := newFilteredLogger(t, CategoryFlags)

	logger.With("run", "abc").WithGroup("ws").Warn("token flagged", Cat(CategoryFlags))

	if buf.Len() != 0 {
		t.Errorf("a derived logger lost its filter: %q", buf.String())
	}
}
