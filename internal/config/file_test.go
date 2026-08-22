package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// examplePath is the commented file the documentation tells people to copy.
const examplePath = "../../polymarket-scraper.example.toml"

// writeConfig puts a settings file in a temporary directory and returns its
// path.
func writeConfig(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "settings.toml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing the settings file: %v", err)
	}

	return path
}

// inDir runs the rest of the test with the working directory changed, which is
// how the automatic lookup of ./polymarket-scraper.toml is exercised.
func inDir(t *testing.T, dir string) {
	t.Helper()

	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
}

// The example claims that a copy with nothing changed behaves exactly like
// running with no file at all. That is only true for as long as someone keeps
// it true, which is what this checks.
func TestTheExampleFileMatchesTheDefaults(t *testing.T) {
	got, err := Parse(minimalArgs("--config", examplePath), noEnv)
	if err != nil {
		t.Fatalf("the example file does not load: %v", err)
	}

	want := New()
	want.TokensPath = "tokens.txt"
	want.OutPath = "books.json"
	want.ConfigPath = examplePath

	if got != want {
		t.Errorf("loading the example changed something:\n got %+v\nwant %+v", got, want)
	}
}

// Every setting the file can carry should appear in the example, or nobody
// editing it will discover the setting exists.
func TestTheExampleFileMentionsEverySetting(t *testing.T) {
	contents, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("reading the example: %v", err)
	}
	text := string(contents)

	settings := []string{
		"mode",
		"tokens", "out",
		"duration", "grace",
		"url", "max_assets_per_connection", "ping_interval", "idle_timeout",
		"reorder_tolerance", "strict_best_bid_ask", "discover_limit",
		"reconnect_initial_backoff", "reconnect_max_backoff", "read_limit",
		"only", "rate", "batch_size", "attempts", "timeout",
		"initial_backoff", "max_backoff", "max_retry_after", "resync_workers",
		"level", "file", "console_value_limit",
		"max_errors", "max_error_length", "max_events",
	}

	for _, setting := range settings {
		if !strings.Contains(text, setting) {
			t.Errorf("the example does not mention %q", setting)
		}
	}
}

// A typo silently ignored looks exactly like a setting that had no effect, and
// the run quietly does not do what the file says. This is the audience the file
// exists for: an agent editing it has no other way to find out.
func TestAnUnknownSettingIsRejected(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		mustSay  string
	}{
		{"misspelled key", "[window]\ndurationn = \"30s\"\n", "durationn"},
		{"key in the wrong section", "[rest]\nping_interval = \"5s\"\n", "ping_interval"},
		{"unknown section", "[nonsense]\nvalue = 1\n", "nonsense"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, tt.contents)

			_, err := Parse(minimalArgs("--config", path), noEnv)
			if err == nil {
				t.Fatal("Parse accepted an unknown setting")
			}
			if !strings.Contains(err.Error(), tt.mustSay) {
				t.Errorf("error %q does not name the offending setting", err)
			}
		})
	}
}

func TestSettingsFileIsApplied(t *testing.T) {
	path := writeConfig(t, `
[window]
duration = "45s"
grace    = 7

[websocket]
max_assets_per_connection = 120
strict_best_bid_ask       = true

[rest]
rate           = 2.5
attempts       = 9
resync_workers = 2

[logging]
level = "warn"

[limits]
max_errors = 42
`)

	got, err := Parse(minimalArgs("--config", path), noEnv)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"duration", got.Duration, 45 * time.Second},
		{"grace, as a bare number of seconds", got.Grace, 7 * time.Second},
		{"max assets", got.MaxAssetsPerConnection, 120},
		{"strict best bid ask", got.StrictBestBidAsk, true},
		{"rest rate", got.RESTRate, 2.5},
		{"rest attempts", got.RESTAttempts, 9},
		{"resync workers", got.ResyncWorkers, 2},
		{"log level", got.LogLevel, "warn"},
		{"max errors", got.MaxErrors, 42},
		{"config path is recorded", got.ConfigPath, path},
	}

	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s = %v, want %v", check.name, check.got, check.want)
		}
	}
}

// Anything the file does not mention keeps the value it would otherwise have
// had, which is what makes a three-line settings file usable.
func TestSettingsFileLeavesUnmentionedValuesAlone(t *testing.T) {
	path := writeConfig(t, "[window]\nduration = \"45s\"\n")

	got, err := Parse(minimalArgs("--config", path), noEnv)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if got.Grace != DefaultGrace {
		t.Errorf("Grace = %v, want the default %v", got.Grace, DefaultGrace)
	}
	if got.RESTRate != DefaultRESTRate {
		t.Errorf("RESTRate = %v, want the default %v", got.RESTRate, DefaultRESTRate)
	}
}

// "Set to false" and "not mentioned" are different instructions, and the
// pointer fields in the file struct are what keeps them apart.
func TestSettingsFileCanSetAValueToItsZero(t *testing.T) {
	path := writeConfig(t, "[websocket]\ndiscover_limit = 0\nreorder_tolerance = 0\n")

	got, err := Parse(minimalArgs("--config", path), noEnv)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if got.DiscoverLimit != 0 {
		t.Errorf("DiscoverLimit = %d, want the 0 the file asked for", got.DiscoverLimit)
	}
	if got.ReorderTolerance != 0 {
		t.Errorf("ReorderTolerance = %v, want the 0 the file asked for", got.ReorderTolerance)
	}
}

