package logging

import (
	"context"
	"log/slog"
)

// CategoryKey is the attribute naming what a record is about. Like KindKey it
// is removed from the rendered output, since it says nothing a reader of the
// line needs.
const CategoryKey = "category"

// Category is what a record is about, as opposed to how much it matters.
//
// It exists because level is the wrong axis for the question actually being
// asked. Something reading a run wants every flag and none of the keepalive
// chatter, or the reverse while chasing a disconnect, and both of those live at
// the same level. Raising the level to quieten one silences the other with it.
type Category string

// The categories. Any record without one always passes, so a call site that has
// not been categorised is never silently lost.
const (
	// CategoryStartup is the configuration record and what was odd about the
	// inputs: things said once, before collection begins.
	CategoryStartup Category = "startup"

	// CategoryProgress is the run reaching its stages.
	CategoryProgress Category = "progress"

	// CategoryConnection is sockets opening, closing and misbehaving.
	CategoryConnection Category = "connection"

	// CategoryFlags is per-token observations as the trackers raise them. The
	// highest volume of the lot on a bad run, and the most useful.
	CategoryFlags Category = "flags"

	// CategoryREST is fetches that did not go to plan.
	CategoryREST Category = "rest"

	// CategoryDecode is a message the scraper could not read.
	CategoryDecode Category = "decode"

	// CategoryDiscovery is tokens taken on mid-window from announcements.
	CategoryDiscovery Category = "discovery"
)

// Categories lists every category, for validation and for the help text.
var Categories = []Category{
	CategoryStartup,
	CategoryProgress,
	CategoryConnection,
	CategoryFlags,
	CategoryREST,
	CategoryDecode,
	CategoryDiscovery,
}

// Cat returns the attribute marking a record's category, for use at a call site.
func Cat(category Category) slog.Attr {
	return slog.String(CategoryKey, string(category))
}

// Filter drops records belonging to a category that has been switched off.
//
// It sits in front of everything else in the chain, so a suppressed record
// costs nothing: it is not rendered, not counted as a repeat, and not written
// to either destination.
type Filter struct {
	next     slog.Handler
	disabled map[Category]bool

	// scoped is the category carried by a logger built with
	// With(Cat(...)), as opposed to one passed at the call site. Both spellings
	// have to work, or which one a caller happened to use would decide whether
	// the toggle applied.
	scoped Category
}

// NewFilter wraps a handler, suppressing the named categories.
func NewFilter(next slog.Handler, disabled map[Category]bool) *Filter {
	return &Filter{next: next, disabled: disabled}
}

// Enabled implements slog.Handler.
//
// It answers for the wrapped handler rather than for the filter, because a
// category is a property of a record and there is no record here yet.
func (f *Filter) Enabled(ctx context.Context, level slog.Level) bool {
	return f.next.Enabled(ctx, level)
}

// Handle implements slog.Handler.
func (f *Filter) Handle(ctx context.Context, record slog.Record) error {
	if f.suppresses(record) {
		return nil
	}

	return f.next.Handle(ctx, record)
}

// suppresses reports whether a record has been switched off.
func (f *Filter) suppresses(record slog.Record) bool {
	// An error is never suppressed. A category says what a record is about, not
	// whether it matters, and something has to reach a reader who has turned
	// everything else off, or a silent run and a broken one look the same.
	if record.Level >= slog.LevelError {
		return false
	}

	category := f.scoped
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key != CategoryKey {
			return true
		}
		category = Category(attr.Value.Resolve().String())

		return false
	})

	if category == "" {
		return false
	}

	return f.disabled[category]
}

// WithAttrs implements slog.Handler.
func (f *Filter) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return f
	}

	clone := *f
	clone.next = f.next.WithAttrs(attrs)
	for _, attr := range attrs {
		if attr.Key == CategoryKey {
			clone.scoped = Category(attr.Value.Resolve().String())
		}
	}

	return &clone
}

// WithGroup implements slog.Handler.
func (f *Filter) WithGroup(name string) slog.Handler {
	if name == "" {
		return f
	}

	clone := *f
	clone.next = f.next.WithGroup(name)

	return &clone
}
