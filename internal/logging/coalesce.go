package logging

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// Coalescer collapses a run of identical records into one line plus a count.
//
// The failure this exists for is a connection that drops and redials in a tight
// loop. Each attempt logs the same sentence, and within seconds the interesting
// records are thousands of lines back. The same repetition also fills the
// output document's bounded error list, so the messages that survive the cap are
// the earliest and least informative ones.
//
// The first occurrence is emitted immediately and only the repeats are held.
// That ordering is deliberate: the whole point of the real-time log file is that
// something watching a run sees a problem as it starts, and holding the first
// occurrence until the run ended would delay every message by however long its
// repetition lasts. When the run does end, a summary carrying the total is
// emitted:
//
//	[!] connection ended shard=0 error="..."
//	[!] connection ended (x37) shard=0 error="..."
//
// A line already written to a file cannot be rewritten in place, so a trailing
// summary is the honest form of "modify that line with xN".
type Coalescer struct {
	next  slog.Handler
	scope string
	state *runState
}

// runState is the run being accumulated. It is shared by every Coalescer
// derived from one another through WithAttrs or WithGroup, so that a repeated
// message is recognised as repeated no matter which derived logger emitted it.
type runState struct {
	mu     sync.Mutex
	active bool
	key    string
	count  int
	record slog.Record
	holder slog.Handler
}

// NewCoalescer wraps a handler with repeat suppression.
func NewCoalescer(next slog.Handler) *Coalescer {
	return &Coalescer{next: next, state: &runState{}}
}

// Enabled implements slog.Handler.
func (c *Coalescer) Enabled(ctx context.Context, level slog.Level) bool {
	return c.next.Enabled(ctx, level)
}

// Handle implements slog.Handler.
func (c *Coalescer) Handle(ctx context.Context, record slog.Record) error {
	key := c.keyFor(record)

	c.state.mu.Lock()
	if c.state.active && c.state.key == key {
		c.state.count++
		c.state.mu.Unlock()

		return nil
	}

	summary, holder := c.state.closeRun()

	c.state.active = true
	c.state.key = key
	c.state.count = 1
	c.state.record = record.Clone()
	c.state.holder = c.next
	c.state.mu.Unlock()

	// Emitted outside the lock. A destination that itself logs, or that blocks
	// on a slow file, must not be able to stall every other goroutine trying to
	// record a line.
	var failures []error
	if holder != nil {
		if err := holder.Handle(ctx, summary); err != nil {
			failures = append(failures, err)
		}
	}
	if err := c.next.Handle(ctx, record); err != nil {
		failures = append(failures, err)
	}

	return errors.Join(failures...)
}

// Flush emits the summary for a run still in progress.
//
// It must be called before the process exits, or a failure that repeated until
// the very end reports every occurrence except its count.
func (c *Coalescer) Flush() error {
	c.state.mu.Lock()
	summary, holder := c.state.closeRun()
	c.state.mu.Unlock()

	if holder == nil {
		return nil
	}

	return holder.Handle(context.Background(), summary)
}

// closeRun ends the current run, returning the summary to emit and the handler
// to emit it through, or a nil handler when there is nothing worth saying.
// The caller must hold the mutex.
func (s *runState) closeRun() (slog.Record, slog.Handler) {
	defer func() {
		s.active = false
		s.holder = nil
	}()

	// A run of one was already emitted in full; a "(x1)" line would repeat it
	// to no purpose.
	if !s.active || s.count <= 1 {
		return slog.Record{}, nil
	}

	summary := s.record.Clone()
	summary.Message = fmt.Sprintf("%s (x%d)", s.record.Message, s.count)

	return summary, s.holder
}

// WithAttrs implements slog.Handler.
func (c *Coalescer) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return c
	}

	return &Coalescer{
		next:  c.next.WithAttrs(attrs),
		scope: c.scope + renderScope(attrs),
		state: c.state,
	}
}

// WithGroup implements slog.Handler.
func (c *Coalescer) WithGroup(name string) slog.Handler {
	if name == "" {
		return c
	}

	return &Coalescer{
		next:  c.next.WithGroup(name),
		scope: c.scope + name + "/",
		state: c.state,
	}
}

// keyFor identifies a record for comparison.
//
// The scope is included so that two loggers carrying different attributes, such
// as two shards, are not mistaken for one another when they log the same
// sentence. Only the timestamp is excluded, since every record differs in that.
func (c *Coalescer) keyFor(record slog.Record) string {
	var key strings.Builder

	key.WriteString(c.scope)
	key.WriteString(record.Level.String())
	key.WriteByte(0)
	key.WriteString(record.Message)

	record.Attrs(func(attr slog.Attr) bool {
		key.WriteByte(0)
		key.WriteString(attr.Key)
		key.WriteByte('=')
		key.WriteString(attr.Value.Resolve().String())

		return true
	})

	return key.String()
}

// renderScope flattens handler-level attributes into a comparison key.
func renderScope(attrs []slog.Attr) string {
	var scope strings.Builder

	for _, attr := range attrs {
		scope.WriteString(attr.Key)
		scope.WriteByte('=')
		scope.WriteString(attr.Value.Resolve().String())
		scope.WriteByte(';')
	}

	return scope.String()
}
