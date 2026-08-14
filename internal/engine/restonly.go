package engine

import (
	"context"
	"errors"
	"time"

	"github.com/netqo/polymarket-scraper/internal/report"
	"github.com/netqo/polymarket-scraper/internal/restclient"
	"github.com/netqo/polymarket-scraper/internal/tracker"
)

// runRESTOnly collects every token's book over REST and nothing else.
//
// It exists for two reasons. It is the debugging path, giving a way to see what
// the exchange says right now without any of the websocket machinery in the
// way. And it mirrors the fallback a consumer would otherwise have to implement
// itself, so a run that cannot use the websocket still produces a document in
// exactly the same shape.
//
// Books are fetched in batches first, because several hundred tokens fit in a
// handful of requests that way, and only the tokens the batches did not cover
// are then fetched one at a time. That second pass is what turns "absent from
// the batch response" into a specific per-token answer: a token id the exchange
// does not recognise is a different thing from one it could not serve.
func (e *Engine) runRESTOnly(ctx context.Context) (report.Document, error) {
	startedAt := e.now()
	budget := e.cfg.Budget(len(e.tokens.IDs))

	// The deadline is the same arithmetic the websocket path uses, so a run
	// that cannot finish says so by producing failures rather than by running
	// past the time it was given.
	ctx, cancel := context.WithDeadline(ctx, startedAt.Add(budget.Finalize))
	defer cancel()

	e.noteTokenListAnomalies()
	trackers := e.newTrackers()

	e.logger.Info("collecting over REST only",
		"tokens", len(e.tokens.IDs),
		"batch_size", e.cfg.RESTBatchSize,
		"rate", e.cfg.RESTRate)

	e.fetchInBatches(ctx, trackers)
	e.fetchStragglers(ctx, trackers)

	return e.finalizeDocument(startedAt, e.now(), finalizeAll(trackers, e.now())), nil
}

// fetchInBatches fetches books in as few requests as the endpoint allows.
func (e *Engine) fetchInBatches(ctx context.Context, trackers map[string]*tracker.Tracker) {
	for _, batch := range chunk(e.tokens.IDs, e.cfg.RESTBatchSize) {
		if ctx.Err() != nil {
			e.errors.Addf("stopped batch fetching early: %v", ctx.Err())
			return
		}

		books, err := e.rest.Books(ctx, batch)
		if err != nil {
			// A failed batch is not fatal to the tokens in it: the individual
			// pass below still gets a chance at each of them.
			e.errors.Addf("a batch of %d books failed: %v", len(batch), err)
			e.logger.Warn("batch fetch failed", "tokens", len(batch), "error", err)
			continue
		}

		now := e.now()
		for _, fetched := range books {
			t, requested := trackers[fetched.AssetID]
			if !requested {
				continue
			}
			t.ApplyRESTBook(fetched, now)
			e.restSeeded++
		}
	}
}

// fetchStragglers fetches the tokens the batches did not answer for, one at a
// time, so each failure can be attributed to the token it belongs to.
func (e *Engine) fetchStragglers(ctx context.Context, trackers map[string]*tracker.Tracker) {
	for _, id := range e.tokens.IDs {
		t := trackers[id]
		if t.State() != tracker.StatePending {
			continue
		}

		if ctx.Err() != nil {
			t.Sweep()
			t.NoteResyncFailed()
			continue
		}

		e.fetchStraggler(ctx, t, id)
	}
}

// fetchStraggler fetches a single token the batches did not answer for, and
// records what happened to it.
func (e *Engine) fetchStraggler(ctx context.Context, t *tracker.Tracker, id string) {
	fetched, err := e.rest.Book(ctx, id)
	switch {
	case err == nil:
		t.ApplyRESTBook(fetched, e.now())
		e.restSeeded++

	case errors.Is(err, restclient.ErrNotFound):
		// The exchange has never heard of this id. That is a fact about the
		// token rather than a failure of the run, and reporting it as one would
		// send a consumer looking for a problem that is not there.
		t.NoteTokenNotFound()
		e.errors.Addf("token %s is not recognised by the exchange", id)

	default:
		// We asked for a book and did not get one, so this token has no
		// trustworthy data and says so.
		t.Sweep()
		t.NoteResyncFailed()
		e.errors.Addf("could not fetch a book for token %s: %v", id, err)
	}
}

// finalizeAll freezes every tracker into the value the report writes out.
func finalizeAll(trackers map[string]*tracker.Tracker, at time.Time) map[string]tracker.Snapshot {
	snapshots := make(map[string]tracker.Snapshot, len(trackers))
	for id, t := range trackers {
		snapshots[id] = t.Finalize(at)
	}

	return snapshots
}

// chunk splits ids into batches of at most size.
func chunk(ids []string, size int) [][]string {
	if size <= 0 || len(ids) == 0 {
		return nil
	}

	batches := make([][]string, 0, (len(ids)+size-1)/size)
	for start := 0; start < len(ids); start += size {
		end := min(start+size, len(ids))
		batches = append(batches, ids[start:end])
	}

	return batches
}
