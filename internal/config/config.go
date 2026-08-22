// Package config owns the command line surface and the run timeline.
//
// Two things live here that might look unrelated. The flag set and its help
// text are the contract a consuming agent reads before it ever runs the binary
// (requirement F1), so the help text is written by hand rather than generated,
// and it is the ground truth. The shutdown budget is arithmetic derived from
// the same flags, and putting it here keeps requirement A4's timeline a pure
// function that can be checked by a table test rather than by watching a real
// process fail to exit.
package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"time"
)

// Default endpoints. Both are overridable so tests can point the binary at an
// in-process fake, which is what keeps the test suite off the network.
const (
	DefaultWSURL   = "wss://ws-subscriptions-clob.polymarket.com/ws/market"
	DefaultRESTURL = "https://clob.polymarket.com"
)

// Defaults for the collection window.
const (
	DefaultDuration = 90 * time.Second
	DefaultGrace    = 30 * time.Second

	// maxDuration rejects a window long enough that a mistyped flag would look
	// like a hung process rather than a configuration error.
	maxDuration = time.Hour
)

// REST pacing defaults.
const (
	// DefaultRESTRate is requirement E2's "~10 requests per second". Polymarket
	// documents far higher limits, so this is politeness rather than necessity,
	// and it is adjustable.
	DefaultRESTRate = 10.0

	// DefaultRESTBatchSize is how many token ids go into one POST /books call.
	// The endpoint rejects payloads past roughly 500 ids, and the limit is on
	// bytes rather than count, so this leaves room for longer ids.
	DefaultRESTBatchSize = 250

	maxRESTBatchSize = 500
)

// REST client defaults.
//
// These were constants inside the restclient package until the configuration
// was consolidated. They are tuning rather than policy, so they have no flags:
// changing one is a deliberate act that belongs in a file next to the others,
// not something to be typed on a command line by mistake.
const (
	// DefaultRESTAttempts is how many times a request is tried before giving
	// up. Three is enough to ride out a transient failure and few enough that a
	// failing token cannot eat the run's deadline.
	DefaultRESTAttempts = 3

	// DefaultRESTTimeout bounds a single request, so a connection that accepts
	// and then says nothing cannot stall the run on its own.
	DefaultRESTTimeout = 10 * time.Second

	// DefaultRESTInitialBackoff and DefaultRESTMaxBackoff bound the wait
	// between attempts.
	DefaultRESTInitialBackoff = 250 * time.Millisecond
	DefaultRESTMaxBackoff     = 4 * time.Second

	// DefaultRESTMaxRetryAfter caps how long a server-supplied Retry-After is
	// honoured. A cooperative client should wait, but not past the point where
	// waiting costs more than the data is worth.
	DefaultRESTMaxRetryAfter = 10 * time.Second

	// DefaultResyncWorkers is how many re-seed workers run. They share one rate
	// limiter, so this is not a throughput setting: it is how many requests can
	// be in flight while others wait on the limiter, which keeps a slow
	// response from idling the whole budget.
	DefaultResyncWorkers = 4
)

// Reconnection defaults.
//
// Deliberately short. A dropped connection stops delta delivery for every token
// it carried, and against a 90 second window the old ceiling of 8 seconds spent
// nearly a tenth of the run blind. The tokens themselves are re-seeded over
// REST immediately and independently, so what the wait costs is not trust but
// the updates that happen during it.
//
// The first retry is where the win is: most disconnects are a transient blip
// and succeed immediately. Backing off still matters for the case where the far
// end is genuinely refusing, which is why there is a ceiling at all.
//
// There is no jitter and no setting for one: this is a single process and its
// requests are already paced, so jitter would add randomness to a program that
// otherwise has none.
const (
	DefaultReconnectInitialBackoff = 250 * time.Millisecond
	DefaultReconnectMaxBackoff     = 4 * time.Second
)

