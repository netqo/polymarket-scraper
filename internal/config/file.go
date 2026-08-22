package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/BurntSushi/toml"
)

// DefaultConfigName is the file looked for in the working directory when no
// path is given. Its absence is not an error: the scraper is fully usable from
// flags alone, and a file is a convenience rather than a requirement.
const DefaultConfigName = "polymarket-scraper.toml"

// duration accepts either a bare number of seconds or a Go duration string.
//
// TOML has no duration type, so these arrive as text. The two spellings are
// both accepted for the same reason the flags accept both: the specification
// talks in seconds, and a person maintaining the file thinks in "1m30s".
type duration struct{ d time.Duration }

// UnmarshalText implements encoding.TextUnmarshaler for the TOML decoder.
func (v *duration) UnmarshalText(text []byte) error {
	parsed, err := parseDuration(string(text))
	if err != nil {
		return err
	}
	v.d = parsed

	return nil
}

// parseDuration reads the duration spelling shared by the flags and the file.
func parseDuration(raw string) (time.Duration, error) {
	if value, err := strconv.ParseFloat(raw, 64); err == nil {
		return time.Duration(value * float64(time.Second)), nil
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("expected a number of seconds or a duration such as 1m30s, got %q", raw)
	}

	return parsed, nil
}

// file is the on-disk shape.
//
// Every field is a pointer so that "absent" and "set to the zero value" stay
// distinguishable. Without that, a file saying `strict_best_bid_ask = false`
// would be indistinguishable from one that did not mention it, and there would
// be no way to let a mode or a default supply a value only when the file is
// silent.
type file struct {
	Mode *string `toml:"mode"`

	Input  *inputSection  `toml:"input"`
	Window *windowSection `toml:"window"`

	Websocket *websocketSection `toml:"websocket"`
	REST      *restSection      `toml:"rest"`
	Logging   *loggingSection   `toml:"logging"`
	Limits    *limitsSection    `toml:"limits"`
}

type inputSection struct {
	Tokens *string `toml:"tokens"`
	Out    *string `toml:"out"`
}

type windowSection struct {
	Duration *duration `toml:"duration"`
	Grace    *duration `toml:"grace"`
}

type websocketSection struct {
	URL                     *string   `toml:"url"`
	MaxAssetsPerConnection  *int      `toml:"max_assets_per_connection"`
	PingInterval            *duration `toml:"ping_interval"`
	IdleTimeout             *duration `toml:"idle_timeout"`
	ReorderTolerance        *duration `toml:"reorder_tolerance"`
	StrictBestBidAsk        *bool     `toml:"strict_best_bid_ask"`
	DiscoverLimit           *int      `toml:"discover_limit"`
	ReconnectInitialBackoff *duration `toml:"reconnect_initial_backoff"`
	ReconnectMaxBackoff     *duration `toml:"reconnect_max_backoff"`
	ReadLimit               *int64    `toml:"read_limit"`
}

type restSection struct {
	URL            *string   `toml:"url"`
	Only           *bool     `toml:"only"`
	Rate           *float64  `toml:"rate"`
	BatchSize      *int      `toml:"batch_size"`
	Attempts       *int      `toml:"attempts"`
	Timeout        *duration `toml:"timeout"`
	InitialBackoff *duration `toml:"initial_backoff"`
	MaxBackoff     *duration `toml:"max_backoff"`
	MaxRetryAfter  *duration `toml:"max_retry_after"`
	ResyncWorkers  *int      `toml:"resync_workers"`
}

type loggingSection struct {
	Level             *string            `toml:"level"`
	File              *string            `toml:"file"`
	ConsoleValueLimit *int               `toml:"console_value_limit"`
	Categories        *categoriesSection `toml:"categories"`
}

type categoriesSection struct {
	Startup    *bool `toml:"startup"`
	Progress   *bool `toml:"progress"`
	Connection *bool `toml:"connection"`
	Flags      *bool `toml:"flags"`
	REST       *bool `toml:"rest"`
	Decode     *bool `toml:"decode"`
	Discovery  *bool `toml:"discovery"`
}

type limitsSection struct {
	MaxErrors      *int `toml:"max_errors"`
	MaxEvents      *int `toml:"max_events"`
	MaxErrorLength *int `toml:"max_error_length"`
}

// loadFile reads a settings file.
//
// Unknown keys are rejected rather than ignored. A silently ignored key is the
// worst outcome for the audience this file exists for: an agent editing it has
// no way to tell a typo from a setting that had no effect, and would conclude
// the scraper ignored an instruction it never received.
func loadFile(path string) (*file, error) {
	// #nosec G304 -- the path is operator-supplied, which is the point of it.
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("reading the config file: %w", err)
	}

	var loaded file
	meta, err := toml.Decode(string(data), &loaded)
	if err != nil {
		return nil, fmt.Errorf("config file %s: %w", path, err)
	}

	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf(
			"config file %s: unknown setting %q. A misspelled setting is silently "+
				"ignored by most tools, which is how a run ends up not doing what its "+
				"file says; see the commented example for the accepted names",
			path, undecoded[0].String())
	}

	return &loaded, nil
}

