package engine

import (
	"github.com/netqo/polymarket-scraper/internal/stream"
	"github.com/netqo/polymarket-scraper/internal/tracker"
)

// observer reports a token's changes to everything in this run that wants them.
//
// The log and the change stream want different subsets: a person reading the
// log wants to know a token lost trust, and does not want a line every time a
// price moves, which at several hundred tokens is most of the traffic. The
// stream wants both, because the thing reading it is deciding what to do.
//
// Runs on a shard's apply goroutine, so neither destination may block. The
// stream's writes are appends under a mutex and the logger's are the same, so
// the only thing either can wait on is the other's write to the same file.
type observer struct {
	engine *Engine

	// changes is nil when no stream was configured, which is the default. Every
	// method checks, rather than substituting a no-op writer, so that a run
	// without a stream does no work per quote at all.
	changes *stream.Writer
}

// Flagged implements tracker.Observer.
func (o observer) Flagged(tokenID string, flag tracker.Flag) {
	o.engine.logFlag(tokenID, flag)

	if o.changes != nil {
		o.changes.Flagged(tokenID, flag)
	}
}

// Quoted implements tracker.Observer.
//
// Deliberately not logged. A moving price is the normal state of a live market,
// and reporting each one would drown every line worth reading.
func (o observer) Quoted(tokenID string, quote tracker.Quote) {
	if o.changes != nil {
		o.changes.Quoted(tokenID, quote)
	}
}

// Traded implements tracker.Observer.
func (o observer) Traded(tokenID string, trade tracker.LastTrade) {
	if o.changes != nil {
		o.changes.Traded(tokenID, trade)
	}
}