// Bounds on what the scraper will hold in memory or write out.
const (
	// DefaultMaxErrors caps how many distinct messages the run's error list
	// holds. Repeats are collapsed, so a connection redialling in a loop costs
	// one entry however long it runs.
	DefaultMaxErrors = 500

	// DefaultMaxEvents caps each announcement list. The announcement feed is
	// global rather than filtered to this run's subscription, so its volume has
	// nothing to do with how many tokens were asked for.
	DefaultMaxEvents = 500

	// DefaultReadLimit bounds a single websocket frame. The initial snapshot
	// for a full shard is the largest thing the server sends and it grows with
	// the number of assets, so this is far above the library's own default.
	DefaultReadLimit = 32 << 20

	// DefaultMaxErrorLength bounds one message in the output document's error
	// list. A decode failure quotes the frame it could not read, and a frame
	// can be kilobytes; these strings are meant to be read and quoted verbatim,
	// so one running to several pages would make the whole list useless.
	DefaultMaxErrorLength = 500

	// DefaultConsoleValueLimit is how much of one attribute value reaches the
	// terminal. About two lines: long enough for a file path or the start of a
	// frame, short enough that one enormous value cannot scroll the rest of the
	// run out of view. The log file applies no limit.
	DefaultConsoleValueLimit = 300
)

// Websocket defaults.
const (
	// DefaultMaxAssetsPerConnection stays well below the point where the server
	// silently stops sending the initial snapshot. That failure mode is the
	// dangerous one: the socket stays open and keeps delivering deltas, so a
	// client that does not check looks healthy while holding no book state.
	DefaultMaxAssetsPerConnection = 400

	// MaxAssetsCeiling is how wide a connection may get in total, which is a
	// different number from the width above. That one decides how the requested
	// tokens are spread across connections; this one bounds a connection that
	// later takes on announced tokens as well.
	//
	// The server stops honouring a subscription somewhere around 750 assets, so
	// this is a deliberate margin below the observed cliff rather than the cliff
	// itself: the failure is silent, and finding its exact edge is not worth
	// discovering in production.
	MaxAssetsCeiling = 700

	// DefaultPingInterval matches the documented keepalive cadence. The frame
	// is the literal text "PING", not a websocket protocol ping.
	DefaultPingInterval = 10 * time.Second

	// DefaultIdleTimeout is how long a connection may deliver nothing at all
	// before it is treated as dead (requirement B2).
	DefaultIdleTimeout = 30 * time.Second

	// DefaultReorderTolerance is how far a timestamp may go backwards before
	// the token is distrusted rather than merely flagged. See the D4 deviation
	// in the README: the feed carries no sequence numbers, so treating every
	// regression as a gap would turn one clock quirk into a resync storm.
	DefaultReorderTolerance = 5 * time.Second

	// DefaultDiscoverLimit caps how many tokens a run will subscribe to
	// mid-window after seeing them announced. The announcement feed is global
	// rather than filtered to our subscription, so this needs a hard budget.
	DefaultDiscoverLimit = 100
)

// defaultLogLevel is the severity used when neither --log-level nor LOG_LEVEL
// is set.
const defaultLogLevel = "info"

