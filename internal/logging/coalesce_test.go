// Test data: Invented records. What is under test is suppression and counting, which hold
// whatever the message says.

package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// newCoalescingLogger builds a logger that suppresses repeats, returning the
// Coalescer too so a test can flush it.
func newCoalescingLogger(t *testing.T) (*slog.Logger, *Coalescer, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer
	coalescer := NewCoalescer(New(&buf, Options{Level: slog.LevelDebug}))

	return slog.New(coalescer), coalescer, &buf
}

// lines returns the non-empty lines written so far.
func lines(buf *bytes.Buffer) []string {
	trimmed := strings.TrimSpace(buf.String())
	if trimmed == "" {
		return nil
	}

	return strings.Split(trimmed, "\n")
}

// The real-time log file only earns its keep if a problem shows up as it
// starts, so the first occurrence is never held back.
func TestCoalescerEmitsTheFirstOccurrenceImmediately(t *testing.T) {
	logger, _, buf := newCoalescingLogger(t)

	logger.Warn("connection ended", "shard", 0)

	if got := lines(buf); len(got) != 1 {
		t.Fatalf("wrote %d lines for the first occurrence, want 1: %v", len(got), got)
	}
	if !strings.Contains(buf.String(), "connection ended shard=0") {
		t.Errorf("line = %q, want the record itself", buf.String())
	}
}

func TestCoalescerSuppressesRepeatsUntilTheRunEnds(t *testing.T) {
	logger, _, buf := newCoalescingLogger(t)

	for range 37 {
		logger.Warn("connection ended", "shard", 0)
	}

	if got := lines(buf); len(got) != 1 {
		t.Fatalf("wrote %d lines for 37 identical records, want 1: %v", len(got), got)
	}

	// A different record ends the run and releases the summary.
	logger.Info("collecting")

	got := lines(buf)
	if len(got) != 3 {
		t.Fatalf("wrote %d lines, want the original, the summary and the new record: %v", len(got), got)
	}
	if !strings.Contains(got[1], "connection ended (x37)") {
		t.Errorf("summary = %q, want the total count", got[1])
	}
	if !strings.Contains(got[2], "collecting") {
		t.Errorf("third line = %q, want the record that ended the run", got[2])
	}
}

// A failure that repeated until the process exited would otherwise report every
// occurrence except how many there were.
func TestCoalescerFlushReleasesAPendingSummary(t *testing.T) {
	logger, coalescer, buf := newCoalescingLogger(t)

	for range 5 {
		logger.Error("could not re-seed", "token", "111")
	}
	if got := lines(buf); len(got) != 1 {
		t.Fatalf("wrote %d lines before the flush, want 1: %v", len(got), got)
	}

	if err := coalescer.Flush(); err != nil {
		t.Fatalf("Flush returned error: %v", err)
	}

	got := lines(buf)
	if len(got) != 2 {
		t.Fatalf("wrote %d lines after the flush, want 2: %v", len(got), got)
	}
	if !strings.Contains(got[1], "could not re-seed (x5)") {
		t.Errorf("summary = %q, want the total count", got[1])
	}
}

// A run of one was already written in full, so "(x1)" would repeat it to no
// purpose.
func TestCoalescerSaysNothingExtraForASingleOccurrence(t *testing.T) {
	logger, coalescer, buf := newCoalescingLogger(t)

	logger.Info("collecting")
	if err := coalescer.Flush(); err != nil {
		t.Fatalf("Flush returned error: %v", err)
	}

	if got := lines(buf); len(got) != 1 {
		t.Errorf("wrote %d lines for one record, want 1: %v", len(got), got)
	}
}

func TestCoalescerFlushOnAnEmptyLoggerDoesNothing(t *testing.T) {
	_, coalescer, buf := newCoalescingLogger(t)

	if err := coalescer.Flush(); err != nil {
		t.Fatalf("Flush returned error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("flushing an idle coalescer wrote %q", buf.String())
	}
}

// Records that differ in any way a reader would notice are different records.
func TestCoalescerDistinguishesRecordsThatDiffer(t *testing.T) {
	tests := []struct {
		name  string
		first func(*slog.Logger)
		then  func(*slog.Logger)
	}{
		{
			name:  "different message",
			first: func(l *slog.Logger) { l.Warn("connection ended") },
			then:  func(l *slog.Logger) { l.Warn("connection idle") },
		},
		{
			name:  "different attribute value",
			first: func(l *slog.Logger) { l.Warn("connection ended", "shard", 0) },
			then:  func(l *slog.Logger) { l.Warn("connection ended", "shard", 1) },
		},
		{
			name:  "different level",
			first: func(l *slog.Logger) { l.Warn("connection ended") },
			then:  func(l *slog.Logger) { l.Error("connection ended") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, _, buf := newCoalescingLogger(t)

			tt.first(logger)
			tt.then(logger)

			if got := lines(buf); len(got) != 2 {
				t.Errorf("wrote %d lines for two distinct records, want 2: %v", len(got), got)
			}
		})
	}
}

// Two shards logging the same sentence are two facts, not one repeated.
func TestCoalescerDoesNotMergeAcrossDerivedLoggers(t *testing.T) {
	logger, _, buf := newCoalescingLogger(t)

	logger.With("shard", 0).Warn("connection ended")
	logger.With("shard", 1).Warn("connection ended")

	if got := lines(buf); len(got) != 2 {
		t.Errorf("wrote %d lines for two shards, want 2: %v", len(got), got)
	}
}

// One derived logger repeating itself is still a repeat, which only works
// because every Coalescer derived from one another shares its run state.
func TestCoalescerStillMergesWithinOneDerivedLogger(t *testing.T) {
	logger, coalescer, buf := newCoalescingLogger(t)
	shard := logger.With("shard", 0)

	for range 4 {
		shard.Warn("connection ended")
	}
	if err := coalescer.Flush(); err != nil {
		t.Fatalf("Flush returned error: %v", err)
	}

	got := lines(buf)
	if len(got) != 2 {
		t.Fatalf("wrote %d lines, want the record and its summary: %v", len(got), got)
	}
	if !strings.Contains(got[1], "connection ended (x4)") {
		t.Errorf("summary = %q, want the total count", got[1])
	}
	if !strings.Contains(got[1], "shard=0") {
		t.Errorf("summary = %q, want it to keep the derived attributes", got[1])
	}
}

// The engine logs from a goroutine per connection and per shard, so this runs
// under the race detector in CI.
func TestCoalescerIsSafeUnderConcurrentUse(t *testing.T) {
	logger, coalescer, buf := newCoalescingLogger(t)

	var wg sync.WaitGroup
	for shard := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				logger.Warn("connection ended", "shard", shard)
			}
		}()
	}
	wg.Wait()

	if err := coalescer.Flush(); err != nil {
		t.Fatalf("Flush returned error: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("nothing was written at all")
	}

	// Suppression is opportunistic under interleaving, so the guarantee is a
	// reduction rather than an exact count: 400 records must not produce 400
	// lines, and every line must still be one of the eight shards' messages.
	got := lines(buf)
	if len(got) >= 400 {
		t.Errorf("wrote %d lines for 400 records, want repeats collapsed", len(got))
	}
	for _, line := range got {
		if !strings.Contains(line, "connection ended") {
			t.Errorf("unexpected line %q", line)
		}
	}
}
