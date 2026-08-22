package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"time"
)

// Environment variables the scraper reads.
const (
	// EnvLogLevel sets the minimum severity, as --log-level does.
	EnvLogLevel = "LOG_LEVEL"

	// EnvConfig names a settings file, as --config does. A file named this way
	// must exist; being unable to read it is an error rather than a fallback.
	EnvConfig = "POLYMARKET_CONFIG"

	// EnvMode selects a bundle of defaults, as --mode does.
	EnvMode = "POLYMARKET_MODE"
)

// Sentinel results from Parse that are not failures.
//
// They are returned as errors because that is how the flag package reports
// them, but the caller maps them to a successful exit: asking for help is not
// a usage mistake.
var (
	// ErrHelp reports that help was requested.
	ErrHelp = errors.New("help requested")
	// ErrVersion reports that the version was requested.
	ErrVersion = errors.New("version requested")

	// ErrHelpJSON reports that help was requested in machine-readable form.
	ErrHelpJSON = errors.New("machine-readable help requested")
)

// Parse turns command line arguments into a validated Config.
//
// Four sources contribute, each overriding the one before it:
//
//	defaults        New()
//	mode            the bundle --mode implies
//	settings file   --config, POLYMARKET_CONFIG, or ./polymarket-scraper.toml
//	environment     LOG_LEVEL
//	flags           everything on the command line
//
// The order is the conventional one, and the reason it is worth stating is that
// the file exists to be edited by an agent while the flags stay whatever a shell
// script pinned months ago. The more specific instruction, typed at the moment
// of the run, is the one that wins.
//
// The flag package's own usage output is suppressed: the hand-written text in
// usage.go is what an operator and a consuming agent read, and having two
// sources for it would guarantee they eventually disagree.
func Parse(args []string, lookupEnv func(string) (string, bool)) (Config, error) {
	// A first pass over a throwaway configuration, purely to learn the two
	// things the rest of the load depends on: which settings file to read, and
	// which mode's defaults to start from. Both can also come from the file or
	// the environment, so neither can be resolved by the main pass, which has
	// to run last.
	probe, probeSet, err := probeFlags(args)
	if err != nil {
		return Config{}, err
	}

	path, loaded, err := readSettings(probe.ConfigPath, lookupEnv)
	if err != nil {
		return Config{}, err
	}

	mode := resolveMode(probe, probeSet, loaded, lookupEnv)
	if err := validateMode(mode); err != nil {
		return Config{}, err
	}

	cfg := New()
	cfg.applyMode(mode)
	loaded.apply(&cfg)
	applyEnv(&cfg, lookupEnv)

	var (
		showVersion bool
		help        string
	)
	fs := newFlagSet(&cfg, &showVersion, &help)
	if err := fs.Parse(args); err != nil {
		return Config{}, translateFlagError(err)
	}
	if requested := helpRequest(help); requested != nil {
		return Config{}, requested
	}
	if showVersion {
		return Config{}, ErrVersion
	}
	if rest := fs.Args(); len(rest) > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q: every input is a flag", rest[0])
	}

	// Recorded rather than parsed from, so that a log and the run's own summary
	// can say where the settings came from.
	cfg.ConfigPath = path
	cfg.Mode = mode

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// probeFlags runs the first pass, reporting the parsed configuration and which
// flag names were given explicitly.
func probeFlags(args []string) (Config, map[string]bool, error) {
	probe := New()

	var (
		showVersion bool
		help        string
	)
	fs := newFlagSet(&probe, &showVersion, &help)
	if err := fs.Parse(args); err != nil {
		return Config{}, nil, translateFlagError(err)
	}
	if requested := helpRequest(help); requested != nil {
		return Config{}, nil, requested
	}
	if showVersion {
		return Config{}, nil, ErrVersion
	}

	// Visit reports only the flags actually present on the command line, which
	// is the difference between "--mode production" and not saying anything.
	given := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })

	return probe, given, nil
}

// readSettings loads the settings file, if there is one to load.
func readSettings(explicit string, lookupEnv func(string) (string, bool)) (string, *file, error) {
	path, required := resolveConfigPath(explicit, lookupEnv)
	if path == "" {
		return "", nil, nil
	}

	loaded, err := loadFile(path)
	if err != nil {
		if missingIsFatal(err, required) {
			return "", nil, err
		}

		// The default file simply was not there, which is the normal case.
		return "", nil, nil
	}

	return path, loaded, nil
}

