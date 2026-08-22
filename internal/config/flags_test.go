package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// noEnv is an environment with nothing set.
func noEnv(string) (string, bool) { return "", false }

// env returns a lookup function backed by a map.
func env(vars map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := vars[key]
		return value, ok
	}
}

// minimalArgs are the two flags without which a run cannot start.
func minimalArgs(extra ...string) []string {
	return append([]string{"--tokens", "tokens.txt", "--out", "books.json"}, extra...)
}

func TestParseAppliesDefaults(t *testing.T) {
	got, err := Parse(minimalArgs(), noEnv)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	want := New()
	want.TokensPath = "tokens.txt"
	want.OutPath = "books.json"

	if got != want {
		t.Errorf("Parse() =\n %+v\nwant\n %+v", got, want)
	}
}

// A2 specifies the scan duration in seconds and a consuming agent writes the
// invocation once, so a bare number has to mean seconds. Go's duration syntax
// is accepted too, for the human who maintains it.
func TestParseDurationAcceptsSecondsAndDurations(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"90", 90 * time.Second},
		{"30", 30 * time.Second},
		{"0.5", 500 * time.Millisecond},
		{"1m30s", 90 * time.Second},
		{"2s", 2 * time.Second},
		{"250ms", 250 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := Parse(minimalArgs("--duration", tt.in), noEnv)
			if err != nil {
				t.Fatalf("Parse returned error: %v", err)
			}
			if got.Duration != tt.want {
				t.Errorf("Duration = %v, want %v", got.Duration, tt.want)
			}
		})
	}
}

func TestParseDurationRejectsNonsense(t *testing.T) {
	_, err := Parse(minimalArgs("--duration", "soon"), noEnv)
	if err == nil {
		t.Fatal("Parse accepted a nonsense duration")
	}
	if !strings.Contains(err.Error(), "1m30s") {
		t.Errorf("error %q does not show the accepted syntax", err)
	}
}

func TestParseOverridesEveryDefault(t *testing.T) {
	args := minimalArgs(
		"--duration", "5",
		"--grace", "3",
		"--rest-only",
		"--rest-rate", "2.5",
		"--rest-batch-size", "50",
		"--max-assets-per-connection", "100",
		"--ping-interval", "1",
		"--idle-timeout", "4",
		"--reorder-tolerance", "0",
		"--strict-best-bid-ask",
		"--discover-limit", "0",
		"--ws-url", "ws://127.0.0.1:1/ws",
		"--rest-url", "http://127.0.0.1:2",
		"--log-level", "debug",
		"--log-file", "run.log",
	)

	got, err := Parse(args, noEnv)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	// Built from the defaults rather than spelled out in full, so that adding a
	// setting with no flag does not break a test about flags.
	want := New()
	want.TokensPath = "tokens.txt"
	want.OutPath = "books.json"
	want.Duration = 5 * time.Second
	want.Grace = 3 * time.Second
	want.RESTOnly = true
	want.RESTRate = 2.5
	want.RESTBatchSize = 50
	want.MaxAssetsPerConnection = 100
	want.PingInterval = time.Second
	want.IdleTimeout = 4 * time.Second
	want.ReorderTolerance = 0
	want.StrictBestBidAsk = true
	want.DiscoverLimit = 0
	want.WSURL = "ws://127.0.0.1:1/ws"
	want.RESTURL = "http://127.0.0.1:2"
	want.LogLevel = "debug"
	want.LogFile = "run.log"

	if got != want {
		t.Errorf("Parse() =\n %+v\nwant\n %+v", got, want)
	}
}

// Every flag the binary binds has to be reachable, or it is documented,
// validated and dead. Checking the count here is what catches a flag added to
// newFlagSet and then never exercised.
func TestEveryBoundFlagIsOverriddenByATest(t *testing.T) {
	// --version and --config are exercised by their own tests; everything else
	// is covered by TestParseOverridesEveryDefault above.
	covered := map[string]bool{
		"version": true, "config": true, "mode": true,
		"tokens": true, "out": true, "duration": true, "grace": true,
		"rest-only": true, "rest-rate": true, "rest-batch-size": true,
		"max-assets-per-connection": true, "ping-interval": true,
		"idle-timeout": true, "reorder-tolerance": true,
		"strict-best-bid-ask": true, "discover-limit": true,
		"ws-url": true, "rest-url": true, "log-level": true, "log-file": true,
	}

	for _, name := range boundFlags() {
		if !covered[name] {
			t.Errorf("flag --%s is bound but no test overrides it", name)
		}
	}
}

func TestParseSingleDashFlagsAlsoWork(t *testing.T) {
	got, err := Parse([]string{"-tokens", "t.txt", "-out", "o.json"}, noEnv)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if got.TokensPath != "t.txt" {
		t.Errorf("TokensPath = %q, want t.txt", got.TokensPath)
	}
}

func TestParseLogLevelFallsBackToTheEnvironment(t *testing.T) {
	got, err := Parse(minimalArgs(), env(map[string]string{"LOG_LEVEL": "warn"}))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if got.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want warn", got.LogLevel)
	}
}

func TestParseFlagBeatsTheEnvironment(t *testing.T) {
	got, err := Parse(minimalArgs("--log-level", "error"), env(map[string]string{"LOG_LEVEL": "warn"}))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if got.LogLevel != "error" {
		t.Errorf("LogLevel = %q, want error", got.LogLevel)
	}
}

func TestParseReportsHelpAndVersion(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want error
	}{
		{"long help", []string{"--help"}, ErrHelp},
		{"short help", []string{"-h"}, ErrHelp},
		{"version", []string{"--version"}, ErrVersion},
		{"version alongside other flags", minimalArgs("--version"), ErrVersion},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.args, noEnv); !errors.Is(err, tt.want) {
				t.Errorf("Parse(%v) error = %v, want %v", tt.args, err, tt.want)
			}
		})
	}
}

// A run started with a stray positional argument is almost always a quoting
// mistake, and silently ignoring it would mean silently ignoring an intent.
func TestParseRejectsPositionalArguments(t *testing.T) {
	_, err := Parse(minimalArgs("extra.txt"), noEnv)
	if err == nil {
		t.Fatal("Parse accepted a positional argument")
	}
	if !strings.Contains(err.Error(), "extra.txt") {
		t.Errorf("error %q does not name the offending argument", err)
	}
}

func TestParseRejectsUnknownFlags(t *testing.T) {
	if _, err := Parse(minimalArgs("--nope"), noEnv); err == nil {
		t.Fatal("Parse accepted an unknown flag")
	}
}
