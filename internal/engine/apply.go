package engine

import (
	"context"
	"time"

	"github.com/netqo/polymarket-scraper/internal/tracker"
	"github.com/netqo/polymarket-scraper/internal/wire"
	"github.com/netqo/polymarket-scraper/internal/wsclient"
)

// applyLoop owns a shard's tracker state for the life of the run.
//
// It performs no I/O and makes no blocking send. Requests for a re-seed go out
// through a non-blocking send, and a request that cannot be queued becomes an
// honest failure for that token rather than a stall. That is what guarantees
// this goroutine can always be brought to a stop, and therefore that the run
// can always produce its document.
//
// When the window closes it moves into a drain rather than stopping, so that
// re-seeds still in flight when the sockets shut have somewhere to land.
func (e *Engine) applyLoop(collectCtx, drainCtx context.Context, s *shardState, results chan<- shardResult) {
	for {
		select {
		case frame := <-s.frames:
			e.applyFrame(s, frame)

		case msg := <-s.control:
			e.applyControl(s, msg)

		case <-collectCtx.Done():
			e.drainShard(drainCtx, s, results)
			return
		}
	}
}

// drainInterval is how often the drain checks whether it still has a reason to
// wait.
const drainInterval = 10 * time.Millisecond

// drainShard keeps applying until nothing is outstanding, or the drain deadline
// arrives.
//
// Waiting the whole drain allowance regardless would add seconds of dead time
// to every run, which matters because the caller has its own budget to spend.
// The wait exists to let re-seeds that were in flight when the window closed
// land, so once none are, there is nothing left to wait for.
func (e *Engine) drainShard(drainCtx context.Context, s *shardState, results chan<- shardResult) {
	idle := time.NewTicker(drainInterval)
	defer idle.Stop()

	for {
		select {
		case frame := <-s.frames:
			e.applyFrame(s, frame)

		case msg := <-s.control:
			e.applyControl(s, msg)

		case <-idle.C:
			if e.outstanding.Load() == 0 {
				results <- s.finalize(e.now())
				return
			}

		case <-drainCtx.Done():
			e.finishShard(s, results)
			return
		}
	}
}

// finishShard consumes whatever is already queued, then freezes every tracker.
//
// The queued work is drained without blocking: it is the results of re-seeds
// that arrived while the window was closing, and applying them turns tokens
// that would otherwise be reported as failures into good data. Anything not yet
// arrived is not waited for.
func (e *Engine) finishShard(s *shardState, results chan<- shardResult) {
	for {
		select {
		case frame := <-s.frames:
			e.applyFrame(s, frame)
		case msg := <-s.control:
			e.applyControl(s, msg)
		default:
			results <- s.finalize(e.now())
			return
		}
	}
}

// finalize freezes every tracker this shard owns.
func (s *shardState) finalize(at time.Time) shardResult {
	snapshots := make(map[string]tracker.Snapshot, len(s.trackers))
	for id, t := range s.trackers {
		snapshots[id] = t.Finalize(at)
	}

	return shardResult{shardID: s.id, snapshots: snapshots}
}

// applyFrame routes one decoded frame to the tokens it concerns.
func (e *Engine) applyFrame(s *shardState, frame wsclient.Frame) {
	if frame.Err != nil {
		e.handleDecodeFailure(s, frame)
	}

	for _, event := range frame.Events {
		e.applyEvent(s, event, frame.ReceivedAt)
	}
}

// handleDecodeFailure deals with a frame that did not fully decode.
//
// The hard part is that a message we could not read is also a message whose
// token we do not know. There is no way to attribute the loss, so every token
// on the connection is treated as having possibly missed an update. That is
// expensive and deliberately so: the alternative is to carry on with a book
// that may have silently diverged, which is the one outcome this whole program
// exists to prevent. Re-seeding is batched, so the cost of being wrong here is
// a couple of extra requests rather than one per token.
func (e *Engine) handleDecodeFailure(s *shardState, frame wsclient.Frame) {
	e.errors.Addf("shard %d: a frame could not be decoded, so every token on it was re-seeded: %v",
		s.id, frame.Err)

	for _, t := range s.trackers {
		e.act(s, t, t.NoteDecodeError())
	}
}

