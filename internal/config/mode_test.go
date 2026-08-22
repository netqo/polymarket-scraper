package config

import (
	"strings"
	"testing"
)

func TestDefaultModeIsProduction(t *testing.T) {
	got, err := Parse(minimalArgs(), noEnv)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if got.Mode != ModeProduction {
		t.Errorf("Mode = %q, want %q", got.Mode, ModeProduction)
	}
	if got.LogLevel != defaultLogLevel {
		t.Errorf("LogLevel = %q, want the default %q", got.LogLevel, defaultLogLevel)
	}
}

// A mode is a bundle of defaults, so it has to move more than one thing or it
// would just be a slower way of writing --log-level.
func TestDebugModeTurnsSeveralThingsUp(t *testing.T) {
	got, err := Parse(minimalArgs("--mode", ModeDebug), noEnv)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if got.Mode != ModeDebug {
		t.Errorf("Mode = %q, want %q", got.Mode, ModeDebug)
	}
	if got.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", got.LogLevel)
	}
	// Zero means no limit: in debug the reason to be watching the terminal is
	// usually the value that would otherwise have been cut.
	if got.ConsoleValueLimit != 0 {
		t.Errorf("ConsoleValueLimit = %d, want 0 in debug", got.ConsoleValueLimit)
	}
}

// A mode decides what a run does when it is not told otherwise. It never
// overrides an instruction.
func TestAnExplicitSettingBeatsTheMode(t *testing.T) {
	got, err := Parse(minimalArgs("--mode", ModeDebug, "--log-level", "error"), noEnv)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if got.LogLevel != "error" {
		t.Errorf("LogLevel = %q, want the flag to beat the mode", got.LogLevel)
	}
	if got.Mode != ModeDebug {
		t.Errorf("Mode = %q, want the mode to still be recorded", got.Mode)
	}
}

func TestModeComesFromTheMostSpecificSource(t *testing.T) {
	path := writeConfig(t, "mode = \"debug\"\n")

	fromFile, err := Parse(minimalArgs("--config", path), noEnv)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if fromFile.Mode != ModeDebug {
		t.Errorf("Mode = %q, want the file's debug", fromFile.Mode)
	}

	fromEnv, err := Parse(minimalArgs("--config", path), env(map[string]string{EnvMode: ModeProduction}))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if fromEnv.Mode != ModeProduction {
		t.Errorf("Mode = %q, want the environment to beat the file", fromEnv.Mode)
	}

	fromFlag, err := Parse(
		minimalArgs("--config", path, "--mode", ModeProduction),
		env(map[string]string{EnvMode: ModeDebug}),
	)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if fromFlag.Mode != ModeProduction {
		t.Errorf("Mode = %q, want the flag to beat both", fromFlag.Mode)
	}
}

// Unlike an unrecognised log level, which falls back to info, a wrong mode is
// worth refusing: it is asked for deliberately, and silently running in
// production when debug was requested wastes the run it was needed for.
func TestAnUnknownModeIsRejected(t *testing.T) {
	sources := []struct {
		name  string
		parse func() error
	}{
		{
			name: "flag",
			parse: func() error {
				_, err := Parse(minimalArgs("--mode", "verbose"), noEnv)
				return err
			},
		},
		{
			name: "environment",
			parse: func() error {
				_, err := Parse(minimalArgs(), env(map[string]string{EnvMode: "verbose"}))
				return err
			},
		},
		{
			name: "file",
			parse: func() error {
				path := writeConfig(t, "mode = \"verbose\"\n")
				_, err := Parse(minimalArgs("--config", path), noEnv)
				return err
			},
		},
	}

	for _, source := range sources {
		t.Run(source.name, func(t *testing.T) {
			err := source.parse()
			if err == nil {
				t.Fatal("Parse accepted an unknown mode")
			}
			if !strings.Contains(err.Error(), "verbose") {
				t.Errorf("error %q does not name the offending value", err)
			}
		})
	}
}
