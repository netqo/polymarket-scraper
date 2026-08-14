package engine

import (
	"context"
	"errors"

	"github.com/netqo/polymarket-scraper/internal/restclient"
	"github.com/netqo/polymarket-scraper/internal/wire"
)

// resyncWorkers is how many re-seed workers run.
//
// They share one rate limiter, so this is not a throughput setting: it is how
// many requests can be in flight while others wait on the limiter, which keeps
// a slow response from idling the whole budget.
const resyncWorkers = 4

// resyncRequest asks for a token's book to be fetched.
type resyncRequest struct {
	shardID int
	tokenID string
}

// resyncWorker fetches books for tokens that have lost trust.
//
// Requests are batched opportunistically, and that turns out to matter more
// than anything else in this file. A disconnect distrusts every token on the
// connection at once, so the queue fills with hundreds of requests in an
// instant. Fetched one at a time at ten requests a second, four hundred tokens
// would take forty seconds to recover, which is most of a window. Batched, it
// is two requests.
func (e *Engine) resyncWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return

		case first := <-e.resync:
			batch := e.drainQueue(first)
			e.fetch(ctx, batch)
			e.outstanding.Add(-int64(len(batch)))
		}
	}
}

// drainQueue takes everything already waiting, up to one request's worth.
func (e *Engine) drainQueue(first resyncRequest) []resyncRequest {
	batch := make([]resyncRequest, 1, e.cfg.RESTBatchSize)
	batch[0] = first

	for len(batch) < e.cfg.RESTBatchSize {
		select {
		case next := <-e.resync:
			batch = append(batch, next)
		default:
			return batch
		}
	}

	return batch
}

// fetch retrieves a batch of books and routes each answer to the shard that
// owns the token.
func (e *Engine) fetch(ctx context.Context, batch []resyncRequest) {
	if len(batch) == 1 {
		e.fetchOne(ctx, batch[0])
		return
	}

	tokenIDs := make([]string, len(batch))
	for i, request := range batch {
		tokenIDs[i] = request.tokenID
	}

	books, err := e.rest.Books(ctx, tokenIDs)
	if err != nil {
		e.errors.Addf("a re-seed of %d tokens failed: %v", len(batch), err)
		for _, request := range batch {
			e.route(ctx, request.shardID, control{kind: ctrlRESTFailed, tokenID: request.tokenID, at: e.now()})
		}
		return
	}

	e.routeBooks(ctx, batch, books)
}

// routeBooks delivers each fetched book and accounts for anything the response
// left out.
//
// A token missing from a batch response is one the exchange declined to serve.
// It is reported as a failed re-seed rather than being quietly left with its
// pre-gap book, which is the outcome that would actually hurt.
func (e *Engine) routeBooks(ctx context.Context, batch []resyncRequest, books []wire.RESTBook) {
	byToken := make(map[string]wire.RESTBook, len(books))
	for _, fetched := range books {
		byToken[fetched.AssetID] = fetched
	}

	for _, request := range batch {
		fetched, served := byToken[request.tokenID]
		if !served {
			e.errors.Addf("token %s was left out of a re-seed response", request.tokenID)
			e.route(ctx, request.shardID, control{kind: ctrlRESTFailed, tokenID: request.tokenID, at: e.now()})
			continue
		}

		e.restResyncs.Add(1)
		e.route(ctx, request.shardID, control{
			kind:    ctrlRESTBook,
			tokenID: request.tokenID,
			book:    fetched,
			at:      e.now(),
		})
	}
}

// fetchOne retrieves a single book, which allows a precise answer for the one
// failure that is a fact rather than a setback.
func (e *Engine) fetchOne(ctx context.Context, request resyncRequest) {
	fetched, err := e.rest.Book(ctx, request.tokenID)

	switch {
	case err == nil:
		e.restResyncs.Add(1)
		e.route(ctx, request.shardID, control{
			kind:    ctrlRESTBook,
			tokenID: request.tokenID,
			book:    fetched,
			at:      e.now(),
		})

	case errors.Is(err, restclient.ErrNotFound):
		e.errors.Addf("token %s is not recognised by the exchange", request.tokenID)
		e.route(ctx, request.shardID, control{kind: ctrlTokenNotFound, tokenID: request.tokenID, at: e.now()})

	default:
		e.errors.Addf("could not re-seed token %s: %v", request.tokenID, err)
		e.route(ctx, request.shardID, control{kind: ctrlRESTFailed, tokenID: request.tokenID, at: e.now()})
	}
}

// route delivers a control message to the shard that owns a token.
func (e *Engine) route(ctx context.Context, shardID int, msg control) {
	if shardID < 0 || shardID >= len(e.shards) {
		return
	}

	e.shards[shardID].send(ctx, msg)
}

// seedMetadata fetches every token's book once at the start of the run.
//
// The websocket never sends minimum order size or the negative risk flag, and
// the output document reports both, so this is not an optimisation or a
// fallback: without it those fields would be null on every token of every run.
// It also seeds the book for anything the websocket has not delivered yet.
func (e *Engine) seedMetadata(ctx context.Context) {
	for _, shard := range e.shards {
		for _, batch := range chunk(shard.assetIDs, e.cfg.RESTBatchSize) {
			if ctx.Err() != nil {
				return
			}

			e.outstanding.Add(1)
			books, err := e.rest.Books(ctx, batch)
			if err != nil {
				e.errors.Addf("could not fetch metadata for %d tokens on shard %d: %v",
					len(batch), shard.id, err)
				e.outstanding.Add(-1)
				continue
			}

			for _, fetched := range books {
				e.route(ctx, shard.id, control{
					kind:    ctrlRESTBook,
					tokenID: fetched.AssetID,
					book:    fetched,
					at:      e.now(),
				})
			}
			e.outstanding.Add(-1)
		}
	}
}
