// Package engine runs a collection window and produces the output document.
//
// It is the only package that owns goroutines, timers or deadlines, which is
// deliberate: everything it coordinates is pure and separately tested, so a
// failure here is a failure of orchestration rather than of interpretation.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/netqo/polymarket-scraper/internal/config"
	"github.com/netqo/polymarket-scraper/internal/report"
	"github.com/netqo/polymarket-scraper/internal/restclient"
	"github.com/netqo/polymarket-scraper/internal/tokenlist"
	"github.com/netqo/polymarket-scraper/internal/tracker"
)

// ErrNotImplemented reports that the requested collection mode does not exist
// in this build yet.
var ErrNotImplemented = errors.New("this build can only collect over REST; pass --rest-only")

// Options configure an Engine.
type Options struct {
	Config config.Config
	Tokens tokenlist.List
	Logger *slog.Logger

	// Now supplies the clock. Tests replace it; production leaves it nil.
	Now func() time.Time

	// HTTPClient replaces the REST transport. Tests point it at an in-process
	// server; production leaves it nil.
	HTTPClient *http.Client
}

// Engine collects a window's worth of order books.
type Engine struct {
	cfg    config.Config
	tokens tokenlist.List
	logger *slog.Logger
	now    func() time.Time

	rest   *restclient.Client
	errors *errorSink

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

	return &Engine{
		cfg:    opts.Config,
		tokens: opts.Tokens,
		logger: logger,
		now:    now,
		rest:   rest,
		errors: &errorSink{},
	}, nil
}

// Run collects the window and returns the document.
//
// It returns an error only when no usable document could be produced at all.
// Individual tokens failing is not a run failure: that is what each token's
// status is for, and a document full of honest failures is far more useful to
// a consumer than no document.
func (e *Engine) Run(ctx context.Context) (report.Document, error) {
	if !e.cfg.RESTOnly {
		return report.Document{}, ErrNotImplemented
	}

	return e.runRESTOnly(ctx)
}

// newTrackers builds one tracker per requested token, in request order.
func (e *Engine) newTrackers() map[string]*tracker.Tracker {
	opts := tracker.Options{
		ReorderTolerance: e.cfg.ReorderTolerance,
		StrictBestBidAsk: e.cfg.StrictBestBidAsk,
		RESTOnly:         e.cfg.RESTOnly,
	}

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

// finalize freezes every tracker and assembles the document.
func (e *Engine) finalize(startedAt, finishedAt time.Time, trackers map[string]*tracker.Tracker) report.Document {
	snapshots := make(map[string]tracker.Snapshot, len(trackers))
	for id, t := range trackers {
		snapshots[id] = t.Finalize(finishedAt)
	}

	return report.Build(report.Input{
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		Window:     e.cfg.CollectWindow(len(e.tokens.IDs)),
		Requested:  e.tokens.IDs,
		Snapshots:  snapshots,
		Connection: report.Connection{
			RESTRequests: e.rest.Requests(),
			RESTResyncs:  e.restSeeded,
		},
		Errors: e.errors.Messages(),
	})
}
