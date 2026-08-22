package tracker

import (
	"testing"
	"time"

	"github.com/netqo/polymarket-scraper/internal/book"
	"github.com/netqo/polymarket-scraper/internal/decimal"
	"github.com/netqo/polymarket-scraper/internal/wire"
)

// quoting returns a tracker that records every quote reported for it.
func quoting(t *testing.T) (*Tracker, *[]Quote) {
	t.Helper()

	var seen []Quote
	tr := New("111", Options{
		Observer: recorder{onQuote: func(_ string, quote Quote) { seen = append(seen, quote) }},
	})

	return tr, &seen
}

func TestQuoteReportsTheTopOfBook(t *testing.T) {
	tr, seen := quoting(t)

	tr.ApplySnapshot(twoSided("0.97", "0.99", "1000"), at(0))

	if len(*seen) != 1 {
		t.Fatalf("got %d quotes, want 1: %+v", len(*seen), *seen)
	}

	got := (*seen)[0]
	checks := []struct {
		name string
		got  string
		want string
	}{
		{"bid", got.Bid.Raw(), "0.97"},
		{"ask", got.Ask.Raw(), "0.99"},
		{"spread", got.Spread.Raw(), "0.02"},
		{"mid", got.Mid.Raw(), "0.98"},
		{"exchange timestamp", got.ExchangeTimestamp, "1000"},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s = %q, want %q", check.name, check.got, check.want)
		}
	}
}

// Most updates change a level nobody is looking at. Reporting every one would
// bury the handful that moved the price, and at several hundred tokens it would
// be the dominant cost of the run.
func TestQuoteIsNotReportedWhenTheTopOfBookDoesNotMove(t *testing.T) {
	tr, seen := quoting(t)

	tr.ApplySnapshot(twoSided("0.97", "0.99", "1000"), at(0))

	// Deeper in the book, on both sides. Neither touches the best price.
	tr.ApplyChange(change("0.95", "500", wire.SideBuy, "h1"), "1001", at(time.Second))
	tr.ApplyChange(change("1.00", "500", wire.SideSell, "h2"), "1002", at(2*time.Second))

	if len(*seen) != 1 {
		t.Errorf("got %d quotes, want only the first: %+v", len(*seen), *seen)
	}
}

func TestQuoteIsReportedWhenTheTopOfBookMoves(t *testing.T) {
	tr, seen := quoting(t)

	tr.ApplySnapshot(twoSided("0.97", "0.99", "1000"), at(0))
	tr.ApplyChange(change("0.98", "100", wire.SideBuy, "h1"), "1001", at(time.Second))

	if len(*seen) != 2 {
		t.Fatalf("got %d quotes, want the move reported: %+v", len(*seen), *seen)
	}
	if got := (*seen)[1].Bid.Raw(); got != "0.98" {
		t.Errorf("bid = %q, want the new best 0.98", got)
	}
}

// The same price spelled differently is the same price. Reporting it as a move
// would fill the stream with changes that did not happen.
func TestARespelledPriceIsNotAMove(t *testing.T) {
	tr, seen := quoting(t)

	tr.ApplySnapshot(twoSided("0.97", "0.99", "1000"), at(0))
	tr.ApplyChange(change("0.970", "250", wire.SideBuy, "h1"), "1001", at(time.Second))

	if len(*seen) != 1 {
		t.Errorf("got %d quotes, want the respelling ignored: %+v", len(*seen), *seen)
	}
}

// A one-sided book has no spread, and reporting zero would be inventing one.
func TestAOneSidedBookHasNoSpreadOrMid(t *testing.T) {
	tr, seen := quoting(t)

	tr.ApplySnapshot(wire.Book{
		Market:    "0xmarket",
		Timestamp: "1000",
		Bids:      []book.Level{lvl("0.97", "100")},
	}, at(0))

	if len(*seen) != 1 {
		t.Fatalf("got %d quotes, want 1", len(*seen))
	}

	got := (*seen)[0]
	if got.Bid.Raw() != "0.97" {
		t.Errorf("bid = %q, want 0.97", got.Bid.Raw())
	}
	if !got.Ask.Absent() {
		t.Errorf("ask = %q, want absent", got.Ask.Raw())
	}
	if !got.Spread.Absent() || !got.Mid.Absent() {
		t.Errorf("spread %q and mid %q, want both absent", got.Spread.Raw(), got.Mid.Raw())
	}
}

// A mid lands on half of the smallest unit whenever the two best prices differ
// by an odd number of them, so the halving is deferred to formatting where it
// stays exact.
func TestQuoteMidIsExactOnAHalfUnit(t *testing.T) {
	tr, seen := quoting(t)

	tr.ApplySnapshot(twoSided("0.001", "0.002", "1000"), at(0))

	if got := (*seen)[0].Mid.Raw(); got != "0.0015" {
		t.Errorf("mid = %q, want 0.0015", got)
	}
}

func TestQuoteReportsACrossedBook(t *testing.T) {
	tr, seen := quoting(t)

	tr.ApplySnapshot(twoSided("0.99", "0.97", "1000"), at(0))

	if !(*seen)[0].Crossed {
		t.Error("a crossed book was reported as normal")
	}
}

func TestTradesAreReportedAsTheyArrive(t *testing.T) {
	var seen []LastTrade
	tr := New("111", Options{
		Observer: recorder{onTrade: func(_ string, trade LastTrade) { seen = append(seen, trade) }},
	})

	tr.ApplySnapshot(snapshotAt("1000"), at(0))
	tr.ApplyTrade(wire.LastTrade{
		Price:      decimal.Parse("0.980"),
		Size:       decimal.Parse("120"),
		Side:       wire.SideBuy,
		FeeRateBPS: decimal.Parse("0"),
		Timestamp:  "1500",
	}, at(time.Second))

	if len(seen) != 1 {
		t.Fatalf("got %d trades, want 1", len(seen))
	}
	if got := seen[0].Price.Raw(); got != "0.980" {
		t.Errorf("price = %q, want the feed spelling 0.980", got)
	}
	if got := seen[0].FeeRateBPS.Raw(); got != "0" {
		t.Errorf("fee rate = %q, want 0", got)
	}
}

// An update applied to a token that is not trusted must not be reported either,
// or the stream would carry prices the document refuses to stand behind.
func TestNoQuoteIsReportedWhileATokenIsUntrusted(t *testing.T) {
	tr, seen := quoting(t)

	tr.ApplySnapshot(twoSided("0.97", "0.99", "1000"), at(0))
	before := len(*seen)

	tr.NoteDisconnect()
	tr.ApplyChange(change("0.5", "999", wire.SideBuy, "h9"), "2000", at(time.Second))

	if len(*seen) != before {
		t.Errorf("got %d quotes, want none while untrusted: %+v", len(*seen), (*seen)[before:])
	}
}

// A tracker with no observer must behave exactly as it did before there was
// one, which is the case for every existing test in this package.
func TestNoObserverIsHarmless(t *testing.T) {
	tr := New("111", Options{})

	tr.ApplySnapshot(twoSided("0.97", "0.99", "1000"), at(0))
	tr.ApplyChange(change("0.98", "100", wire.SideBuy, "h1"), "1001", at(time.Second))
	tr.ApplyTrade(wire.LastTrade{Price: decimal.Parse("0.98")}, at(2*time.Second))

	if got := tr.Finalize(at(time.Minute)); got.Status != StatusOK {
		t.Errorf("Status = %q, want ok", got.Status)
	}
}
