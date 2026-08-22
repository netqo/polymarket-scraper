package config

import (
	"fmt"
	"strconv"
	"time"
)

// programName is the binary's name as it appears in the help text.
const programName = "polymarket-scraper"

// usageTemplate is the help text.
//
// It is written by hand rather than generated because it is the contract a
// consuming agent reads before its first run (requirement F1): it has to say
// what the tool guarantees, not just what each flag is called. A test walks the
// bound flags and this text in both directions, so a flag can neither be added
// without documenting it nor documented without existing.
const usageTemplate = `%[1]s collects live Polymarket order books over a fixed
window and writes one JSON document describing every token it was asked about.

It is read-only and credential-free: market data only, no API keys, no wallet
material, no order endpoints. It applies no filtering and computes no edges,
fees or scores. Judgement belongs to whatever reads the output.

USAGE
  %[1]s --tokens FILE --out FILE [--duration SECONDS] [options]

SETTINGS FILE
  --config FILE     Read settings from a TOML file. Without this, %[16]s
                    in the working directory is used if it exists. A file named
                    explicitly, here or in %[17]s, must exist.
  --mode MODE       %[18]s or %[19]s. A bundle of defaults rather than a
                    setting of its own: %[19]s drops the log level to debug and
                    stops truncating values on the terminal. Also read from
                    %[20]s. Default %[18]s.

  Sources override each other in this order, each beating the one before it:
  defaults, the mode's bundle, the settings file, the environment, then flags.

  Tuning that has no flag lives in the file alone: retry counts and timeouts,
  backoffs, worker counts, buffer and list limits. Run --help is not the place
  to discover those; the commented example file is.

REQUIRED
  --tokens FILE     Token ids to collect: one per line, or a JSON array of
                    strings. Blank lines and lines starting with '#' are
                    ignored. Ids are always treated as strings, never numbers.
  --out FILE        Where to write the output document. The write is atomic:
                    the path either holds the previous run's document or this
                    one's, never a partial file.

WINDOW
  --duration N      Length of the collection window. A bare number is seconds;
                    a value such as 1m30s also works. Default %[2]s.
  --grace N         Extra time allowed for shutdown after the window closes,
                    after which the process terminates unconditionally.
                    Default %[3]s.

MODE
  --rest-only       Skip the websocket and snapshot every token over REST.
                    A debugging aid, and a mirror of the usual fallback path.
                    In this mode the window is extended if the requests
                    themselves cannot finish within it.

REST PACING
  --rest-rate N     Ceiling on REST requests per second. Default %[4]g.
  --rest-batch-size N
                    Token ids per batched book request. Default %[5]d; the
                    endpoint rejects payloads much beyond %[6]d.

WEBSOCKET
  --max-assets-per-connection N
                    Sharding width: how the requested tokens are spread across
                    connections. Default %[7]d, maximum %[15]d. Past roughly 750
                    assets the server stops sending the initial snapshot without
                    reporting an error, so the maximum stays below that.
  --ping-interval N Keepalive cadence. Default %[8]s.
  --idle-timeout N  Silence tolerated before a connection is treated as dead
                    and reconnected. Must exceed --ping-interval.
                    Default %[9]s.
  --reorder-tolerance N
                    How far a timestamp may go backwards before its token is
                    distrusted rather than merely flagged. Default %[10]s.
  --strict-best-bid-ask
                    Treat a published best quote that disagrees with the
                    maintained book as a gap, not just an anomaly.
  --discover-limit N
                    Cap on tokens subscribed to mid-window after being
                    announced. 0 disables it. Announcements are reported
                    either way. Default %[11]d.

ENDPOINTS
  --ws-url URL      Default %[12]s
  --rest-url URL    Default %[13]s

OTHER
  --log-level LEVEL debug, info, warn or error. Also read from LOG_LEVEL.
                    Default %[14]s.
  --log-file PATH   Also append every log line to this file as the run happens,
                    uncoloured and never truncated, so it can be tailed while
                    the run is still going. Created if absent, mode 0600.
                    Off by default.
  --version         Print the version and exit.
  --help            Print this text and exit.

OUTPUT
  Every requested token appears in the output exactly once with an explicit
  status: ok, no_data, subscribe_failed or resync_failed. An empty book on a
  live token is ok with empty sides, which is not the same thing as a failure.
  A book is never reported as ok unless it is current: after a disconnect or a
  suspected gap the token is re-seeded, and if that fails it is reported as
  resync_failed rather than having its stale book passed off as fresh.

  Prices and sizes are passed through as the API's own decimal strings, so no
  value is ever re-rounded. See SCHEMA.md for the full contract.

EXIT STATUS
  0   The output document was written and is valid. Individual tokens may
      still have failed; their status says so.
  2   The command line was wrong. Nothing was written.
  1   The run failed and the output is unusable.
  3   The watchdog terminated a run that would not shut down.

LOGGING
  Logs go to stderr. On success, and only on success, one summary line is
  written to stdout, so non-empty stdout is a reliable success signal.

  Each line carries a timestamp and a prefix: [!] error or warning, [*] info,
  [~] step or detail, [+] success, [?] something needing a decision. Colour is
  used only when stderr is a terminal. A message that repeats is written once
  and then summarised as "message (xN)" once the repeats stop, so a connection
  failing in a loop cannot bury everything else.
`

// Usage returns the help text.
func Usage() string {
	return fmt.Sprintf(usageTemplate,
		programName,
		secondsText(DefaultDuration),
		secondsText(DefaultGrace),
		DefaultRESTRate,
		DefaultRESTBatchSize,
		maxRESTBatchSize,
		DefaultMaxAssetsPerConnection,
		secondsText(DefaultPingInterval),
		secondsText(DefaultIdleTimeout),
		secondsText(DefaultReorderTolerance),
		DefaultDiscoverLimit,
		DefaultWSURL,
		DefaultRESTURL,
		defaultLogLevel,
		MaxAssetsCeiling,
		DefaultConfigName,
		EnvConfig,
		ModeProduction,
		ModeDebug,
		EnvMode,
	)
}

// secondsText renders a duration in seconds, the unit the specification and the
// flags themselves use. Duration.String would print the default window as
// "1m30s", which is accepted but reads as a different unit from the "--duration
// 90" the documentation is describing.
func secondsText(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'g', -1, 64) + "s"
}
