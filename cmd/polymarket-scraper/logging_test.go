package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/netqo/polymarket-scraper/internal/config"
	"github.com/netqo/polymarket-scraper/internal/logging"
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

	got := buf.String()
	if !strings.Contains(got, "hello") || !strings.Contains(got, "key=value") {
		t.Fatalf("log record = %q, want the message and its attribute", got)
	}
}

// Successive runs append into the same log file, so without an identifier their
// lines become indistinguishable as soon as the timestamps get close.
func TestProcessLoggerTagsEveryRecordWithTheRun(t *testing.T) {
	var buf bytes.Buffer

	proc, err := newProcessLogger(&buf, loggerConfig(levelInfo, ""))
	if err != nil {
		t.Fatalf("newProcessLogger returned error: %v", err)
	}
	defer proc.Close()

	proc.logger.Info("first")
	proc.logger.Info("second")

	ids := runIDs(t, buf.String())
	if len(ids) != 2 {
		t.Fatalf("found %d run identifiers in %q, want one per record", len(ids), buf.String())
	}
	if ids[0] != ids[1] {
		t.Errorf("records carry different run identifiers %q and %q", ids[0], ids[1])
	}
	if ids[0] == "unknown" {
		t.Error("the run identifier fell back to its failure value")
	}
}

// Two loggers are two runs, or a log file shared between them tells no one
// anything.
func TestProcessLoggerGivesEachRunItsOwnIdentifier(t *testing.T) {
	var first, second bytes.Buffer

	for _, buf := range []*bytes.Buffer{&first, &second} {
		proc, err := newProcessLogger(buf, loggerConfig(levelInfo, ""))
		if err != nil {
			t.Fatalf("newProcessLogger returned error: %v", err)
		}
		proc.logger.Info("hello")
		proc.Close()
	}

	if runIDs(t, first.String())[0] == runIDs(t, second.String())[0] {
		t.Errorf("two runs share the identifier %q", runIDs(t, first.String())[0])
	}
}

// runIDs extracts the run attribute from each line of rendered output.
func runIDs(t *testing.T, rendered string) []string {
	t.Helper()

	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(rendered), "\n") {
		_, after, found := strings.Cut(line, runKey+"=")
		if !found {
			t.Fatalf("line %q carries no %s attribute", line, runKey)
		}
		id, _, _ := strings.Cut(after, " ")
		ids = append(ids, id)
	}

	return ids
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
	if !strings.Contains(string(contents), "tokens=400") {
		t.Errorf("log file = %q, want the record already on disk", contents)
	}

	// Both destinations see it.
	if !strings.Contains(buf.String(), "tokens=400") {
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

// The whole point of separating the two destinations: a frame that could not be
// decoded is quoted in full where an agent can read it, and cut down to
// something legible on a terminal.
func TestProcessLoggerTruncatesForTheTerminalOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	frame := strings.Repeat("x", config.DefaultConsoleValueLimit*4)

	var buf bytes.Buffer
	proc, err := newProcessLogger(&buf, loggerConfig(levelInfo, path))
	if err != nil {
		t.Fatalf("newProcessLogger returned error: %v", err)
	}
	proc.logger.Warn("a frame could not be decoded", "error", frame)
	proc.Close()

	if strings.Contains(buf.String(), frame) {
		t.Error("the terminal received the value in full")
	}
	if !strings.Contains(buf.String(), "bytes)") {
		t.Errorf("terminal = %q, want the truncation to report the true length", buf.String())
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the log file: %v", err)
	}
	if !strings.Contains(string(contents), frame) {
		t.Error("the log file did not keep the value in full")
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

// The two packages know nothing about each other, so the translation between
// them is the thing that can silently break: a category renamed on one side
// would switch nothing off on the other, with no error anywhere.
func TestProcessLoggerAppliesTheConfiguredCategories(t *testing.T) {
	cfg := loggerConfig(levelDebug, "")
	cfg.LogCategories.Flags = false

	var buf bytes.Buffer
	proc, err := newProcessLogger(&buf, cfg)
	if err != nil {
		t.Fatalf("newProcessLogger returned error: %v", err)
	}
	defer proc.Close()

	proc.logger.Warn("token flagged", logging.Cat(logging.CategoryFlags), "flag", "delta_gap")
	if buf.Len() != 0 {
		t.Errorf("a switched-off category was written: %q", buf.String())
	}

	proc.logger.Info("connected", logging.Cat(logging.CategoryConnection))
	if !strings.Contains(buf.String(), "connected") {
		t.Errorf("output = %q, want the category that is still on", buf.String())
	}
}

// Something has to reach a reader who has switched everything else off, or a
// silent run and a broken one look alike.
func TestProcessLoggerStillReportsErrorsWithEveryCategoryOff(t *testing.T) {
	cfg := loggerConfig(levelDebug, "")
	cfg.LogCategories = config.LogCategories{}

	var buf bytes.Buffer
	proc, err := newProcessLogger(&buf, cfg)
	if err != nil {
		t.Fatalf("newProcessLogger returned error: %v", err)
	}
	defer proc.Close()

	proc.logger.Error("the run failed", logging.Cat(logging.CategoryConnection), "error", "everything")

	if !strings.Contains(buf.String(), "the run failed") {
		t.Errorf("output = %q, want the error despite every category being off", buf.String())
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
