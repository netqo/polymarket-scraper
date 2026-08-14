package engine

import (
	"time"

	"github.com/netqo/polymarket-scraper/internal/tracker"
	"github.com/netqo/polymarket-scraper/internal/wire"
)

// admitAnnounced subscribes mid-window to the tokens a new market announcement
// names.
//
// This is what makes a run useful during the short-duration crypto series,
// where instances are created minutes before they start: a token list assembled
// before the run began does not contain them, and by the time the next run
// starts they may already be resolving.
//
// It runs on the apply goroutine, which is the only thing allowed to touch this
// shard's trackers, so adding one here needs no synchronisation. The
// subscription itself is a non-blocking hand-off to the connection's write
// queue, so a wedged connection cannot stall the apply loop.
//
// Two limits apply, and both are real rather than defensive. The announcement
// feed is global rather than filtered to this run, so without a budget a run
// would gradually subscribe to every market on the exchange. And the connection
// itself stops sending snapshots past a certain width, so filling one past that
// point would quietly cost the tokens that were actually requested.
func (e *Engine) admitAnnounced(s *shardState, event wire.NewMarket, at time.Time) {
	if e.cfg.DiscoverLimit <= 0 || s.closedToDiscovery {
		return
	}

	added := e.collectAdmissible(s, event)
	if len(added) == 0 {
		return
	}

	conn := s.conn.Load()
	if conn == nil || !conn.Subscribe(added) {
		// The tokens were added to the shard but nothing is listening for them.
		// They stay, and the sweep will fetch their books over REST, so they
		// are reported honestly either way.
		e.errors.Addf("shard %d: could not subscribe to %d announced tokens; they will be fetched over REST instead",
			s.id, len(added))
		return
	}

	e.logger.Info("subscribed to announced tokens",
		"shard", s.id, "tokens", len(added), "question", event.Question, "at", at)
}

// collectAdmissible decides which of an announcement's tokens to take on.
func (e *Engine) collectAdmissible(s *shardState, event wire.NewMarket) []string {
	var added []string

	for _, assetID := range event.AssetIDs {
		if assetID == "" || e.requested[assetID] {
			continue
		}
		if _, known := s.trackers[assetID]; known {
			continue
		}

		if len(s.trackers) >= e.cfg.MaxAssetsPerConnection {
			s.closedToDiscovery = true
			e.errors.Addf("shard %d stopped taking announced tokens: the connection is at its width limit of %d",
				s.id, e.cfg.MaxAssetsPerConnection)
			break
		}

		if int(e.discovered.Add(1)) > e.cfg.DiscoverLimit {
			e.discovered.Add(-1)
			s.closedToDiscovery = true
			e.errors.Addf("stopped taking announced tokens at the limit of %d", e.cfg.DiscoverLimit)
			break
		}

		s.trackers[assetID] = tracker.New(assetID, e.discoveredTrackerOptions())
		s.discovered[assetID] = true
		added = append(added, assetID)
	}

	return added
}

// discoveredTrackerOptions mark a token as picked up rather than requested, so
// its entry in the document says where it came from.
func (e *Engine) discoveredTrackerOptions() tracker.Options {
	opts := e.trackerOptions()
	opts.Discovered = true

	return opts
}

// closeDiscovery stops a shard taking on new tokens.
//
// It runs at the sweep, near the end of the window. A token subscribed in the
// last seconds would produce a book with almost no history behind it and no
// time to recover if anything went wrong, which is worse than not having it:
// the consumer would have to distinguish it from a fully observed one, and
// nothing in the document would help.
func (s *shardState) closeDiscovery() { s.closedToDiscovery = true }

// splitDiscovered separates the requested tokens from the picked-up ones.
//
// The requested list is what the document's completeness guarantee is measured
// against, so the two cannot be mixed: a discovered token must never be able to
// stand in for one that was asked for and never arrived.
func (e *Engine) splitDiscovered(all map[string]tracker.Snapshot) (map[string]tracker.Snapshot, []tracker.Snapshot) {
	requested := make(map[string]tracker.Snapshot, len(all))
	var discovered []tracker.Snapshot

	for id, snapshot := range all {
		if e.requested[id] {
			requested[id] = snapshot
			continue
		}
		discovered = append(discovered, snapshot)
	}

	// Sorted, because map iteration is not, and the document should not change
	// order between runs that saw the same thing.
	sortSnapshots(discovered)

	return requested, discovered
}

// sortSnapshots orders snapshots by token id.
func sortSnapshots(snapshots []tracker.Snapshot) {
	for i := 1; i < len(snapshots); i++ {
		for j := i; j > 0 && snapshots[j].TokenID < snapshots[j-1].TokenID; j-- {
			snapshots[j], snapshots[j-1] = snapshots[j-1], snapshots[j]
		}
	}
}