// The file is the considered configuration; a flag is what someone typed for
// one run. The more specific instruction wins.
func TestPrecedenceRunsFromFileThroughEnvironmentToFlags(t *testing.T) {
	path := writeConfig(t, "[logging]\nlevel = \"warn\"\n")

	fromFile, err := Parse(minimalArgs("--config", path), noEnv)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if fromFile.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want the file's warn", fromFile.LogLevel)
	}

	fromEnv, err := Parse(minimalArgs("--config", path), env(map[string]string{"LOG_LEVEL": "error"}))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if fromEnv.LogLevel != "error" {
		t.Errorf("LogLevel = %q, want the environment to beat the file", fromEnv.LogLevel)
	}

	fromFlag, err := Parse(
		minimalArgs("--config", path, "--log-level", "debug"),
		env(map[string]string{"LOG_LEVEL": "error"}),
	)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if fromFlag.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want the flag to beat both", fromFlag.LogLevel)
	}
}

// A file named on purpose must exist. Carrying on without it would run with
// settings nobody asked for.
func TestAnExplicitSettingsFileMustExist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.toml")

	if _, err := Parse(minimalArgs("--config", missing), noEnv); err == nil {
		t.Fatal("Parse accepted a --config path that does not exist")
	}

	fromEnv := env(map[string]string{EnvConfig: missing})
	if _, err := Parse(minimalArgs(), fromEnv); err == nil {
		t.Fatalf("Parse accepted a %s path that does not exist", EnvConfig)
	}
}

// The default name is looked for silently, so a directory without one behaves
// exactly as it did before settings files existed.
func TestTheDefaultSettingsFileIsOptionalButUsed(t *testing.T) {
	empty := t.TempDir()
	inDir(t, empty)

	got, err := Parse(minimalArgs(), noEnv)
	if err != nil {
		t.Fatalf("Parse failed with no settings file present: %v", err)
	}
	if got.ConfigPath != "" {
		t.Errorf("ConfigPath = %q, want empty when no file exists", got.ConfigPath)
	}
	if got.Duration != DefaultDuration {
		t.Errorf("Duration = %v, want the default", got.Duration)
	}

	if err := os.WriteFile(
		filepath.Join(empty, DefaultConfigName),
		[]byte("[window]\nduration = \"11s\"\n"), 0o600,
	); err != nil {
		t.Fatalf("writing the default settings file: %v", err)
	}

	got, err = Parse(minimalArgs(), noEnv)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if got.Duration != 11*time.Second {
		t.Errorf("Duration = %v, want the 11s from ./%s", got.Duration, DefaultConfigName)
	}
	if got.ConfigPath != DefaultConfigName {
		t.Errorf("ConfigPath = %q, want %q", got.ConfigPath, DefaultConfigName)
	}
}

func TestDurationsAcceptBothSpellings(t *testing.T) {
	tests := []struct {
		spelling string
		want     time.Duration
	}{
		{`"1m30s"`, 90 * time.Second},
		{`"90s"`, 90 * time.Second},
		{`"90"`, 90 * time.Second},
		{`90`, 90 * time.Second},
		{`"250ms"`, 250 * time.Millisecond},
		{`0.5`, 500 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.spelling, func(t *testing.T) {
			path := writeConfig(t, "[window]\nduration = "+tt.spelling+"\n")

			got, err := Parse(minimalArgs("--config", path), noEnv)
			if err != nil {
				t.Fatalf("Parse returned error: %v", err)
			}
			if got.Duration != tt.want {
				t.Errorf("Duration = %v, want %v", got.Duration, tt.want)
			}
		})
	}
}

func TestARejectedDurationSaysWhatIsAccepted(t *testing.T) {
	path := writeConfig(t, "[window]\nduration = \"soon\"\n")

	_, err := Parse(minimalArgs("--config", path), noEnv)
	if err == nil {
		t.Fatal("Parse accepted a nonsense duration")
	}
	if !strings.Contains(err.Error(), "1m30s") {
		t.Errorf("error %q does not show the accepted syntax", err)
	}
}

// A setting reachable only from the file still has to be validated, or a typo
// in a number produces a run that behaves strangely rather than one that
// refuses to start.
func TestTuningSettingsAreValidated(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		mustSay  string
	}{
		{"zero attempts", "[rest]\nattempts = 0\n", "rest.attempts"},
		{"no workers", "[rest]\nresync_workers = 0\n", "rest.resync_workers"},
		{"zero timeout", "[rest]\ntimeout = 0\n", "rest.timeout"},
		{"negative limit", "[limits]\nmax_error_length = -1\n", "limits.max_error_length"},
		{"empty error list", "[limits]\nmax_errors = 0\n", "limits.max_errors"},
		{"ceiling below floor", "[rest]\ninitial_backoff = \"5s\"\nmax_backoff = \"1s\"\n", "rest.max_backoff"},
		{"zero read limit", "[websocket]\nread_limit = 0\n", "websocket.read_limit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, tt.contents)

			_, err := Parse(minimalArgs("--config", path), noEnv)
			if err == nil {
				t.Fatal("Parse accepted an unusable value")
			}
			if !strings.Contains(err.Error(), tt.mustSay) {
				t.Errorf("error %q does not name the setting in the file's own spelling", err)
			}
		})
	}
}
