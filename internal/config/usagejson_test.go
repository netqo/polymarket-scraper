package config

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestHelpSpellings(t *testing.T) {
	tests := []struct {
		args []string
		want error
	}{
		{[]string{"--help"}, ErrHelp},
		{[]string{"-help"}, ErrHelp},
		{[]string{"-h"}, ErrHelp},
		{[]string{"--help=text"}, ErrHelp},
		{[]string{"--help=json"}, ErrHelpJSON},
		{[]string{"-help=json"}, ErrHelpJSON},
		// Help wins over everything else on the line: someone asking how to use
		// the tool is not also asking it to run.
		{minimalArgs("--help=json"), ErrHelpJSON},
	}

	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			if _, err := Parse(tt.args, noEnv); !errors.Is(err, tt.want) {
				t.Errorf("Parse(%v) error = %v, want %v", tt.args, err, tt.want)
			}
		})
	}
}

func TestAnUnknownHelpFormatIsRefused(t *testing.T) {
	_, err := Parse([]string{"--help=yaml"}, noEnv)
	if err == nil {
		t.Fatal("Parse accepted an unknown help format")
	}
	if !strings.Contains(err.Error(), "json") {
		t.Errorf("error %q does not say which formats exist", err)
	}
}

// decodeHelp parses the machine-readable help.
func decodeHelp(t *testing.T) HelpDocument {
	t.Helper()

	encoded, err := UsageJSON()
	if err != nil {
		t.Fatalf("UsageJSON returned error: %v", err)
	}

	var document HelpDocument
	if err := json.Unmarshal([]byte(encoded), &document); err != nil {
		t.Fatalf("the help document does not parse: %v", err)
	}

	return document
}

// Derived from the flag set rather than written down, so a flag cannot exist
// without appearing here. Checking it is what keeps that true.
func TestJSONHelpListsEveryBoundFlag(t *testing.T) {
	document := decodeHelp(t)

	var named []string
	for _, f := range document.Flags {
		named = append(named, f.Name)
	}
	slices.Sort(named)

	if bound := boundFlags(); !slices.Equal(named, bound) {
		t.Errorf("flags = %v, want the bound set %v", named, bound)
	}
}

// A default that is not the one the flag would actually use is worse than no
// default at all, because a reader would trust it.
func TestJSONHelpReportsRealDefaults(t *testing.T) {
	document := decodeHelp(t)

	defaults := make(map[string]string, len(document.Flags))
	for _, f := range document.Flags {
		defaults[f.Name] = f.Default
	}

	// Spot-checked against values the flag set is built from, in the spelling
	// the flag itself accepts back.
	checks := map[string]string{
		"duration":                  "90",
		"grace":                     "30",
		"rest-rate":                 "10",
		"max-assets-per-connection": "400",
		"mode":                      ModeProduction,
		"log-level":                 defaultLogLevel,
	}

	for name, want := range checks {
		if got := defaults[name]; got != want {
			t.Errorf("default for --%s = %q, want %q", name, got, want)
		}
	}
}

// The prose is included whole rather than broken into fields, because the
// guarantees it states are the part a consuming agent most needs and they do
// not survive being chopped up.
func TestJSONHelpCarriesTheProseVerbatim(t *testing.T) {
	if got := decodeHelp(t).UsageText; got != Usage() {
		t.Error("usage_text is not the --help output verbatim")
	}
}

// A section named here but absent from the example is a section nobody editing
// the file would ever find.
func TestJSONHelpSectionsExistInTheExample(t *testing.T) {
	contents, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("reading the example: %v", err)
	}
	example := string(contents)

	for _, section := range decodeHelp(t).ConfigFile.Sections {
		if !strings.Contains(example, "["+section+"]") {
			t.Errorf("the help declares a %q section that %s does not contain", section, examplePath)
		}
	}
}

func TestJSONHelpDescribesEveryExitCode(t *testing.T) {
	document := decodeHelp(t)

	var codes []int
	for _, exit := range document.ExitCodes {
		codes = append(codes, exit.Code)
		if exit.Meaning == "" {
			t.Errorf("exit code %d has no explanation", exit.Code)
		}
	}

	if want := []int{0, 1, 2, 3}; !slices.Equal(codes, want) {
		t.Errorf("exit codes = %v, want %v", codes, want)
	}

	// The text help states the same set, and the two disagreeing would leave a
	// reader unable to tell which was current.
	usage := Usage()
	for _, exit := range document.ExitCodes {
		if !strings.Contains(usage, "  "+strconv.Itoa(exit.Code)+"   ") {
			t.Errorf("the text help does not list exit code %d", exit.Code)
		}
	}
}

func TestJSONHelpNamesTheEnvironmentVariables(t *testing.T) {
	document := decodeHelp(t)

	named := make(map[string]string, len(document.Environment))
	for _, variable := range document.Environment {
		named[variable.Name] = variable.Flag
	}

	for _, want := range []string{EnvLogLevel, EnvMode, EnvConfig} {
		flagName, listed := named[want]
		if !listed {
			t.Errorf("%s is not listed in the machine-readable help", want)
			continue
		}
		if !slices.Contains(boundFlags(), flagName) {
			t.Errorf("%s claims to stand in for --%s, which is not a flag", want, flagName)
		}
	}
}
