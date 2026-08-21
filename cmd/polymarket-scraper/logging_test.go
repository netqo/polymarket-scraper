package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/netqo/polymarket-scraper/internal/config"
)

// loggerConfig is the minimum configuration newProcessLogger reads.
func loggerConfig(level, logFile string) config.Config {
	cfg := config.New()
	cfg.LogLevel = level
	cfg.LogFile = logFile

	return cfg
}

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
func TestProcessLoggerWritesToTheGivenWriter(t *testing.T) {
	var buf bytes.Buffer

	proc, err := newProcessLogger(&buf, loggerConfig(levelInfo, ""))
	if err != nil {
		t.Fatalf("newProcessLogger returned error: %v", err)
	}
	defer proc.Close()

	proc.logger.Info("hello", "key", "value")

	if got := buf.String(); !strings.Contains(got, "hello key=value") {
		t.Fatalf("log record = %q, want the message and its attribute", got)
	}
}

func TestProcessLoggerRespectsLevel(t *testing.T) {
	var buf bytes.Buffer

	proc, err := newProcessLogger(&buf, loggerConfig(levelError, ""))
	if err != nil {
		t.Fatalf("newProcessLogger returned error: %v", err)
	}
	defer proc.Close()

	proc.logger.Info("suppressed")

	if buf.Len() != 0 {
		t.Fatalf("info record written at error level: %q", buf.String())
	}
}

// The file destination is absent rather than opened-and-unused, which is what
// keeps a default run from touching the filesystem at all.
func TestProcessLoggerWithoutALogFileOpensNothing(t *testing.T) {
	var buf bytes.Buffer

	proc, err := newProcessLogger(&buf, loggerConfig(levelInfo, ""))
	if err != nil {
		t.Fatalf("newProcessLogger returned error: %v", err)
	}
	defer proc.Close()

	if proc.file != nil {
		t.Errorf("a log file was opened without being asked for: %s", proc.file.Name())
	}

	proc.logger.Info("hello")
	if !strings.Contains(buf.String(), "hello") {
		t.Errorf("stderr = %q, want the record", buf.String())
	}
}

// The point of the file is that it can be read while the run is still going, so
// a record has to reach the disk when it is logged rather than at close.
func TestProcessLoggerWritesTheFileAsTheRunHappens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")

	var buf bytes.Buffer
	proc, err := newProcessLogger(&buf, loggerConfig(levelInfo, path))
	if err != nil {
		t.Fatalf("newProcessLogger returned error: %v", err)
	}
	defer proc.Close()

	proc.logger.Info("collecting", "tokens", 400)

	// Read before Close, which is the whole guarantee.
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the log file mid-run: %v", err)
	}
	if !strings.Contains(string(contents), "collecting tokens=400") {
		t.Errorf("log file = %q, want the record already on disk", contents)
	}

	// Both destinations see it.
	if !strings.Contains(buf.String(), "collecting tokens=400") {
		t.Errorf("stderr = %q, want the record as well", buf.String())
	}
}

// Escape codes in a file an agent parses are noise it has to strip back out.
func TestProcessLoggerFileIsNeverColoured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")

	var buf bytes.Buffer
	proc, err := newProcessLogger(&buf, loggerConfig(levelInfo, path))
	if err != nil {
		t.Fatalf("newProcessLogger returned error: %v", err)
	}
	proc.logger.Error("it broke")
	proc.Close()

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the log file: %v", err)
	}
	if strings.Contains(string(contents), "\x1b[") {
		t.Errorf("log file contains escape codes: %q", contents)
	}
}

// A log names the tokens a run collected, so it is no wider than the output
// document it explains.
func TestProcessLoggerFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")

	var buf bytes.Buffer
	proc, err := newProcessLogger(&buf, loggerConfig(levelInfo, path))
	if err != nil {
		t.Fatalf("newProcessLogger returned error: %v", err)
	}
	proc.logger.Info("hello")
	proc.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != logFileMode {
		t.Errorf("log file mode = %o, want %o", perm, logFileMode)
	}
}

// A run is usually one of a series, and losing the previous run's log is the
// wrong thing to do to whoever is working out when a problem started.
func TestProcessLoggerAppendsToAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")

	for _, message := range []string{"first run", "second run"} {
		var buf bytes.Buffer
		proc, err := newProcessLogger(&buf, loggerConfig(levelInfo, path))
		if err != nil {
			t.Fatalf("newProcessLogger returned error: %v", err)
		}
		proc.logger.Info(message)
		proc.Close()
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the log file: %v", err)
	}
	for _, message := range []string{"first run", "second run"} {
		if !strings.Contains(string(contents), message) {
			t.Errorf("log file = %q, want it to still contain %q", contents, message)
		}
	}
}

// A failure that repeated until the run ended would otherwise be reported
// without its count, which is where the count is most worth having.
func TestProcessLoggerCloseFlushesTheRepeatCount(t *testing.T) {
	var buf bytes.Buffer

	proc, err := newProcessLogger(&buf, loggerConfig(levelInfo, ""))
	if err != nil {
		t.Fatalf("newProcessLogger returned error: %v", err)
	}

	for range 9 {
		proc.logger.Warn("connection ended", "shard", 0)
	}
	if strings.Contains(buf.String(), "(x9)") {
		t.Fatal("the count was written before the run of repeats ended")
	}

	proc.Close()

	if !strings.Contains(buf.String(), "connection ended (x9)") {
		t.Errorf("stderr = %q, want the repeat count after Close", buf.String())
	}
}

// An unwritable log path is a usage mistake, and it has to be reported before
// the run spends its window rather than after.
func TestProcessLoggerReportsAnUnusableLogPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-directory", "run.log")

	var buf bytes.Buffer
	if _, err := newProcessLogger(&buf, loggerConfig(levelInfo, path)); err == nil {
		t.Fatal("newProcessLogger accepted an unwritable log path")
	} else if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the path", err)
	}
}

func TestProcessLoggerCloseIsSafeOnANilReceiver(t *testing.T) {
	var proc *processLogger
	proc.Close()
}
