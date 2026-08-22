package tracker

import (
	"github.com/netqo/polymarket-scraper/internal/book"
	"github.com/netqo/polymarket-scraper/internal/decimal"
)

// midScaleQuote is the scale a mid price is rendered at.
//
// A mid is the sum of two nano-scale prices halved, so it can land on a half of
// the smallest unit and needs one digit more than the prices themselves. The
// same reasoning as the window statistics, and the same arithmetic: halving is
// deferred to formatting, where it stays exact.
const midScaleQuote = decimal.Scale + 1

// Observer is told about a token's changes as they happen.
//
// It exists so a run can be watched while it is running rather than only read
// once it has finished. The document is complete but arrives at the end, which
// is no use to anything deciding what to do during the window.
//
// Every method runs on the goroutine that owns the tracker and must not block.
// This package performs no I/O of its own and knows nothing about where the
// changes go, which is what keeps its trust rules verifiable with no clock and
// no network in the way.
type Observer interface {
	// Flagged reports a flag the first time it is raised for a token.
	Flagged(tokenID string, flag Flag)

	// Quoted reports that a token's top of book changed. It is not called for
	// an update that leaves the best bid and ask where they were, which is most
	// of them.
	Quoted(tokenID string, quote Quote)

	// Traded reports a fill.
	Traded(tokenID string, trade LastTrade)
}

// Quote is a token's top of book at a moment.
//
// Every field is a decimal for the same reason the document's are: these
// numbers are read by something deciding whether to act, and a value that has
// been through binary floating point is a value that might have been rounded.
// A field is absent, and therefore null, when there is no honest answer rather
// than when the answer is zero.
type Quote struct {
	// Bid and Ask are the best prices on each side, absent when that side is
	// empty or its best price could not be interpreted.
	Bid decimal.Dec
	Ask decimal.Dec

	// Spread and Mid are absent unless both sides had a usable price. A
	// one-sided book has no spread, and reporting zero would be inventing one.
	Spread decimal.Dec
	Mid    decimal.Dec

	// Crossed reports the best bid at or above the best ask, which usually
	// means the data is stale.
	Crossed bool

	// ExchangeTimestamp is the feed's own timestamp for the update that moved
	// the quote, verbatim.
	ExchangeTimestamp string
}

// quote reads the current top of book.
func (t *Tracker) quote() Quote {
	current := Quote{
		Crossed:           t.book.Crossed(),
		ExchangeTimestamp: t.exchangeTimestamp,
	}

	bestBid, haveBid := t.book.Best(book.Bids)
	if haveBid {
		current.Bid = bestBid.Price
	}

	bestAsk, haveAsk := t.book.Best(book.Asks)
	if haveAsk {
		current.Ask = bestAsk.Price
	}

	if !haveBid || !haveAsk {
		return current
	}

	if spread, ok := t.book.Spread(); ok {
		current.Spread = decimal.FromScaled(spread, decimal.Scale)
	}

	bid, _ := bestBid.Price.Nano()
	ask, _ := bestAsk.Price.Nano()
	// The sum is twice the mid, so halving it and shifting one decimal place is
	// the same as multiplying by five at the finer scale. That keeps the
	// division exact.
	current.Mid = decimal.FromScaled((bid+ask)*5, midScaleQuote)

	return current
}

// noteQuote tells the observer when the top of book has moved.
//
// Most updates change a level nobody is looking at. Reporting every one of them
// would bury the handful that moved the price, and at several hundred tokens it
// would be the dominant cost of the run.
func (t *Tracker) noteQuote() {
	if t.opts.Observer == nil {
		return
	}

	current := t.quote()
	if t.haveQuote && sameQuote(t.lastQuote, current) {
		return
	}

	t.lastQuote, t.haveQuote = current, true
	t.opts.Observer.Quoted(t.tokenID, current)
}

// sameQuote reports whether two quotes describe the same top of book.
//
// Compared by value rather than by text, so a price respelled from "0.98" to
// "0.980" is not reported as a move. The timestamp is excluded because it
// changes on every update and would make every comparison differ.
func sameQuote(a, b Quote) bool {
	return decimal.Cmp(a.Bid, b.Bid) == 0 &&
		decimal.Cmp(a.Ask, b.Ask) == 0 &&
		a.Crossed == b.Crossed
}