// Config is a validated run configuration.
type Config struct {
	// TokensPath is the file listing the token ids to collect.
	TokensPath string
	// OutPath is where the output document is written, atomically.
	OutPath string

	// Duration is the length of the collection window.
	Duration time.Duration
	// Grace is the additional time allowed for shutdown before the watchdog
	// terminates the process.
	Grace time.Duration

	// RESTOnly skips the websocket entirely and snapshots every token over
	// REST. It doubles as a debugging aid and as a mirror of the consuming
	// agent's own fallback path.
	RESTOnly bool
	// RESTRate is the global ceiling on REST requests per second.
	RESTRate float64
	// RESTBatchSize is how many token ids go into one batched book request.
	RESTBatchSize int

	// MaxAssetsPerConnection is the sharding width for websocket subscriptions.
	MaxAssetsPerConnection int
	// PingInterval is how often the keepalive frame is sent.
	PingInterval time.Duration
	// IdleTimeout is how long silence is tolerated before reconnecting.
	IdleTimeout time.Duration
	// ReorderTolerance is how far out of order a delta may arrive before its
	// token is distrusted.
	ReorderTolerance time.Duration
	// StrictBestBidAsk turns a disagreement between the published best quote
	// and the maintained book from a flag into a full resync.
	StrictBestBidAsk bool
	// DiscoverLimit caps mid-window subscriptions to newly announced tokens.
	// Zero disables discovery; announcements are still reported either way,
	// because reporting them is required and subscribing to them is not.
	DiscoverLimit int

	// WSURL and RESTURL are the endpoints to talk to.
	WSURL   string
	RESTURL string

	// LogLevel is the minimum severity written to stderr.
	LogLevel string

	// LogFile additionally appends every log line to a file as the run happens,
	// uncoloured and untruncated. Empty disables it.
	//
	// It exists because stderr is only readable by whatever launched the
	// process, which rules out watching a run in progress from anywhere else.
	// The file is appended to and flushed per line, so it can be tailed while
	// the run is still going rather than only after it exits.
	LogFile string

	// ConfigPath is the settings file this configuration was loaded from, or
	// empty when none was used. It is recorded so a log says where a run's
	// settings came from.
	ConfigPath string

	// Mode selects a bundle of defaults. See the Mode constants.
	Mode string

	// Tuning below. None of these has a command line flag, deliberately.
	//
	// They were constants scattered through the engine and the clients, and
	// consolidating them here is what lets them be changed without rebuilding.
	// Keeping them off the command line is what stops --help from growing to
	// thirty entries and burying the dozen settings anyone actually sets.

	// ReconnectInitialBackoff and ReconnectMaxBackoff bound the wait between
	// redial attempts on a dropped connection.
	ReconnectInitialBackoff time.Duration
	ReconnectMaxBackoff     time.Duration

	// RESTAttempts is how many times one REST request is tried.
	RESTAttempts int
	// RESTTimeout bounds a single REST attempt.
	RESTTimeout time.Duration
	// RESTInitialBackoff and RESTMaxBackoff bound the wait between attempts.
	RESTInitialBackoff time.Duration
	RESTMaxBackoff     time.Duration
	// RESTMaxRetryAfter caps how long a server-supplied Retry-After is obeyed.
	RESTMaxRetryAfter time.Duration
	// ResyncWorkers is how many re-seed workers run concurrently.
	ResyncWorkers int

	// ReadLimit bounds a single websocket frame, in bytes.
	ReadLimit int64

	// MaxErrors and MaxEvents bound the output document's lists.
	MaxErrors int
	MaxEvents int

	// MaxErrorLength bounds one message in the output document's error list.
	MaxErrorLength int
	// ConsoleValueLimit is how much of one attribute value reaches the terminal.
	ConsoleValueLimit int

	// LogCategories switches classes of log record on and off. Errors are
	// never affected.
	LogCategories LogCategories
}

// New returns a Config with every default applied and nothing else set.
func New() Config {
	return Config{
		Duration:               DefaultDuration,
		Grace:                  DefaultGrace,
		RESTRate:               DefaultRESTRate,
		RESTBatchSize:          DefaultRESTBatchSize,
		MaxAssetsPerConnection: DefaultMaxAssetsPerConnection,
		PingInterval:           DefaultPingInterval,
		IdleTimeout:            DefaultIdleTimeout,
		ReorderTolerance:       DefaultReorderTolerance,
		DiscoverLimit:          DefaultDiscoverLimit,
		WSURL:                  DefaultWSURL,
		RESTURL:                DefaultRESTURL,
		LogLevel:               defaultLogLevel,
		Mode:                   ModeProduction,

		ReconnectInitialBackoff: DefaultReconnectInitialBackoff,
		ReconnectMaxBackoff:     DefaultReconnectMaxBackoff,

		RESTAttempts:       DefaultRESTAttempts,
		RESTTimeout:        DefaultRESTTimeout,
		RESTInitialBackoff: DefaultRESTInitialBackoff,
		RESTMaxBackoff:     DefaultRESTMaxBackoff,
		RESTMaxRetryAfter:  DefaultRESTMaxRetryAfter,
		ResyncWorkers:      DefaultResyncWorkers,

		ReadLimit: DefaultReadLimit,

		MaxErrors: DefaultMaxErrors,
		MaxEvents: DefaultMaxEvents,

		MaxErrorLength:    DefaultMaxErrorLength,
		ConsoleValueLimit: DefaultConsoleValueLimit,
		LogCategories:     AllLogCategories(),
	}
}

