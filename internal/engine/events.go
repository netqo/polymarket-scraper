package engine

import (
	"sync"
	"time"

	"github.com/netqo/polymarket-scraper/internal/report"
	"github.com/netqo/polymarket-scraper/internal/wire"
)

// The cap on each announcement list is a setting rather than a constant: see
// config.DefaultMaxEvents and limits.max_events.
//
// It exists because the announcement feed is global rather than filtered to
// this run's subscription, so its volume has nothing to do with how many tokens
// were asked for. On a busy day the short-duration crypto markets alone
// announce several per minute, and a long window would accumulate them without
// bound.

// eventLog collects the announcements seen during a window.
//
// Every shard receives the same global feed, so the same announcement arrives
// once per connection. Deduplication is not cosmetic: without it a run with
// four shards would report every new market four times, and a consumer counting
// fresh markets would be wrong by a factor of four.
//
// It is one of only two things in the engine that several goroutines write to,
// and like the other it is append-only and guarded by a plain mutex. A
// dedicated collector goroutine would be ceremony: these arrive a few times a
// minute.
type eventLog struct {
	mu sync.Mutex

	// limit is how many entries each list keeps.
	limit int

	newMarkets []report.NewMarket
	resolved   []report.Resolved

	seenNew      map[string]bool
	seenResolved map[string]bool

	suppressed int
}

func newEventLog(limit int) *eventLog {
	return &eventLog{
		limit:        limit,
		seenNew:      make(map[string]bool),
		seenResolved: make(map[string]bool),
	}
}

// noteNewMarket records a market announcement, reporting whether this was the
// first sighting.
//
// Every connection receives the same global feed, so the answer is what lets
// exactly one shard act on an announcement. Without it, four shards would each
// try to subscribe to the same newly announced tokens.
func (l *eventLog) noteNewMarket(event wire.NewMarket, at time.Time) bool {
	key := identify(event.ID, event.ConditionID, event.Market)

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.seenNew[key] {
		return false
	}
	if len(l.newMarkets) >= l.limit {
		l.suppressed++
		// The announcement is not recorded, but it is still the first sighting,
		// and dropping it from the list is no reason to stop collecting the
		// tokens it names.
		l.seenNew[key] = true
		return true
	}
	l.seenNew[key] = true

	l.newMarkets = append(l.newMarkets, report.NewMarket{
		Question:    event.Question,
		ConditionID: report.Optional(event.ConditionID),
		AssetIDs:    event.AssetIDs,
		Outcomes:    event.Outcomes,
		ReceivedAt:  report.FormatTime(at),
	})

	return true
}

// noteResolved records a settlement announcement.
func (l *eventLog) noteResolved(event wire.MarketResolved, at time.Time) {
	key := identify(event.ID, event.Market, event.WinningAssetID)

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.seenResolved[key] {
		return
	}
	if len(l.resolved) >= l.limit {
		l.suppressed++
		return
	}
	l.seenResolved[key] = true

	l.resolved = append(l.resolved, report.Resolved{
		ConditionID:    report.Optional(event.Market),
		AssetIDs:       event.AssetIDs,
		WinningAssetID: report.Optional(event.WinningAssetID),
		WinningOutcome: report.Optional(event.WinningOutcome),
		ReceivedAt:     report.FormatTime(at),
	})
}

// events returns the collected announcements.
func (l *eventLog) events() report.Events {
	l.mu.Lock()
	defer l.mu.Unlock()

	return report.Events{
		NewMarkets: append([]report.NewMarket(nil), l.newMarkets...),
		Resolved:   append([]report.Resolved(nil), l.resolved...),
	}
}

// suppressedCount reports how many announcements the cap dropped, so a run that
// hit it can say so rather than quietly reporting a truncated list.
func (l *eventLog) suppressedCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.suppressed
}

// identify picks the first usable identifier, so an announcement missing one
// field is still deduplicated on another rather than being counted twice.
func identify(candidates ...string) string {
	for _, candidate := range candidates {
		if candidate != "" {
			return candidate
		}
	}

	return ""
}
