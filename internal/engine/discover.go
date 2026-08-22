package engine

import (
	"slices"
	"strings"
	"time"

	"github.com/netqo/polymarket-scraper/internal/config"
	"github.com/netqo/polymarket-scraper/internal/logging"
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
	if e.cfg.DiscoverLimit <= 0 || e.discoveryClosed.Load() {
		return
	}
	if !e.wanted(event) {
		return
	}

	for _, assetID := range e.newTokens(s, event) {
		// The budget is claimed before a home is found, and given back if none
		// is. Claiming it after would let two announcements handled at the same
		// time both see the last slot and both take it.
		if int(e.discovered.Add(1)) > e.cfg.DiscoverLimit {
			e.discovered.Add(-1)
			e.discoveryClosed.Store(true)
			e.errors.Addf("stopped taking announced tokens at the limit of %d", e.cfg.DiscoverLimit)

			return
		}

		if !e.place(s, assetID, event, at) {
			e.discovered.Add(-1)
			return
		}
	}
}

// wanted reports whether an announcement is one this run cares about.
//
// The feed carries no series field, so the only thing to match on is how the
// market is worded. That is why the pattern is configuration: which markets
// matter is a question about a trading strategy, not about this program, and a
// build that hardcoded "Up or Down" would be wrong the day Polymarket renamed
// something.
func (e *Engine) wanted(event wire.NewMarket) bool {
	if e.discoverMatch == nil {
		return true
	}

	return e.discoverMatch.MatchString(event.Question) || e.discoverMatch.MatchString(event.Slug)
}

// newTokens picks out the tokens of an announcement worth taking on.
func (e *Engine) newTokens(s *shardState, event wire.NewMarket) []string {
	var fresh []string

	for _, assetID := range event.AssetIDs {
		if assetID == "" || e.requested[assetID] {
			continue
		}
		if _, known := s.trackers[assetID]; known {
			continue
		}
		fresh = append(fresh, assetID)
	}

	return fresh
}

// place finds a connection for an announced token, reporting whether it found
// one.
//
// The connection that saw the announcement is preferred, because putting the
// token there needs no coordination: this is that shard's own apply goroutine,
// which is the only thing allowed to touch its trackers. Everything else is a
// hand-off.
func (e *Engine) place(s *shardState, assetID string, event wire.NewMarket, at time.Time) bool {
	if s.reserve(1, e.connectionCeiling()) {
		s.trackers[assetID] = tracker.New(assetID, e.discoveredTrackerOptions())
		e.subscribeTo(s, []string{assetID}, event, at)

		return true
	}

	host, started := e.shardWithRoom(s, 1)
	if host == nil {
		e.discoveryClosed.Store(true)
		e.errors.Addf("stopped taking announced tokens: every connection is at its ceiling of %d assets and no more may be opened",
			e.connectionCeiling())

		return false
	}

	if started {
		// Brand new, so its apply goroutine does not exist yet and this one may
		// populate it directly. Starting it afterwards is what publishes the
		// trackers to it.
		host.trackers[assetID] = tracker.New(assetID, e.discoveredTrackerOptions())
		host.extendSubscription([]string{assetID})
		e.startShard(host)

		e.logger.Info("opened another connection for announced tokens",
			logging.Cat(logging.CategoryDiscovery),
			"shard", host.id, "token", assetID, "question", event.Question)

		return true
	}

	// An existing connection with room. Its trackers belong to its own
	// goroutine, so the token is handed over rather than written across.
	host.send(e.running.collectCtx, control{kind: ctrlAdopt, at: at, tokens: []string{assetID}})

	return true
}

// shardWithRoom finds a connection that can take more tokens, opening one when
// every existing connection is full.
//
// The second result reports that the shard is new and has not been started, so
// its caller may populate it before it has a goroutine of its own.
func (e *Engine) shardWithRoom(exclude *shardState, tokens int) (*shardState, bool) {
	ceiling := e.connectionCeiling()

	e.shards.mu.Lock()
	defer e.shards.mu.Unlock()

	for _, candidate := range e.shards.shards {
		if candidate == exclude {
			continue
		}
		if candidate.reserve(tokens, ceiling) {
			return candidate, false
		}
	}

	if len(e.shards.shards) >= e.maxShards() {
		return nil, false
	}

	grown := newShardState(len(e.shards.shards), nil, e.trackerOptions())
	grown.reserve(tokens, ceiling)
	e.shards.shards = append(e.shards.shards, grown)

	return grown, true
}

// adopt takes on tokens another connection could not fit.
//
// It runs on this shard's own apply goroutine, which is what makes writing to
// the tracker map safe.
func (e *Engine) adopt(s *shardState, tokens []string, at time.Time) {
	var taken []string

	for _, assetID := range tokens {
		if _, known := s.trackers[assetID]; known {
			continue
		}
		s.trackers[assetID] = tracker.New(assetID, e.discoveredTrackerOptions())
		taken = append(taken, assetID)
	}

	if len(taken) == 0 {
		return
	}

	e.subscribeTo(s, taken, wire.NewMarket{}, at)
}

// subscribeTo asks a live connection for more tokens.
//
// The subscription is extended before the request is attempted, and whether or
// not it succeeds: the shard owns these tokens from here on, so every later
// redial has to ask for them too. Leaving them out would narrow the feed back to
// the original shortlist at the first reconnect while their books carried on
// being reported as current, which is the one thing that must not happen.
func (e *Engine) subscribeTo(s *shardState, tokens []string, event wire.NewMarket, at time.Time) {
	s.extendSubscription(tokens)

	conn := s.conn.Load()
	if conn == nil || !conn.Subscribe(tokens) {
		// The tokens belong to the shard but nothing is listening for them yet.
		// They stay, and the sweep will fetch their books over REST, so they are
		// reported honestly either way.
		e.errors.Addf("shard %d: could not subscribe to %d announced tokens; they will be fetched over REST instead",
			s.id, len(tokens))

		return
	}

	e.logger.Info("subscribed to announced tokens", logging.Cat(logging.CategoryDiscovery),
		"shard", s.id, "tokens", len(tokens), "question", event.Question, "at", at)
}

// connectionCeiling is how wide this run will let a connection get.
//
// It is deliberately not the width used to spread the requested tokens. Those
// are two different questions, and conflating them meant that a shortlist
// exactly filling a connection -- which is the consuming agent's documented
// configuration -- switched discovery off entirely, on the run where it matters
// most. The width decides how work is divided; this is the point past which the
// server quietly stops honouring the subscription.
func (e *Engine) connectionCeiling() int {
	ceiling := e.cfg.MaxAssetsPerConnection + e.cfg.DiscoverLimit
	if ceiling > config.MaxAssetsCeiling {
		return config.MaxAssetsCeiling
	}

	return ceiling
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
//
// The engine-wide flag is what actually stops discovery, since an announcement
// can now be placed on a connection other than the one that saw it. This stays
// so that a shard's own state still says so.
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
	slices.SortFunc(snapshots, func(a, b tracker.Snapshot) int {
		return strings.Compare(a.TokenID, b.TokenID)
	})
}
