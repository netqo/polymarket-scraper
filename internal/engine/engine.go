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
	"sync/atomic"
	"time"

	"github.com/netqo/polymarket-scraper/internal/config"
	"github.com/netqo/polymarket-scraper/internal/report"
	"github.com/netqo/polymarket-scraper/internal/restclient"
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

	shards []*shardState
	resync chan resyncRequest

	// requested is the set of tokens the run was asked for, which is what the
	// completeness guarantee is measured against.
	requested map[string]bool

	// discovered counts tokens taken on from announcements, across all shards.
	discovered atomic.Int64

	reconnects  atomic.Int64
	restResyncs atomic.Int64

	// outstanding counts REST work in flight, so the drain after the window
	// closes lasts only as long as there is something to wait for.
	outstanding atomic.Int64

	// restSeeded counts books taken over REST in a rest-only run.
	restSeeded int
}

// New builds an engine, failing before any work starts if the configuration
// cannot produce a working client.
func New(opts Options) (*Engine, error) {
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

	return &Engine{
		cfg:        opts.Config,
		tokens:     opts.Tokens,
		logger:     logger,
		now:        now,
		httpClient: opts.HTTPClient,
		halt:       halt,
		rest:       rest,
		errors:     &errorSink{},
		events:     newEventLog(),
		// Sized so every token can have a re-seed outstanding at once, which
		// is exactly what a disconnect produces.
		resync:    make(chan resyncRequest, len(opts.Tokens.IDs)+resyncWorkers),
		requested: requestedSet(opts.Tokens.IDs),
	}, nil
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
		e.shards = append(e.shards, newShardState(len(e.shards), assetIDs, opts))
	}
}

func (e *Engine) trackerOptions() tracker.Options {
	return tracker.Options{
		ReorderTolerance: e.cfg.ReorderTolerance,
		StrictBestBidAsk: e.cfg.StrictBestBidAsk,
		RESTOnly:         e.cfg.RESTOnly,
	}
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
			dropped, maxEvents)
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
			WSConnections: len(e.shards),
			Reconnects:    int(e.reconnects.Load()),
			RESTRequests:  e.rest.Requests(),
			RESTResyncs:   int(e.restResyncs.Load()) + e.restSeeded,
		},
		Events: e.events.events(),
		Errors: e.errors.Messages(),
	})
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