// LogValue renders the whole configuration for a log record.
//
// It exists so that a run's settings are recorded once, in full, in the same
// stream as everything else. Reading a log without them means guessing whether
// a number is a symptom or the configured behaviour working exactly as asked:
// eight reconnects is alarming at a 30s idle timeout and unremarkable at one of
// 200ms, and nothing else in the log says which was in force.
//
// Every field is included, and none is redacted, because there is nothing here
// to redact. The tool is credential-free by design (requirement A6): no API
// keys, no wallet material, no order endpoints, ever. If that ever stops being
// true, this method is the one place that has to change.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("tokens_path", c.TokensPath),
		slog.String("out_path", c.OutPath),
		slog.Duration("duration", c.Duration),
		slog.Duration("grace", c.Grace),
		slog.Bool("rest_only", c.RESTOnly),
		slog.Float64("rest_rate", c.RESTRate),
		slog.Int("rest_batch_size", c.RESTBatchSize),
		slog.Int("max_assets_per_connection", c.MaxAssetsPerConnection),
		slog.Duration("ping_interval", c.PingInterval),
		slog.Duration("idle_timeout", c.IdleTimeout),
		slog.Duration("reorder_tolerance", c.ReorderTolerance),
		slog.Bool("strict_best_bid_ask", c.StrictBestBidAsk),
		slog.Int("discover_limit", c.DiscoverLimit),
		slog.String("ws_url", c.WSURL),
		slog.String("rest_url", c.RESTURL),
		slog.String("log_level", c.LogLevel),
		slog.String("log_file", c.LogFile),

		slog.String("mode", c.Mode),
		slog.String("config_file", c.ConfigPath),

		slog.Duration("reconnect_initial_backoff", c.ReconnectInitialBackoff),
		slog.Duration("reconnect_max_backoff", c.ReconnectMaxBackoff),
		slog.Int("rest_attempts", c.RESTAttempts),
		slog.Duration("rest_timeout", c.RESTTimeout),
		slog.Duration("rest_initial_backoff", c.RESTInitialBackoff),
		slog.Duration("rest_max_backoff", c.RESTMaxBackoff),
		slog.Duration("rest_max_retry_after", c.RESTMaxRetryAfter),
		slog.Int("resync_workers", c.ResyncWorkers),
		slog.Int64("read_limit", c.ReadLimit),
		slog.Int("max_errors", c.MaxErrors),
		slog.Int("max_events", c.MaxEvents),
		slog.Int("max_error_length", c.MaxErrorLength),
		slog.Int("console_value_limit", c.ConsoleValueLimit),
		slog.Any("log_categories_off", c.LogCategories.Disabled()),
	)
}

// Validate reports the first problem with the configuration.
//
// Every message names the flag and says what would go wrong, because these
// strings are the only feedback an operator gets from a headless run.
func (c Config) Validate() error {
	if c.TokensPath == "" {
		return fmt.Errorf("--tokens is required: give it a file of token ids, one per line or a JSON array")
	}
	if c.OutPath == "" {
		return fmt.Errorf("--out is required: give it the path to write the output document to")
	}

	if c.Duration <= 0 {
		return fmt.Errorf("--duration must be positive, got %v", c.Duration)
	}
	if c.Duration > maxDuration {
		return fmt.Errorf("--duration must be at most %v, got %v: a longer run is almost certainly a typo", maxDuration, c.Duration)
	}
	if c.Grace <= 0 {
		return fmt.Errorf("--grace must be positive, got %v: shutdown needs time to write the output", c.Grace)
	}

	if c.RESTRate <= 0 {
		return fmt.Errorf("--rest-rate must be positive, got %v", c.RESTRate)
	}
	if c.RESTBatchSize < 1 || c.RESTBatchSize > maxRESTBatchSize {
		return fmt.Errorf("--rest-batch-size must be between 1 and %d, got %d: the endpoint rejects larger payloads", maxRESTBatchSize, c.RESTBatchSize)
	}

	if c.MaxAssetsPerConnection < 1 || c.MaxAssetsPerConnection > MaxAssetsCeiling {
		return fmt.Errorf("--max-assets-per-connection must be between 1 and %d, got %d: past that the server stops sending the initial snapshot without reporting an error", MaxAssetsCeiling, c.MaxAssetsPerConnection)
	}
	if c.PingInterval <= 0 {
		return fmt.Errorf("--ping-interval must be positive, got %v", c.PingInterval)
	}
	if c.IdleTimeout <= c.PingInterval {
		return fmt.Errorf("--idle-timeout (%v) must be longer than --ping-interval (%v), or every connection is declared dead before it can answer", c.IdleTimeout, c.PingInterval)
	}
	if c.ReorderTolerance < 0 {
		return fmt.Errorf("--reorder-tolerance must not be negative, got %v", c.ReorderTolerance)
	}
	if c.DiscoverLimit < 0 {
		return fmt.Errorf("--discover-limit must not be negative, got %d: use 0 to disable discovery", c.DiscoverLimit)
	}

	if err := validateURL("--ws-url", c.WSURL, "ws", "wss"); err != nil {
		return err
	}
	if err := validateURL("--rest-url", c.RESTURL, "http", "https"); err != nil {
		return err
	}
	if err := validateMode(c.Mode); err != nil {
		return err
	}

	return c.validateTuning()
}

