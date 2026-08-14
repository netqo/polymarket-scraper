package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"time"
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
)

// Parse turns command line arguments into a validated Config.
//
// Defaults come from New, then the environment supplies the log level, then
// flags override both. The flag package's own usage output is suppressed: the
// hand-written text in usage.go is what an operator and a consuming agent read,
// and having two sources for it would guarantee they eventually disagree.
func Parse(args []string, lookupEnv func(string) (string, bool)) (Config, error) {
	cfg := New()
	if level, ok := lookupEnv("LOG_LEVEL"); ok && level != "" {
		cfg.LogLevel = level
	}

	var showVersion bool
	fs := newFlagSet(&cfg, &showVersion)

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Config{}, ErrHelp
		}
		return Config{}, err
	}

	if showVersion {
		return Config{}, ErrVersion
	}
	if rest := fs.Args(); len(rest) > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q: every input is a flag", rest[0])
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// newFlagSet binds every flag to cfg.
//
// It is separate from Parse so a test can walk the bound flags and check them
// against the hand-written help text in both directions. That is what keeps
// requirement F1's "--help is accurate and complete" true as flags are added,
// rather than true only on the day it was written.
func newFlagSet(cfg *Config, showVersion *bool) *flag.FlagSet {
	fs := flag.NewFlagSet(programName, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	fs.BoolVar(showVersion, "version", false, "")

	fs.StringVar(&cfg.TokensPath, "tokens", cfg.TokensPath, "")
	fs.StringVar(&cfg.OutPath, "out", cfg.OutPath, "")
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

	return fs
}

// seconds is a flag value for a duration expressed as a bare number of seconds.
//
// Requirement A2 specifies the scan duration in seconds, and a consuming agent
// fills in the invocation once and never revisits it, so "--duration 90" has to
// mean ninety seconds. Anything that is not a bare number falls through to Go's
// duration syntax, so "--duration 1m30s" also works for a human.
type seconds struct{ target *time.Duration }

func (s seconds) String() string {
	if s.target == nil {
		return ""
	}

	return strconv.FormatFloat(s.target.Seconds(), 'g', -1, 64)
}

func (s seconds) Set(raw string) error {
	if value, err := strconv.ParseFloat(raw, 64); err == nil {
		*s.target = time.Duration(value * float64(time.Second))
		return nil
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("expected a number of seconds or a duration such as 1m30s, got %q", raw)
	}
	*s.target = parsed

	return nil
}
