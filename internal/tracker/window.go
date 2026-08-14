package tracker

import (
	"time"

	"github.com/netqo/polymarket-scraper/internal/book"
	"github.com/netqo/polymarket-scraper/internal/decimal"
)

// midScale is the number of fractional digits needed to express a mid price
// exactly. A mid is the sum of two nano-scale prices halved, so it can land on
// a half of the smallest unit and needs one digit more than the prices do.
const midScale = decimal.Scale + 1

// window accumulates volatility context for a token in constant space.
//
// Constant space is a requirement rather than an optimisation: hundreds of
// tokens across a window that can run for minutes means retaining a time series
// per token is exactly the kind of unbounded growth the memory budget forbids.
// Everything here is integer arithmetic on fixed-point values, so the numbers
// that reach the output carry the same no-artifacts guarantee as values that
// came straight off the wire.
type window struct {
	updates int

	// Mid prices are held as the sum of the two best prices in nano, which is
	// twice the mid. Halving is deferred to formatting, where it is exact.
	midHigh, midLow int64
	haveMid         bool

	// The time-weighted spread is a sum of spread times elapsed milliseconds,
	// divided at the end by the time the book actually had two sides. It is
	// bounded by the widest possible spread times the window length regardless
	// of how many samples arrive, which leaves four orders of magnitude of
	// headroom in an int64.
	spreadWeighted int64
	twoSidedMillis int64

	lastSampleAt time.Time
	lastSpread   int64
	lastTwoSided bool
}

// sample records the state of the book at a moment in time.
//
// It closes the interval since the previous sample using the book as it was
// during that interval, then reads the current top of book. Intervals where the
// book had only one side contribute nothing to the spread average, because
// there was no spread to average.
func (w *window) sample(b *book.Book, at time.Time) {
	w.closeInterval(at)

	spread, twoSided := b.Spread()
	w.lastSpread, w.lastTwoSided = spread, twoSided

	bestBid, haveBid := b.Best(book.Bids)
	bestAsk, haveAsk := b.Best(book.Asks)
	if !haveBid || !haveAsk {
		return
	}

	bid, _ := bestBid.Price.Nano()
	ask, _ := bestAsk.Price.Nano()
	sum := bid + ask

	switch {
	case !w.haveMid:
		w.midHigh, w.midLow, w.haveMid = sum, sum, true
	case sum > w.midHigh:
		w.midHigh = sum
	case sum < w.midLow:
		w.midLow = sum
	}
}

// closeInterval accumulates the time-weighted spread up to at.
func (w *window) closeInterval(at time.Time) {
	defer func() { w.lastSampleAt = at }()

	if w.lastSampleAt.IsZero() || !w.lastTwoSided {
		return
	}

	elapsed := at.Sub(w.lastSampleAt).Milliseconds()
	if elapsed <= 0 {
		return
	}

	w.spreadWeighted += w.lastSpread * elapsed
	w.twoSidedMillis += elapsed
}

// summarize closes the final interval and renders the accumulated statistics.
func (w *window) summarize(at time.Time) WindowSummary {
	w.closeInterval(at)

	summary := WindowSummary{
		Updates:        w.updates,
		TwoSidedMillis: w.twoSidedMillis,
	}

	if w.haveMid {
		// The stored value is twice the mid in nano, so halving it and shifting
		// one decimal place is the same as multiplying by five at the finer
		// scale. That keeps the division exact.
		summary.MidHigh = decimal.FromScaled(w.midHigh*5, midScale)
		summary.MidLow = decimal.FromScaled(w.midLow*5, midScale)
	}

	if w.twoSidedMillis > 0 {
		summary.SpreadTimeWeighted = decimal.FromScaled(w.spreadWeighted/w.twoSidedMillis, decimal.Scale)
	}

	return summary
}

// WindowSummary is the volatility context reported for a token.
//
// The decimal fields are absent, and therefore null in the output, when the
// book never had two sides to measure. Reporting a zero there would be
// inventing a number.
type WindowSummary struct {
	// Updates counts the incremental changes actually applied.
	Updates int

	// MidHigh and MidLow bound the mid price over the window.
	MidHigh decimal.Dec
	MidLow  decimal.Dec

	// SpreadTimeWeighted is the spread averaged over the time the book had two
	// sides, not over the number of updates: a book that sat wide for a minute
	// and tight for a second was mostly wide.
	SpreadTimeWeighted decimal.Dec

	// TwoSidedMillis is how long the book had two sides, so a consumer can see
	// how much of the window the average actually covers.
	TwoSidedMillis int64
}