// resolveMode picks the mode from the most specific source that named one.
func resolveMode(probe Config, given map[string]bool, loaded *file, lookupEnv func(string) (string, bool)) string {
	if given["mode"] {
		return probe.Mode
	}
	if fromEnv, ok := lookupEnv(EnvMode); ok && fromEnv != "" {
		return fromEnv
	}
	if loaded != nil && loaded.Mode != nil {
		return *loaded.Mode
	}

	return ModeProduction
}

// applyEnv layers the environment over the file.
func applyEnv(cfg *Config, lookupEnv func(string) (string, bool)) {
	if level, ok := lookupEnv(EnvLogLevel); ok && level != "" {
		cfg.LogLevel = level
	}
}

// helpRequest maps a --help format onto its sentinel, or nil when help was not
// asked for.
func helpRequest(format string) error {
	switch format {
	case HelpText:
		return ErrHelp
	case HelpJSON:
		return ErrHelpJSON
	default:
		return nil
	}
}

// translateFlagError maps the flag package's help sentinel onto ours.
//
// -h is still handled by the flag package, since binding "help" does not bind
// its shorthand, and it means the same thing as the bare --help.
func translateFlagError(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return ErrHelp
	}

	return err
}

// newFlagSet binds every flag to cfg.
//
// It is separate from Parse so a test can walk the bound flags and check them
// against the hand-written help text in both directions. That is what keeps
// requirement F1's "--help is accurate and complete" true as flags are added,
// rather than true only on the day it was written.
//
// Not every setting has a flag. The tuning values consolidated out of the
// engine and the clients are reachable from the settings file alone, because a
// command line with thirty entries buries the dozen anyone actually sets.
func newFlagSet(cfg *Config, showVersion *bool, help *string) *flag.FlagSet {
	fs := flag.NewFlagSet(programName, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	fs.BoolVar(showVersion, "version", false, "")
	// Bound rather than left to the flag package, so that --help=json can ask
	// for the same contract as data.
	fs.Var(helpFormat{help}, "help", "")

	fs.StringVar(&cfg.ConfigPath, "config", cfg.ConfigPath, "")
	fs.StringVar(&cfg.Mode, "mode", cfg.Mode, "")

	fs.StringVar(&cfg.TokensPath, "tokens", cfg.TokensPath, "")
	fs.StringVar(&cfg.OutPath, "out", cfg.OutPath, "")
	fs.StringVar(&cfg.StreamPath, "stream", cfg.StreamPath, "")
	fs.Var(seconds{&cfg.Duration}, "duration", "")
	fs.Var(seconds{&cfg.Grace}, "grace", "")

	fs.BoolVar(&cfg.RESTOnly, "rest-only", cfg.RESTOnly, "")
	fs.Float64Var(&cfg.RESTRate, "rest-rate", cfg.RESTRate, "")
	fs.IntVar(&cfg.RESTBatchSize, "rest-batch-size", cfg.RESTBatchSize, "")

	fs.IntVar(&cfg.MaxAssetsPerConnection, "max-assets-per-connection", cfg.MaxAssetsPerConnection, "")
	fs.Var(seconds{&cfg.PingInterval}, "ping-interval", "")
	fs.Var(seconds{&cfg.IdleTimeout}, "idle-timeout", "")
	fs.Var(seconds{&cfg.ReorderTolerance}, "reorder-tolerance", "")
	fs.BoolVar(&cfg.StrictBestBidAsk, "strict-best-bid-ask", cfg.StrictBestBidAsk, "")
	fs.IntVar(&cfg.DiscoverLimit, "discover-limit", cfg.DiscoverLimit, "")

	fs.StringVar(&cfg.WSURL, "ws-url", cfg.WSURL, "")
	fs.StringVar(&cfg.RESTURL, "rest-url", cfg.RESTURL, "")
	fs.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "")
	fs.StringVar(&cfg.LogFile, "log-file", cfg.LogFile, "")

	return fs
}

// seconds is a flag value for a duration expressed as a bare number of seconds.
//
// Requirement A2 specifies the scan duration in seconds, and a consuming agent
// fills in the invocation once and never revisits it, so "--duration 90" has to
// mean ninety seconds. Anything that is not a bare number falls through to Go's
// duration syntax, so "--duration 1m30s" also works for a human. The settings
// file accepts both spellings through the same parser.
type seconds struct{ target *time.Duration }

// String implements flag.Value, reporting the current value as a bare number of
// seconds so that --help and error messages use the unit the flag accepts.
func (s seconds) String() string {
	if s.target == nil {
		return ""
	}

	return strconv.FormatFloat(s.target.Seconds(), 'g', -1, 64)
}

// Set implements flag.Value.
func (s seconds) Set(raw string) error {
	parsed, err := parseDuration(raw)
	if err != nil {
		return err
	}
	*s.target = parsed

	return nil
}
