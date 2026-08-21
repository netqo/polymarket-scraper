package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The prefix vocabulary. Two levels share "[!]" because the distinction between
// a warning and an error is one of colour and of what follows, not of category:
// both mean look here.
const (
	prefixError    = "[!]"
	prefixWarn     = "[!]"
	prefixInfo     = "[*]"
	prefixDebug    = "[~]"
	prefixStep     = "[~]"
	prefixSuccess  = "[+]"
	prefixQuestion = "[?]"
)

// ANSI escapes. Only the prefix is ever coloured: colouring the message would
// make the interesting part of the line the hardest to read, and colouring the
// attributes would defeat copying a line into a bug report.
const (
	colourReset   = "\x1b[0m"
	colourRed     = "\x1b[31m"
	colourGreen   = "\x1b[32m"
	colourYellow  = "\x1b[33m"
	colourBlue    = "\x1b[34m"
	colourMagenta = "\x1b[35m"
	colourDim     = "\x1b[2m"
)

// timeFormat is ISO-8601 UTC with milliseconds, matching the convention the
// output document uses for timestamps the scraper generates.
//
// It is spelled out here rather than imported from the report package, because
// a log line and a document field are different contracts that happen to agree
// today, and a dependency from this package up to the report layer would be the
// wrong way round.
const timeFormat = "2006-01-02T15:04:05.000Z"

// ColourMode decides whether escape codes are emitted.
type ColourMode int

// The colour modes.
const (
	// ColourAuto emits colour only when the destination is a terminal and
	// NO_COLOR is unset.
	ColourAuto ColourMode = iota

	// ColourAlways emits colour unconditionally. Tests use it to assert on the
	// escapes without needing a pseudo-terminal.
	ColourAlways

	// ColourNever never emits colour. This is what the log file uses: escape
	// codes in a file an agent parses are noise it has to strip.
	ColourNever
)

// enabled resolves the mode against a destination.
func (m ColourMode) enabled(w io.Writer) bool {
	switch m {
	case ColourAlways:
		return true
	case ColourNever:
		return false
	default:
		return isTerminal(w) && os.Getenv("NO_COLOR") == ""
	}
}

// isTerminal reports whether w is a character device, which is the closest
// thing to a terminal check the standard library offers without a dependency.
func isTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}

	info, err := file.Stat()
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0
}

// Options configure a Handler.
type Options struct {
	// Level is the minimum severity written. Nil means info.
	Level slog.Leveler

	// Colour decides whether escapes are emitted. The zero value auto-detects.
	Colour ColourMode

	// MaxValueLength truncates rendered attribute values, appending an ellipsis
	// and the full length. Zero means no limit.
	//
	// This is what lets one record be short on a terminal and complete in a
	// file: the same error carries its full text, and each destination decides
	// how much of it to show. Messages are never truncated, only values.
	MaxValueLength int
}

// Handler renders records as one prefixed, timestamped line each.
//
// The layout is: timestamp, prefix, message, then attributes as key=value.
// It is fixed-position and space-separated so that it stays greppable, which
// matters because these lines are quoted verbatim into bug reports and read
// back by agents.
type Handler struct {
	out   io.Writer
	mu    *sync.Mutex
	level slog.Leveler

	colour   bool
	maxValue int

	// preformat holds attributes accumulated by WithAttrs, already rendered
	// with the group prefix that was open when they were added.
	preformat []byte

	// groups is the dotted prefix applied to keys, ending in '.' when non-empty.
	groups string
}

// New builds a Handler writing to w.
func New(w io.Writer, opts Options) *Handler {
	level := opts.Level
	if level == nil {
		level = slog.LevelInfo
	}

	return &Handler{
		out:      w,
		mu:       &sync.Mutex{},
		level:    level,
		colour:   opts.Colour.enabled(w),
		maxValue: opts.MaxValueLength,
	}
}

// Enabled implements slog.Handler.
func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}

	clone := h.clone()
	for _, attr := range attrs {
		clone.preformat = clone.appendAttr(clone.preformat, h.groups, attr)
	}

	return clone
}

// WithGroup implements slog.Handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	clone := h.clone()
	clone.groups = h.groups + name + "."

	return clone
}

