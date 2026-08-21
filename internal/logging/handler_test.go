package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// newTestLogger builds a logger writing into a buffer, with colour off unless a
// test asks for it. A bytes.Buffer is not a character device, so ColourAuto
// resolves to plain text and the assertions stay readable.
func newTestLogger(t *testing.T, opts Options) (*slog.Logger, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer

	return slog.New(New(&buf, opts)), &buf
}

func TestHandlerPrefixesByLevel(t *testing.T) {
	tests := []struct {
		name  string
		level slog.Level
		want  string
	}{
		{"error", slog.LevelError, prefixError},
		{"warn", slog.LevelWarn, prefixWarn},
		{"info", slog.LevelInfo, prefixInfo},
		{"debug", slog.LevelDebug, prefixDebug},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, buf := newTestLogger(t, Options{Level: slog.LevelDebug})
			logger.Log(t.Context(), tt.level, "something happened")

			if got := buf.String(); !strings.Contains(got, tt.want+" something happened") {
				t.Errorf("line = %q, want the prefix %q", got, tt.want)
			}
		})
	}
}

// A kind is only ever set deliberately at the call site, so it wins over the
// level, which every record carries whether or not anyone chose it.
func TestHandlerPrefixesByKindAheadOfLevel(t *testing.T) {
	tests := []struct {
		name string
		emit func(*slog.Logger)
		want string
	}{
		{"step", func(l *slog.Logger) { Step(l, "connecting") }, prefixStep},
		{"success", func(l *slog.Logger) { Success(l, "connecting") }, prefixSuccess},
		{"question", func(l *slog.Logger) { Question(l, "connecting") }, prefixQuestion},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, buf := newTestLogger(t, Options{})
			tt.emit(logger)

			got := buf.String()
			if !strings.Contains(got, tt.want+" connecting") {
				t.Errorf("line = %q, want the prefix %q", got, tt.want)
			}
			// The prefix already says what the kind says.
			if strings.Contains(got, KindKey+"=") {
				t.Errorf("line = %q, want the kind attribute suppressed", got)
			}
		})
	}
}

func TestHandlerColourIsOptedIntoNotAssumed(t *testing.T) {
	tests := []struct {
		name       string
		mode       ColourMode
		wantColour bool
	}{
		{"always", ColourAlways, true},
		{"never", ColourNever, false},
		// A buffer is not a terminal, and colour written to a file or a pipe is
		// noise that whatever reads it has to strip back out.
		{"auto into a buffer", ColourAuto, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, buf := newTestLogger(t, Options{Colour: tt.mode})
			logger.Error("it broke")

			if got := strings.Contains(buf.String(), colourRed); got != tt.wantColour {
				t.Errorf("colour present = %v, want %v; line = %q", got, tt.wantColour, buf.String())
			}
		})
	}
}

// Only the prefix is coloured: colouring the message would make the interesting
// part of the line the hardest to read.
func TestHandlerColoursOnlyThePrefix(t *testing.T) {
	logger, buf := newTestLogger(t, Options{Colour: ColourAlways})
	logger.Error("it broke", "shard", 3)

	got := buf.String()
	want := colourRed + prefixError + colourReset + " it broke"
	if !strings.Contains(got, want) {
		t.Errorf("line = %q, want it to contain %q", got, want)
	}
	if strings.Count(got, colourReset) != 1 {
		t.Errorf("line = %q, want exactly one colour reset", got)
	}
}

func TestHandlerWritesAnISO8601UTCTimestamp(t *testing.T) {
	logger, buf := newTestLogger(t, Options{})
	logger.Info("hello")

	stamp, _, found := strings.Cut(buf.String(), " ")
	if !found {
		t.Fatalf("line = %q, want a timestamp then a prefix", buf.String())
	}
	if _, err := time.Parse(timeFormat, stamp); err != nil {
		t.Errorf("timestamp %q does not parse as %q: %v", stamp, timeFormat, err)
	}
}

func TestHandlerRendersAttributes(t *testing.T) {
	logger, buf := newTestLogger(t, Options{})
	logger.Info("collecting", "tokens", 400, "shards", 3)

	got := strings.TrimSpace(buf.String())
	if !strings.HasSuffix(got, "collecting tokens=400 shards=3") {
		t.Errorf("line = %q, want the attributes appended as key=value", got)
	}
}

