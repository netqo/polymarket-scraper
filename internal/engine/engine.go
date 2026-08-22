// Package engine runs a collection window and produces the output document.
//
// It is the only package that owns goroutines, timers or deadlines. Everything
// it coordinates is pure and separately tested, so a failure here is a failure
// of orchestration rather than of interpretation.
//
// One fact shapes the whole design: a Go goroutine blocked in a syscall will
// never observe a cancelled context. There is no way to kill it, which means
// the only hard guarantee that a run ends on time is a watchdog that terminates
// the process. Everything else is cooperative. The consequence is that the
// document has to be producible without joining any goroutine that performs
// I/O, and that single constraint is why book state lives behind a
// single-writer goroutine that does no I/O rather than behind a mutex that an
// I/O goroutine could be holding at the wrong moment.
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/netqo/polymarket-scraper/internal/config"
	"github.com/netqo/polymarket-scraper/internal/logging"
	"github.com/netqo/polymarket-scraper/internal/report"
	"github.com/netqo/polymarket-scraper/internal/restclient"
	"github.com/netqo/polymarket-scraper/internal/stream"
	"github.com/netqo/polymarket-scraper/internal/tokenlist"
	"github.com/netqo/polymarket-scraper/internal/tracker"
)

// Options configure an Engine.
type Options struct {
	Config config.Config
	Tokens tokenlist.List
	Logger *slog.Logger

	// Now supplies the clock. Tests replace it; production leaves it nil.
	Now func() time.Time

	// HTTPClient replaces the transport for both REST and the websocket. Tests
	// point it at in-process servers; production leaves it nil.
	HTTPClient *http.Client

	// Stream receives the run's changes as they happen. Nil disables it, which
	// is the default. Its lifetime belongs to the caller, for the same reason
	// the logger's does: both outlive the collection they describe.
	Stream *stream.Writer

	// Halt terminates the process when a run overruns its budget. Tests replace
	// it so the watchdog can be observed rather than obeyed.
	Halt func()
}

// Engine collects a window's worth of order books.
type Engine struct {
	cfg        config.Config
	tokens     tokenlist.List
	logger     *slog.Logger
	now        func() time.Time
	httpClient *http.Client
	halt       func()

	rest   *restclient.Client
	errors *errorSink
	events *eventLog

	// discoverMatch narrows which announcements are acted on. Nil takes on
	// everything. Compiled once, since it is consulted per announcement.
	discoverMatch *regexp.Regexp

	// observer fans a token's changes out to the log and the change stream.
	observer observer

	// changes is the stream, kept so announcements can reach it too. Nil when
	// none was configured.
	changes *stream.Writer

	shards shardSet
	resync chan resyncRequest

	// running is what a shard needs to be started into. It is set once, before
	// any shard goroutine exists, and only read afterwards. Discovery reads it
	// when it has to grow a connection.
	//
	// Contexts in a struct are usually a smell. The alternative here is
	// threading two of them plus a channel through every layer between the
	// apply loop and the announcement that needs them, to reach one call site.
	running *collection

	// requested is the set of tokens the run was asked for, which is what the
	// completeness guarantee is measured against.
	requested map[string]bool

	// discovered counts tokens taken on from announcements, across all shards.
	discovered atomic.Int64

	// discoveryClosed stops any further announced token being taken on, whether
	// because the budget is spent, every connection is full, or the sweep has
	// arrived. Engine-wide rather than per shard, because an announcement seen
	// by one connection can now be placed on another.
	discoveryClosed atomic.Bool

	reconnects  atomic.Int64
	restResyncs atomic.Int64

	// restSeeded counts books taken over REST in a rest-only run.
	restSeeded int

	// initialShards is how many connections the token list alone needed. Fixed
	// once buildShards has run, so it can be read without touching the set.
	initialShards int
}