// validateTuning checks the settings that have no flag of their own.
//
// They are separated because their messages have to read differently: nobody
// mistypes these on a command line, so the message names the file's own
// spelling rather than a flag, and says what the value would break.
func (c Config) validateTuning() error {
	positive := []struct {
		setting string
		value   int64
	}{
		{"rest.attempts", int64(c.RESTAttempts)},
		{"rest.resync_workers", int64(c.ResyncWorkers)},
		{"websocket.read_limit", c.ReadLimit},
		{"limits.max_errors", int64(c.MaxErrors)},
	}
	for _, check := range positive {
		if check.value < 1 {
			return fmt.Errorf("%s must be at least 1, got %d", check.setting, check.value)
		}
	}

	nonNegative := []struct {
		setting string
		value   int
	}{
		{"limits.max_events", c.MaxEvents},
		{"limits.max_error_length", c.MaxErrorLength},
		{"logging.console_value_limit", c.ConsoleValueLimit},
	}
	for _, check := range nonNegative {
		if check.value < 0 {
			return fmt.Errorf("%s must not be negative, got %d: use 0 for no limit", check.setting, check.value)
		}
	}

	if c.RESTTimeout <= 0 {
		return fmt.Errorf("rest.timeout must be positive, got %v: a request with no bound can stall the whole run", c.RESTTimeout)
	}
	if c.RESTMaxRetryAfter < 0 {
		return fmt.Errorf("rest.max_retry_after must not be negative, got %v", c.RESTMaxRetryAfter)
	}

	if err := validateBackoff(
		"rest.initial_backoff", c.RESTInitialBackoff,
		"rest.max_backoff", c.RESTMaxBackoff,
	); err != nil {
		return err
	}

	return validateBackoff(
		"websocket.reconnect_initial_backoff", c.ReconnectInitialBackoff,
		"websocket.reconnect_max_backoff", c.ReconnectMaxBackoff,
	)
}

// validateBackoff checks a backoff pair, which is only meaningful when the
// ceiling is at or above the floor. The settings are named in full rather than
// built from a prefix, so the message quotes exactly what the file has to say.
func validateBackoff(initialName string, initial time.Duration, maxName string, maximum time.Duration) error {
	if initial <= 0 {
		return fmt.Errorf("%s must be positive, got %v", initialName, initial)
	}
	if maximum < initial {
		return fmt.Errorf("%s (%v) must be at least %s (%v), or the ceiling would shorten the first wait",
			maxName, maximum, initialName, initial)
	}

	return nil
}

// validateURL checks that raw is an absolute URL with one of the allowed
// schemes, so a typo produces a configuration error rather than a dial failure
// halfway through a run.
func validateURL(flagName, raw string, schemes ...string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s is not a valid URL: %w", flagName, err)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute URL, got %q", flagName, raw)
	}

	for _, scheme := range schemes {
		if parsed.Scheme == scheme {
			return nil
		}
	}

	return fmt.Errorf("%s must use one of the schemes %v, got %q", flagName, schemes, parsed.Scheme)
}
