// Test data: Invented data, except where a limit encodes something observed: the shard width
// ceiling exists because the server silently stops sending snapshots past roughly
// 750 assets. See PROTOCOL.md.

package config

import (
	"strings"
	"testing"
	"time"
)

// valid returns a configuration that passes validation, for tests that break
// exactly one thing about it.
func valid() Config {
	cfg := New()
	cfg.TokensPath = "tokens.txt"
	cfg.OutPath = "books.json"

	return cfg
}

func TestValidateAcceptsTheDefaults(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("the default configuration is invalid: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		mustSay string
	}{
		{"missing tokens path", func(c *Config) { c.TokensPath = "" }, "--tokens"},
		{"missing output path", func(c *Config) { c.OutPath = "" }, "--out"},
		{"zero duration", func(c *Config) { c.Duration = 0 }, "--duration"},
		{"negative duration", func(c *Config) { c.Duration = -time.Second }, "--duration"},
		{"absurd duration", func(c *Config) { c.Duration = 2 * time.Hour }, "--duration"},
		{"zero grace", func(c *Config) { c.Grace = 0 }, "--grace"},
		{"zero rest rate", func(c *Config) { c.RESTRate = 0 }, "--rest-rate"},
		{"negative rest rate", func(c *Config) { c.RESTRate = -1 }, "--rest-rate"},
		{"zero batch size", func(c *Config) { c.RESTBatchSize = 0 }, "--rest-batch-size"},
		{"oversized batch", func(c *Config) { c.RESTBatchSize = 5000 }, "--rest-batch-size"},
		{"zero shard width", func(c *Config) { c.MaxAssetsPerConnection = 0 }, "--max-assets-per-connection"},
		{"zero ping interval", func(c *Config) { c.PingInterval = 0 }, "--ping-interval"},
		{"negative reorder tolerance", func(c *Config) { c.ReorderTolerance = -time.Second }, "--reorder-tolerance"},
		{"negative discover limit", func(c *Config) { c.DiscoverLimit = -1 }, "--discover-limit"},
		{"relative websocket url", func(c *Config) { c.WSURL = "/ws/market" }, "--ws-url"},
		{"wrong websocket scheme", func(c *Config) { c.WSURL = "https://example.test/ws" }, "--ws-url"},
		{"relative rest url", func(c *Config) { c.RESTURL = "clob.polymarket.com" }, "--rest-url"},
		{"wrong rest scheme", func(c *Config) { c.RESTURL = "wss://example.test" }, "--rest-url"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid()
			tt.mutate(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate accepted an invalid configuration")
			}
			if !strings.Contains(err.Error(), tt.mustSay) {
				t.Errorf("error %q does not name %s", err, tt.mustSay)
			}
		})
	}
}

// A shard wider than the server's silent subscribe ceiling is the one
// misconfiguration that fails invisibly: the socket stays open, deltas keep
// arriving, and the initial snapshot simply never comes.
func TestValidateRejectsShardWidthPastTheSilentCeiling(t *testing.T) {
	cfg := valid()
	cfg.MaxAssetsPerConnection = 800

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate accepted a shard width past the ceiling")
	}
	if !strings.Contains(err.Error(), "snapshot") {
		t.Errorf("error %q does not explain the failure mode", err)
	}
}

// An idle timeout at or below the ping interval declares every connection dead
// before it has had a chance to answer, producing an endless reconnect loop
// that looks like a network problem.
func TestValidateRejectsAnIdleTimeoutThatCannotBeMet(t *testing.T) {
	tests := []struct {
		name         string
		ping, idle   time.Duration
		wantRejected bool
	}{
		{"idle below ping", 10 * time.Second, 5 * time.Second, true},
		{"idle equal to ping", 10 * time.Second, 10 * time.Second, true},
		{"idle above ping", 10 * time.Second, 11 * time.Second, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid()
			cfg.PingInterval, cfg.IdleTimeout = tt.ping, tt.idle

			err := cfg.Validate()
			if tt.wantRejected && err == nil {
				t.Fatal("Validate accepted an unmeetable idle timeout")
			}
			if !tt.wantRejected && err != nil {
				t.Fatalf("Validate rejected a workable configuration: %v", err)
			}
		})
	}
}

// Zero is a meaningful value for these, not a missing one.
func TestValidateAcceptsMeaningfulZeros(t *testing.T) {
	cfg := valid()
	cfg.ReorderTolerance = 0
	cfg.DiscoverLimit = 0

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate rejected meaningful zeros: %v", err)
	}
}
