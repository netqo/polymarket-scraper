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

// Websocket defaults.
const (
	// DefaultMaxAssetsPerConnection stays well below the point where the server
	// silently stops sending the initial snapshot. That failure mode is the
	// dangerous one: the socket stays open and keeps delivering deltas, so a
	// client that does not check looks healthy while holding no book state.
	DefaultMaxAssetsPerConnection = 400

	// MaxAssetsCeiling is how wide a connection may get in total, which is a
	// different number from the width above. That one decides how the requested
	// tokens are spread across connections; this one is the point past which
	// the server stops honouring the subscription, so it is what bounds a
	// connection that later takes on announced tokens as well.
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
	}
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

	return validateURL("--rest-url", c.RESTURL, "http", "https")
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