// applyEvent routes a single event.
func (e *Engine) applyEvent(s *shardState, event wire.Event, at time.Time) {
	switch typed := event.(type) {
	case wire.Book:
		e.withTracker(s, typed.AssetID, func(t *tracker.Tracker) tracker.Effect {
			return t.ApplySnapshot(typed, at)
		})

	case wire.PriceChange:
		// The envelope names no token: each entry does, and one message
		// routinely covers both legs of a binary market.
		for _, entry := range typed.Changes {
			e.withTracker(s, entry.AssetID, func(t *tracker.Tracker) tracker.Effect {
				return t.ApplyChange(entry, typed.Timestamp, at)
			})
		}

	case wire.LastTrade:
		e.withTracker(s, typed.AssetID, func(t *tracker.Tracker) tracker.Effect {
			return t.ApplyTrade(typed, at)
		})

	case wire.TickSizeChange:
		e.withTracker(s, typed.AssetID, func(t *tracker.Tracker) tracker.Effect {
			return t.ApplyTickSize(typed, at)
		})

	case wire.BestBidAsk:
		e.withTracker(s, typed.AssetID, func(t *tracker.Tracker) tracker.Effect {
			return t.ApplyBestBidAsk(typed, at)
		})

	case wire.MarketResolved:
		e.events.noteResolved(typed, at)
		for _, assetID := range typed.AssetIDs {
			e.withTracker(s, assetID, func(t *tracker.Tracker) tracker.Effect {
				return t.NoteMarketResolved()
			})
		}

	case wire.NewMarket:
		// The announcement feed is global rather than filtered to this
		// subscription, so this concerns no token the shard owns. It is
		// reported anyway: a consumer sweeping for freshly created markets
		// depends on it, and every shard sees the same feed, which is why the
		// log deduplicates.
		if firstSighting := e.events.noteNewMarket(typed, at); firstSighting {
			e.admitAnnounced(s, typed, at)
		}

	case wire.Unknown:
		e.errors.Addf("shard %d: ignored an event type this build does not know: %s", s.id, typed.EventType)
	}
}

// withTracker applies an operation to a token this shard owns, ignoring events
// for anything it does not.
func (e *Engine) withTracker(s *shardState, tokenID string, op func(*tracker.Tracker) tracker.Effect) {
	t, owned := s.trackers[tokenID]
	if !owned {
		return
	}

	e.act(s, t, op(t))
}

// applyControl handles a message from something other than the websocket.
func (e *Engine) applyControl(s *shardState, msg control) {
	switch msg.kind {
	case ctrlDisconnected:
		// Missed updates are never replayed, so every token on this connection
		// is now of unknown accuracy, whatever its book looks like.
		for _, t := range s.trackers {
			e.act(s, t, t.NoteDisconnect())
		}

	case ctrlSubscribeFailed:
		for _, t := range s.trackers {
			t.NoteSubscribeFailed()
		}

	case ctrlRESTBook:
		e.withTracker(s, msg.tokenID, func(t *tracker.Tracker) tracker.Effect {
			return t.ApplyRESTBook(msg.book, msg.at)
		})

	case ctrlRESTFailed:
		e.withTracker(s, msg.tokenID, func(t *tracker.Tracker) tracker.Effect {
			return t.NoteResyncFailed()
		})

	case ctrlTokenNotFound:
		e.withTracker(s, msg.tokenID, func(t *tracker.Tracker) tracker.Effect {
			return t.NoteTokenNotFound()
		})

	case ctrlSweep:
		s.closeDiscovery()
		for _, t := range s.trackers {
			e.act(s, t, t.Sweep())
		}
	}
}

// act carries out what a tracker asked for.
//
// The send is non-blocking on purpose. The queue is sized so that every token
// can have a request outstanding at once, so a full queue means something has
// gone badly wrong; in that case the token is reported as a failure rather than
// having this goroutine wait, because waiting here is what would make the run
// unable to stop.
func (e *Engine) act(s *shardState, t *tracker.Tracker, effect tracker.Effect) {
	if effect != tracker.EffectRequestResync {
		return
	}

	select {
	case e.resync <- resyncRequest{shardID: s.id, tokenID: t.TokenID()}:
		e.outstanding.Add(1)
	default:
		t.NoteResyncFailed()
		e.errors.Addf("shard %d: could not queue a re-seed for token %s, so it is reported as failed",
			s.id, t.TokenID())
	}
}