// A value containing a space or an equals sign would break the framing that
// makes these lines greppable, so it is quoted rather than left ambiguous.
func TestHandlerQuotesValuesThatWouldBreakTheFraming(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"space", "no frame for 30s", `error="no frame for 30s"`},
		{"equals", "a=b", `error="a=b"`},
		{"quote", `he said "no"`, `error="he said \"no\""`},
		{"newline", "one\ntwo", `error="one\ntwo"`},
		{"empty", "", `error=""`},
		{"plain", "reconnecting", `error=reconnecting`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, buf := newTestLogger(t, Options{})
			logger.Info("connection ended", "error", tt.value)

			if got := buf.String(); !strings.Contains(got, tt.want) {
				t.Errorf("line = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

// The same record has to be able to be short on a terminal and complete in a
// file, which is what lets a frame that could not be decoded be previewed in
// one place and kept in full in the other.
func TestHandlerTruncatesValuesOnlyWhenAsked(t *testing.T) {
	long := strings.Repeat("x", 500)

	full, fullBuf := newTestLogger(t, Options{})
	full.Error("undecodable frame", "frame", long)
	if !strings.Contains(fullBuf.String(), long) {
		t.Error("an unlimited handler truncated a value")
	}

	short, shortBuf := newTestLogger(t, Options{MaxValueLength: 20})
	short.Error("undecodable frame", "frame", long)

	got := shortBuf.String()
	if strings.Contains(got, long) {
		t.Error("a limited handler wrote the value in full")
	}
	if !strings.Contains(got, "(500 bytes)") {
		t.Errorf("line = %q, want the true length reported alongside the truncation", got)
	}
}

func TestHandlerWithAttrsAndWithGroup(t *testing.T) {
	tests := []struct {
		name   string
		derive func(*slog.Logger) *slog.Logger
		want   string
	}{
		{
			name:   "attributes are carried onto every record",
			derive: func(l *slog.Logger) *slog.Logger { return l.With("run", "abc") },
			want:   "run=abc tokens=400",
		},
		{
			name:   "a group becomes a dotted key prefix",
			derive: func(l *slog.Logger) *slog.Logger { return l.WithGroup("ws") },
			want:   "ws.tokens=400",
		},
		{
			name: "attributes added before a group keep their own scope",
			derive: func(l *slog.Logger) *slog.Logger {
				return l.With("run", "abc").WithGroup("ws")
			},
			want: "run=abc ws.tokens=400",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, buf := newTestLogger(t, Options{})
			tt.derive(logger).Info("collecting", "tokens", 400)

			if got := buf.String(); !strings.Contains(got, tt.want) {
				t.Errorf("line = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

func TestHandlerRespectsLevel(t *testing.T) {
	logger, buf := newTestLogger(t, Options{Level: slog.LevelError})

	logger.Info("suppressed")
	logger.Warn("also suppressed")
	if buf.Len() != 0 {
		t.Fatalf("records below the level were written: %q", buf.String())
	}

	logger.Error("kept")
	if !strings.Contains(buf.String(), "kept") {
		t.Errorf("the record at the level was dropped: %q", buf.String())
	}
}

func TestHandlerWritesOneLinePerRecord(t *testing.T) {
	logger, buf := newTestLogger(t, Options{})

	logger.Info("first")
	logger.Info("second", "detail", "with spaces")

	if got := strings.Count(buf.String(), "\n"); got != 2 {
		t.Errorf("wrote %d lines for 2 records: %q", got, buf.String())
	}
}

// slog documents that an empty attribute should be dropped rather than rendered
// as a bare "=", which would look like a value that failed to render.
func TestHandlerDropsEmptyAttributes(t *testing.T) {
	logger, buf := newTestLogger(t, Options{})
	logger.LogAttrs(t.Context(), slog.LevelInfo, "hello", slog.Attr{}, slog.String("real", "yes"))

	got := strings.TrimSpace(buf.String())
	if !strings.HasSuffix(got, "hello real=yes") {
		t.Errorf("line = %q, want the empty attribute dropped", got)
	}
}

func TestHandlerFlattensGroupValues(t *testing.T) {
	logger, buf := newTestLogger(t, Options{})
	logger.Info("connection", slog.Group("ws", "shard", 1, "assets", 400))

	if got := buf.String(); !strings.Contains(got, "ws.shard=1 ws.assets=400") {
		t.Errorf("line = %q, want the group flattened into dotted keys", got)
	}
}
