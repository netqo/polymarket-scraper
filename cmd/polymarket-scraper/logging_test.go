package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{" warn ", slog.LevelWarn},
		{"error", slog.LevelError},
		{"info", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"nonsense", slog.LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := parseLevel(tt.in); got != tt.want {
				t.Fatalf("parseLevel(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// Requirement A7: log records must never reach stdout, which carries only the
// final summary line.
func TestNewLoggerWritesToTheGivenWriter(t *testing.T) {
	var buf bytes.Buffer
	newLogger(&buf, levelInfo).Info("hello", "key", "value")

	got := buf.String()
	if !strings.Contains(got, "msg=hello") || !strings.Contains(got, "key=value") {
		t.Fatalf("log record = %q, want it to contain msg=hello and key=value", got)
	}
}

func TestNewLoggerRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	newLogger(&buf, levelError).Info("suppressed")

	if buf.Len() != 0 {
		t.Fatalf("info record written at error level: %q", buf.String())
	}
}
