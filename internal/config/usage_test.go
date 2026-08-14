package config

import (
	"flag"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// mentionedFlag finds the flag names the help text documents.
var mentionedFlag = regexp.MustCompile(`(?m)^\s{2}--([a-z0-9-]+)`)

// boundFlags returns every flag name Parse binds.
func boundFlags() []string {
	cfg := New()
	var showVersion bool

	var names []string
	newFlagSet(&cfg, &showVersion).VisitAll(func(f *flag.Flag) {
		names = append(names, f.Name)
	})
	slices.Sort(names)

	return names
}

// documentedFlags returns every flag name the help text mentions.
func documentedFlags() []string {
	var names []string
	for _, match := range mentionedFlag.FindAllStringSubmatch(Usage(), -1) {
		names = append(names, match[1])
	}
	slices.Sort(names)

	return slices.Compact(names)
}

// F1: --help is the ground truth a consuming agent reads before its first run,
// so it has to stay complete as flags are added. Checking both directions is
// what makes that mechanical: a flag can neither be added without documenting
// it nor documented without existing.
func TestUsageDocumentsEveryBoundFlag(t *testing.T) {
	documented := documentedFlags()

	for _, name := range boundFlags() {
		if !slices.Contains(documented, name) {
			t.Errorf("flag --%s is bound but not documented in the help text", name)
		}
	}
}

func TestUsageDocumentsNothingThatDoesNotExist(t *testing.T) {
	bound := boundFlags()
	// --help is handled by the flag package itself rather than bound by us.
	bound = append(bound, "help")

	for _, name := range documentedFlags() {
		if !slices.Contains(bound, name) {
			t.Errorf("help text documents --%s, which is not a real flag", name)
		}
	}
}

// The help text quotes its defaults; if it quoted a value the flag could not
// accept, following the documentation would produce a usage error.
func TestUsageDefaultsAreAcceptedByTheFlags(t *testing.T) {
	tests := []struct {
		flag  string
		value string
	}{
		{"duration", secondsText(DefaultDuration)},
		{"grace", secondsText(DefaultGrace)},
		{"ping-interval", secondsText(DefaultPingInterval)},
		{"idle-timeout", secondsText(DefaultIdleTimeout)},
		{"reorder-tolerance", secondsText(DefaultReorderTolerance)},
	}

	usage := Usage()
	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			if !strings.Contains(usage, tt.value) {
				t.Fatalf("help text does not quote the default %q for --%s", tt.value, tt.flag)
			}

			args := []string{"--tokens", "t.txt", "--out", "o.json", "--" + tt.flag, tt.value}
			if _, err := Parse(args, noEnv); err != nil {
				t.Errorf("--%s %s was rejected: %v", tt.flag, tt.value, err)
			}
		})
	}
}

// The help text is the only place the tool explains what it will not do, which
// is the part a consuming agent most needs to know.
func TestUsageStatesTheGuarantees(t *testing.T) {
	usage := Usage()

	required := []string{
		"read-only",
		"no API keys",
		"resync_failed",
		"exactly once",
		"atomic",
		"stderr",
		"EXIT STATUS",
	}

	for _, phrase := range required {
		if !strings.Contains(usage, phrase) {
			t.Errorf("help text does not mention %q", phrase)
		}
	}
}
