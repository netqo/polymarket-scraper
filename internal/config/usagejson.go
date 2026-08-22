package config

import (
	"encoding/json"
	"flag"
	"fmt"
)

// Help formats accepted by --help.
const (
	// HelpText is the hand-written text, which is what a person reads.
	HelpText = "text"

	// HelpJSON is the same information with the parts a program would
	// otherwise have to parse out of the prose given as data.
	HelpJSON = "json"
)

// HelpDocument is the machine-readable description of the command line.
//
// The prose is not restated here. It is included whole, in UsageText, because
// the guarantees it states are the part a consuming agent most needs and they
// do not survive being broken into fields. What this adds is the handful of
// facts a program would otherwise have to parse back out of that prose: the
// flags and their defaults, the exit codes, the settings file's shape.
//
// Everything except UsageText is derived from the code rather than written
// down, so none of it can drift.
type HelpDocument struct {
	Program string `json:"program"`

	Flags       []HelpFlag     `json:"flags"`
	Environment []HelpVariable `json:"environment"`
	ExitCodes   []HelpExitCode `json:"exit_codes"`
	ConfigFile  HelpConfigFile `json:"config_file"`

	// UsageText is the full --help output, verbatim.
	UsageText string `json:"usage_text"`
}

// HelpFlag is one command line flag.
type HelpFlag struct {
	Name string `json:"name"`

	// Default is the value used when the flag is absent, rendered the way the
	// flag itself would accept it back.
	Default string `json:"default"`
}

// HelpVariable is one environment variable.
type HelpVariable struct {
	Name string `json:"name"`
	Flag string `json:"equivalent_flag"`
}

// HelpExitCode is one process exit status.
type HelpExitCode struct {
	Code    int    `json:"code"`
	Meaning string `json:"meaning"`
}

// HelpConfigFile describes the settings file.
type HelpConfigFile struct {
	// DefaultName is looked for in the working directory when no path is given.
	DefaultName string `json:"default_name"`

	// Sections are the table names the file accepts.
	Sections []string `json:"sections"`

	// Modes are the accepted values of the mode setting.
	Modes []string `json:"modes"`

	// LogCategories are the switches under logging.categories.
	LogCategories []string `json:"log_categories"`
}

// Exit codes, described here so that the one place that knows what they mean is
// also the place that reports them. The numbers themselves belong to the
// command; this is the explanation of them, kept beside the rest of the command
// line contract.
var exitCodes = []HelpExitCode{
	{0, "the output document was written and is valid; individual tokens may still have failed, and their status says so"},
	{1, "the run failed and the output is unusable"},
	{2, "the command line was wrong; nothing was written"},
	{3, "the watchdog terminated a run that would not shut down"},
}

// UsageJSON renders the command line contract as JSON.
func UsageJSON() (string, error) {
	encoded, err := json.MarshalIndent(helpDocument(), "", "  ")
	if err != nil {
		return "", fmt.Errorf("encoding the help document: %w", err)
	}

	return string(encoded), nil
}

// helpDocument assembles the description from the live flag set.
func helpDocument() HelpDocument {
	return HelpDocument{
		Program:     programName,
		Flags:       helpFlags(),
		Environment: helpEnvironment(),
		ExitCodes:   exitCodes,
		ConfigFile: HelpConfigFile{
			DefaultName:   DefaultConfigName,
			Sections:      []string{"input", "window", "websocket", "rest", "logging", "logging.categories", "limits"},
			Modes:         Modes,
			LogCategories: LogCategories{}.Disabled(),
		},
		UsageText: Usage(),
	}
}

// helpFlags walks the bound flags, so a flag cannot exist without appearing
// here.
func helpFlags() []HelpFlag {
	cfg := New()

	var (
		showVersion bool
		help        string
	)

	var flags []HelpFlag
	newFlagSet(&cfg, &showVersion, &help).VisitAll(func(f *flag.Flag) {
		flags = append(flags, HelpFlag{Name: f.Name, Default: f.DefValue})
	})

	return flags
}

// helpEnvironment lists the variables and the flag each one stands in for.
func helpEnvironment() []HelpVariable {
	return []HelpVariable{
		{Name: EnvLogLevel, Flag: "log-level"},
		{Name: EnvMode, Flag: "mode"},
		{Name: EnvConfig, Flag: "config"},
	}
}

// helpFormat is the value of --help, which may be given bare or with a format.
//
// IsBoolFlag is what allows "--help" on its own alongside "--help=json"; the
// flag package calls Set("true") for the bare form.
type helpFormat struct{ target *string }

func (h helpFormat) String() string {
	if h.target == nil {
		return ""
	}

	return *h.target
}

func (h helpFormat) Set(raw string) error {
	switch raw {
	case "true", HelpText:
		*h.target = HelpText
	case HelpJSON:
		*h.target = HelpJSON
	default:
		return fmt.Errorf("--help takes no value, or one of %q and %q, got %q", HelpText, HelpJSON, raw)
	}

	return nil
}

// IsBoolFlag lets --help be given without a value.
func (helpFormat) IsBoolFlag() bool { return true }