// resolveConfigPath decides which settings file to read, and whether its
// absence is an error.
//
// A path given explicitly must exist: it was named on purpose, and carrying on
// without it would run with settings nobody asked for. The default name is
// looked for silently, so a directory without one behaves exactly as before.
func resolveConfigPath(explicit string, lookupEnv func(string) (string, bool)) (path string, required bool) {
	if explicit != "" {
		return explicit, true
	}

	if fromEnv, ok := lookupEnv(EnvConfig); ok && fromEnv != "" {
		return fromEnv, true
	}

	if _, err := os.Stat(DefaultConfigName); err == nil {
		return DefaultConfigName, false
	}

	return "", false
}

// missingIsFatal reports whether a failure to read the file should stop the run.
func missingIsFatal(err error, required bool) bool {
	return required || !errors.Is(err, fs.ErrNotExist)
}

// apply copies everything the file set onto a configuration.
//
// Nothing the file is silent about is touched, which is what makes the
// precedence chain work: defaults, then the mode's bundle, then this, then the
// environment, then the flags.
func (f *file) apply(cfg *Config) {
	if f == nil {
		return
	}

	if s := f.Input; s != nil {
		assign(&cfg.TokensPath, s.Tokens)
		assign(&cfg.OutPath, s.Out)
	}

	if s := f.Window; s != nil {
		assignDuration(&cfg.Duration, s.Duration)
		assignDuration(&cfg.Grace, s.Grace)
	}

	if s := f.Websocket; s != nil {
		assign(&cfg.WSURL, s.URL)
		assign(&cfg.MaxAssetsPerConnection, s.MaxAssetsPerConnection)
		assignDuration(&cfg.PingInterval, s.PingInterval)
		assignDuration(&cfg.IdleTimeout, s.IdleTimeout)
		assignDuration(&cfg.ReorderTolerance, s.ReorderTolerance)
		assign(&cfg.StrictBestBidAsk, s.StrictBestBidAsk)
		assign(&cfg.DiscoverLimit, s.DiscoverLimit)
		assignDuration(&cfg.ReconnectInitialBackoff, s.ReconnectInitialBackoff)
		assignDuration(&cfg.ReconnectMaxBackoff, s.ReconnectMaxBackoff)
		assign(&cfg.ReadLimit, s.ReadLimit)
	}

	if s := f.REST; s != nil {
		assign(&cfg.RESTURL, s.URL)
		assign(&cfg.RESTOnly, s.Only)
		assign(&cfg.RESTRate, s.Rate)
		assign(&cfg.RESTBatchSize, s.BatchSize)
		assign(&cfg.RESTAttempts, s.Attempts)
		assignDuration(&cfg.RESTTimeout, s.Timeout)
		assignDuration(&cfg.RESTInitialBackoff, s.InitialBackoff)
		assignDuration(&cfg.RESTMaxBackoff, s.MaxBackoff)
		assignDuration(&cfg.RESTMaxRetryAfter, s.MaxRetryAfter)
		assign(&cfg.ResyncWorkers, s.ResyncWorkers)
	}

	if s := f.Logging; s != nil {
		assign(&cfg.LogLevel, s.Level)
		assign(&cfg.LogFile, s.File)
		assign(&cfg.ConsoleValueLimit, s.ConsoleValueLimit)

		if c := s.Categories; c != nil {
			assign(&cfg.LogCategories.Startup, c.Startup)
			assign(&cfg.LogCategories.Progress, c.Progress)
			assign(&cfg.LogCategories.Connection, c.Connection)
			assign(&cfg.LogCategories.Flags, c.Flags)
			assign(&cfg.LogCategories.REST, c.REST)
			assign(&cfg.LogCategories.Decode, c.Decode)
			assign(&cfg.LogCategories.Discovery, c.Discovery)
		}
	}

	if s := f.Limits; s != nil {
		assign(&cfg.MaxErrors, s.MaxErrors)
		assign(&cfg.MaxEvents, s.MaxEvents)
		assign(&cfg.MaxErrorLength, s.MaxErrorLength)
	}
}

// assign copies a value the file provided, leaving the target alone otherwise.
func assign[T any](target *T, value *T) {
	if value != nil {
		*target = *value
	}
}

// assignDuration is assign for the wrapper the TOML decoder needs.
func assignDuration(target *time.Duration, value *duration) {
	if value != nil {
		*target = value.d
	}
}