// clone copies the handler, sharing the mutex so that every logger derived from
// one Handler still serializes its writes against the same destination.
func (h *Handler) clone() *Handler {
	copied := *h
	copied.preformat = append([]byte(nil), h.preformat...)

	return &copied
}

// Handle implements slog.Handler.
func (h *Handler) Handle(_ context.Context, record slog.Record) error {
	buf := make([]byte, 0, 256)

	stamp := record.Time
	if stamp.IsZero() {
		stamp = time.Now()
	}
	buf = stamp.UTC().AppendFormat(buf, timeFormat)

	symbol, colour := prefixFor(record.Level, kindOf(record))
	buf = append(buf, ' ')
	if h.colour {
		buf = append(buf, colour...)
		buf = append(buf, symbol...)
		buf = append(buf, colourReset...)
	} else {
		buf = append(buf, symbol...)
	}

	buf = append(buf, ' ')
	buf = append(buf, record.Message...)

	buf = append(buf, h.preformat...)
	record.Attrs(func(attr slog.Attr) bool {
		// The kind chose the prefix; repeating it as an attribute would say the
		// same thing twice on every line that has one. Skipped on exactly the
		// same condition kindOf matches it, so the two cannot disagree.
		if attr.Key == KindKey {
			return true
		}
		buf = h.appendAttr(buf, h.groups, attr)
		return true
	})

	buf = append(buf, '\n')

	h.mu.Lock()
	defer h.mu.Unlock()

	_, err := h.out.Write(buf)

	return err
}

// kindOf extracts the presentational kind from a record, if it carries one.
func kindOf(record slog.Record) Kind {
	var kind Kind

	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key != KindKey {
			return true
		}
		kind = Kind(attr.Value.Resolve().String())
		return false
	})

	return kind
}

// prefixFor chooses a record's symbol and colour.
//
// A kind wins over the level, because a kind is only ever set deliberately at
// the call site, whereas every record has a level whether or not anyone thought
// about it.
func prefixFor(level slog.Level, kind Kind) (symbol, colour string) {
	switch kind {
	case KindStep:
		return prefixStep, colourYellow
	case KindSuccess:
		return prefixSuccess, colourGreen
	case KindQuestion:
		return prefixQuestion, colourMagenta
	}

	switch {
	case level >= slog.LevelError:
		return prefixError, colourRed
	case level >= slog.LevelWarn:
		return prefixWarn, colourYellow
	case level >= slog.LevelInfo:
		return prefixInfo, colourBlue
	default:
		return prefixDebug, colourDim
	}
}

// appendAttr renders one attribute, flattening groups into dotted keys.
func (h *Handler) appendAttr(buf []byte, prefix string, attr slog.Attr) []byte {
	attr.Value = attr.Value.Resolve()

	// An empty attribute carries no information, and slog documents that
	// handlers should drop it rather than render a bare "=".
	if attr.Equal(slog.Attr{}) {
		return buf
	}

	if attr.Value.Kind() == slog.KindGroup {
		nested := attr.Value.Group()
		if len(nested) == 0 {
			return buf
		}

		inner := prefix
		if attr.Key != "" {
			inner = prefix + attr.Key + "."
		}
		for _, sub := range nested {
			buf = h.appendAttr(buf, inner, sub)
		}

		return buf
	}

	buf = append(buf, ' ')
	buf = append(buf, prefix...)
	buf = append(buf, attr.Key...)
	buf = append(buf, '=')

	return h.appendValue(buf, attr.Value)
}

// appendValue renders a value, quoting it when it would otherwise break the
// key=value framing, and truncating it when the destination asked for a limit.
func (h *Handler) appendValue(buf []byte, value slog.Value) []byte {
	text := value.String()
	if h.maxValue > 0 && len(text) > h.maxValue {
		text = text[:h.maxValue] + "... (" + strconv.Itoa(len(text)) + " bytes)"
	}

	if needsQuoting(text) {
		return strconv.AppendQuote(buf, text)
	}

	return append(buf, text...)
}

// needsQuoting reports whether text would be ambiguous unquoted.
func needsQuoting(text string) bool {
	if text == "" {
		return true
	}

	return strings.ContainsAny(text, " \t\r\n\"=")
}