// New builds an engine, failing before any work starts if the configuration
// cannot produce a working client.
func New(opts Options) (*Engine, error) {
	discoverMatch, err := opts.Config.DiscoverPattern()
	if err != nil {
		return nil, err
	}

	rest, err := restclient.New(restclient.Options{
		BaseURL:    opts.Config.RESTURL,
		Rate:       opts.Config.RESTRate,
		HTTPClient: opts.HTTPClient,
	})
	if err != nil {
		return nil, fmt.Errorf("building the REST client: %w", err)
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	halt := opts.Halt
	if halt == nil {
		halt = func() {}
	}

	engine := &Engine{
		cfg:           opts.Config,
		tokens:        opts.Tokens,
		logger:        logger,
		now:           now,
		httpClient:    opts.HTTPClient,
		halt:          halt,
		rest:          rest,
		discoverMatch: discoverMatch,
		changes:       opts.Stream,
		errors:        newErrorSink(opts.Config),
		events:        newEventLog(opts.Config.MaxEvents),
		// Sized so every tracker that can exist can have a re-seed outstanding
		// at once, which is exactly what a disconnect produces. Discovered
		// tokens are counted too: they use this queue as well, and a queue that
		// only fits the requested ones lets a token the run picked up on its own
		// push a token that was actually asked for onto the failure path.
		resync:    make(chan resyncRequest, len(opts.Tokens.IDs)+opts.Config.DiscoverLimit+opts.Config.ResyncWorkers),
		requested: requestedSet(opts.Tokens.IDs),
	}

	// Built after the engine exists, because it reports through it.
	engine.observer = observer{engine: engine, changes: opts.Stream}

	return engine, nil
}

// Run collects the window and returns the document.
//
// It returns an error only when no usable document could be produced at all.
// Individual tokens failing is not a run failure: that is what each token's
// status is for, and a document full of honest failures is far more useful to
// a consumer than no document.
func (e *Engine) Run(ctx context.Context) (report.Document, error) {
	if e.cfg.RESTOnly {
		return e.runRESTOnly(ctx)
	}

	return e.runWebsocket(ctx)
}

// buildShards splits the token list across connections.
//
// The width stays well below the point where the server accepts a subscription
// and then silently declines to send the initial snapshot, which is a failure
// that reports nothing and looks exactly like a healthy connection.
func (e *Engine) buildShards() {
	opts := e.trackerOptions()

	for _, assetIDs := range chunk(e.tokens.IDs, e.cfg.MaxAssetsPerConnection) {
		e.shards.add(newShardState(e.shards.count(), assetIDs, opts))
	}

	e.initialShards = e.shards.count()
}

func (e *Engine) trackerOptions() tracker.Options {
	return tracker.Options{
		ReorderTolerance: e.cfg.ReorderTolerance,
		StrictBestBidAsk: e.cfg.StrictBestBidAsk,
		RESTOnly:         e.cfg.RESTOnly,
		Observer:         e.observer,
	}
}

// seriousFlags are the observations worth interrupting someone for.
//
// The rest are recorded at info: they describe things the scraper noticed and
// handled correctly, and a run that reported all of them as warnings would
// train whoever reads it to ignore warnings.
var seriousFlags = map[tracker.Flag]bool{
	tracker.FlagDeltaGap:           true,
	tracker.FlagDisconnected:       true,
	tracker.FlagDecodeError:        true,
	tracker.FlagCrossedBook:        true,
	tracker.FlagTokenNotFound:      true,
	tracker.FlagBestBidAskMismatch: true,
	tracker.FlagUnparsablePrice:    true,
}

// logFlag reports a flag the moment a tracker raises it.
//
// Flags reach the output document either way, but the document only exists once
// the run is over. Until then a run quietly accumulating delta_gap on every
// token looks exactly like a healthy one, which is the opposite of what someone
// watching it needs.
//
// The token is named only at debug. One disconnect raises the same flag on
// every token its connection carried, so naming each one produces hundreds of
// records that cannot be collapsed and that bury everything else; leaving it
// off lets them coalesce into a single line and a count. The per-token detail
// is still available, one level down, for reconstructing a specific book's
// history.
func (e *Engine) logFlag(tokenID string, flag tracker.Flag) {
	level := slog.LevelInfo
	if seriousFlags[flag] {
		level = slog.LevelWarn
	}

	ctx := context.Background()
	category := logging.Cat(logging.CategoryFlags)

	if e.logger.Enabled(ctx, slog.LevelDebug) {
		e.logger.Log(ctx, level, "token flagged", category, "flag", string(flag), "token", tokenID)
		return
	}

	e.logger.Log(ctx, level, "token flagged", category, "flag", string(flag))
}

// newTrackers builds one tracker per requested token, for the rest-only path
// which has no shards.
func (e *Engine) newTrackers() map[string]*tracker.Tracker {
	opts := e.trackerOptions()

	trackers := make(map[string]*tracker.Tracker, len(e.tokens.IDs))
	for _, id := range e.tokens.IDs {
		trackers[id] = tracker.New(id, opts)
	}

	return trackers
}

// noteTokenListAnomalies records what was odd about the input, so it reaches
// the document rather than only the log.
func (e *Engine) noteTokenListAnomalies() {
	if e.tokens.Duplicates > 0 {
		e.errors.Addf("the token list contained %d duplicate ids, which were collapsed", e.tokens.Duplicates)
	}
	if count := len(e.tokens.Suspicious); count > 0 {
		e.errors.Addf("%d ids do not look like token ids, starting with %q", count, e.tokens.Suspicious[0])
	}
}

// finalizeDocument assembles the output from the collected snapshots.
func (e *Engine) finalizeDocument(startedAt, finishedAt time.Time, snapshots map[string]tracker.Snapshot) report.Document {
	// A truncated list that does not say it was truncated reads as a complete
	// one, which is the sort of quiet inaccuracy this document exists to avoid.
	if dropped := e.events.suppressedCount(); dropped > 0 {
		e.errors.Addf("%d announcements were not reported: the run hit the cap of %d per list",
			dropped, e.cfg.MaxEvents)
	}

	requested, discovered := e.splitDiscovered(snapshots)

	return report.Build(report.Input{
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		Window:     e.cfg.CollectWindow(len(e.tokens.IDs)),
		Requested:  e.tokens.IDs,
		Snapshots:  requested,
		Discovered: discovered,
		Connection: report.Connection{
			WSConnections: e.shards.count(),
			Reconnects:    int(e.reconnects.Load()),
			RESTRequests:  e.rest.Requests(),
			RESTResyncs:   int(e.restResyncs.Load()) + e.restSeeded,
		},
		Events: e.events.events(),
		Errors: e.errors.Messages(),
	})
}

// collection is the running state a shard is started into.
type collection struct {
	collectCtx context.Context
	drainCtx   context.Context
	results    chan<- shardResult
}

// maxShards bounds how many connections a run can end up with.
//
// The token list decides the starting count; discovery can add enough to hold
// everything its budget allows. The bound matters because the results channel
// is sized from it, and a send onto a full one would block an apply goroutine,
// which is the one thing that must never happen.
//
// It reads initialShards rather than counting the set, because it is called
// while the set's own lock is held and a RWMutex is not reentrant.
func (e *Engine) maxShards() int {
	if e.cfg.DiscoverLimit <= 0 || e.cfg.MaxAssetsPerConnection <= 0 {
		return e.initialShards
	}

	perConnection := e.cfg.MaxAssetsPerConnection
	extra := (e.cfg.DiscoverLimit + perConnection - 1) / perConnection

	return e.initialShards + extra
}

// requestedSet indexes the token list for membership tests.
func requestedSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}

	return set
}

// startWatchdog arms the only hard guarantee that the process ends on time.
//
// Everything else in the shutdown path is cooperative, and cooperation is not
// something a goroutine stuck in a syscall can offer. If this fires, no output
// document has been written, which is the correct outcome: a run that overran
// its budget has not produced trustworthy data and must not look as though it
// has.
func (e *Engine) startWatchdog(budget config.Budget) func() {
	timer := time.AfterFunc(budget.HardStop, func() {
		e.logger.Error("the run did not shut down within its budget; terminating",
			"budget", budget.HardStop)
		e.halt()
	})

	return func() { timer.Stop() }
}
