// Package book maintains a single token's order book.
//
// It is pure: no goroutines, no clock, no I/O, no knowledge of the wire
// protocol. That isolation is deliberate, because two of the scraper's
// correctness guarantees are properties of this data structure alone and are
// worth being able to prove by table test.
//
// The first is ordering. The published API documentation says bids come back
// descending and asks ascending; the live API does the exact opposite on both
// REST and the websocket. Taking the first element of either array as the best
// price therefore yields the worst one. Rather than trusting either claim, this
// package sorts on ingest and keeps both sides in output order at all times, so
// the invariant is true by construction rather than by a sort call somewhere at
// the end.
//
// The second is deletion. A price change with size zero removes its level; it
// does not set the level to zero. Getting that backwards leaves phantom
// liquidity in the book, which is exactly the kind of error that turns into a
// trade against depth that is not there.
package book

import (
	"cmp"
	"slices"
	"strings"

	"github.com/netqo/polymarket-scraper/internal/decimal"
)

// Side identifies one half of the book.
type Side int

// The two sides. Bids are kept descending and asks ascending, which is the
// order they appear in the output document.
const (
	Bids Side = iota
	Asks
)

// String implements fmt.Stringer for log and error messages.
func (s Side) String() string {
	if s == Bids {
		return "bids"
	}

	return "asks"
}

// Level is one price level: the aggregate size resting at a price.
type Level struct {
	Price decimal.Dec `json:"price"`
	Size  decimal.Dec `json:"size"`
}

// Book is one token's two-sided order book.
//
// The zero Book is an empty book and is ready to use. Both sides are stored as
// sorted slices with binary-search insertion rather than as maps, which keeps
// the best price at index zero in constant time. That matters because the
// window statistics sample the top of book after every single update.
type Book struct {
	bids []Level
	asks []Level
}

// side returns a pointer to the requested side's slice.
func (b *Book) side(s Side) *[]Level {
	if s == Bids {
		return &b.bids
	}

	return &b.asks
}

// Replace seeds one side from a snapshot, discarding whatever was there.
//
// The incoming order is not trusted: levels are copied and sorted into output
// order. Levels whose price cannot be interpreted numerically are kept, at the
// end, because the scraper reports what it saw rather than what it understood.
//
// Levels that compare equal are collapsed, keeping the first. One price is one
// level, and "0.98" and "0.980" are one price: a snapshot spelling it both ways
// would otherwise leave a second entry at that price which no delta can reach,
// since a deletion removes the one the search finds. That is phantom liquidity,
// which is the failure this package exists to make impossible.
func (b *Book) Replace(s Side, levels []Level) {
	sorted := make([]Level, len(levels))
	copy(sorted, levels)
	slices.SortStableFunc(sorted, s.compare)

	*b.side(s) = slices.CompactFunc(sorted, func(a, b Level) bool {
		return s.compare(a, b) == 0
	})
}

// Apply applies one incremental change to a side.
//
// A size of zero removes the level. Any other size inserts or updates it, and
// the stored text is taken from the incoming level in both cases, so the value
// written to the output always comes from the most recent message that touched
// that price.
func (b *Book) Apply(s Side, level Level) {
	levels := b.side(s)

	index, found := slices.BinarySearchFunc(*levels, level, s.compare)

	switch {
	case level.Size.IsZero():
		if found {
			*levels = slices.Delete(*levels, index, index+1)
		}
	case found:
		(*levels)[index] = level
	default:
		*levels = slices.Insert(*levels, index, level)
	}
}

// Levels returns up to limit levels of a side in output order, or all of them
// when limit is not positive. The result is a copy: callers hold on to it while
// the book keeps changing.
func (b *Book) Levels(s Side, limit int) []Level {
	levels := *b.side(s)
	if limit > 0 && limit < len(levels) {
		levels = levels[:limit]
	}

	return slices.Clone(levels)
}

// Len reports how many levels a side holds.
func (b *Book) Len(s Side) int { return len(*b.side(s)) }

// Best returns the best level on a side, and whether there is one.
//
// "Best" means the highest bid or the lowest ask. A level whose price could not
// be interpreted is never returned as best, because a price that cannot be
// compared cannot be known to be the best one.
func (b *Book) Best(s Side) (Level, bool) {
	levels := *b.side(s)
	if len(levels) == 0 || !levels[0].Price.Valid() {
		return Level{}, false
	}

	return levels[0], true
}

// Crossed reports whether the best bid is at or above the best ask.
//
// This is an anomaly, not an error: the scraper flags it and reports the book
// as it stands rather than repairing it, because a crossed book usually means
// the data is stale, and silently fixing it would destroy the evidence.
func (b *Book) Crossed() bool {
	bestBid, haveBid := b.Best(Bids)
	bestAsk, haveAsk := b.Best(Asks)
	if !haveBid || !haveAsk {
		return false
	}

	return decimal.Cmp(bestBid.Price, bestAsk.Price) >= 0
}

// Spread returns the difference between the best ask and the best bid in
// fixed-point units, and whether both sides had a usable price.
func (b *Book) Spread() (int64, bool) {
	bestBid, haveBid := b.Best(Bids)
	bestAsk, haveAsk := b.Best(Asks)
	if !haveBid || !haveAsk {
		return 0, false
	}

	bid, _ := bestBid.Price.Nano()
	ask, _ := bestAsk.Price.Nano()

	return ask - bid, true
}

// compare orders two levels the way the side is stored, which is also the order
// they appear in the output.
//
// Levels are identified by numeric price, so "0.98" and "0.980" are the same
// level and a delta quoted either way updates the same entry. A price that
// could not be interpreted numerically sorts to the end of both sides, ordered
// by its text so the result stays stable across runs.
func (s Side) compare(a, b Level) int {
	left, leftOK := a.Price.Nano()
	right, rightOK := b.Price.Nano()

	switch {
	case leftOK && rightOK:
		if s == Bids {
			return cmp.Compare(right, left)
		}
		return cmp.Compare(left, right)

	case leftOK:
		return -1
	case rightOK:
		return 1
	default:
		return strings.Compare(a.Price.Raw(), b.Price.Raw())
	}
}
