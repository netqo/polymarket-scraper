// Test data: Invented books at chosen times. The statistics are integer arithmetic over a
// timeline the test controls, so a real feed would only make the expected values
// harder to state.

package tracker

import (
	"testing"
	"time"

	"github.com/netqo/polymarket-scraper/internal/book"
	"github.com/netqo/polymarket-scraper/internal/decimal"
	"github.com/netqo/polymarket-scraper/internal/wire"
)

// twoSided builds a snapshot with the given best quotes.
func twoSided(bid, ask, millis string) wire.Book {
	return wire.Book{
		Market:    "0xmarket",
		Timestamp: millis,
		Bids:      []book.Level{lvl(bid, "100")},
		Asks:      []book.Level{lvl(ask, "100")},
	}
}

// Mid prices land on half of the smallest unit whenever the two best prices
// differ by an odd number of them, so the halving is deferred to formatting
// where it stays exact rather than being rounded away.
func TestWindowMidPriceIsExactEvenOnAHalfUnit(t *testing.T) {
	tr := New("111", Options{})
	tr.ApplySnapshot(twoSided("0.001", "0.002", "1000"), at(0))

	got := tr.Finalize(at(time.Second)).Window
	if got.MidHigh.Raw() != "0.0015" {
		t.Errorf("MidHigh = %q, want 0.0015", got.MidHigh.Raw())
	}
	if got.MidLow.Raw() != "0.0015" {
		t.Errorf("MidLow = %q, want 0.0015", got.MidLow.Raw())
	}
}

func TestWindowTracksTheMidPriceRange(t *testing.T) {
	tr := New("111", Options{})
	tr.ApplySnapshot(twoSided("0.40", "0.42", "1000"), at(0))             // mid 0.41
	tr.ApplySnapshot(twoSided("0.50", "0.52", "2000"), at(time.Second))   // mid 0.51
	tr.ApplySnapshot(twoSided("0.30", "0.32", "3000"), at(2*time.Second)) // mid 0.31
	tr.ApplySnapshot(twoSided("0.44", "0.46", "4000"), at(3*time.Second)) // mid 0.45

	got := tr.Finalize(at(4 * time.Second)).Window
	if got.MidHigh.Raw() != "0.51" {
		t.Errorf("MidHigh = %q, want 0.51", got.MidHigh.Raw())
	}
	if got.MidLow.Raw() != "0.31" {
		t.Errorf("MidLow = %q, want 0.31", got.MidLow.Raw())
	}
}

// The average is weighted by time, not by how many updates arrived: a book that
// sat wide for a minute and tight for a second was mostly wide, and counting
// samples would say the opposite.
func TestWindowSpreadIsWeightedByTimeNotBySampleCount(t *testing.T) {
	tr := New("111", Options{})

	// Wide for nine seconds, then narrow for one.
	tr.ApplySnapshot(twoSided("0.40", "0.50", "1000"), at(0))
	tr.ApplySnapshot(twoSided("0.44", "0.45", "2000"), at(9*time.Second))

	got := tr.Finalize(at(10 * time.Second)).Window

	// (0.10 * 9000ms + 0.01 * 1000ms) / 10000ms = 0.091
	if got.SpreadTimeWeighted.Raw() != "0.091" {
		t.Errorf("SpreadTimeWeighted = %q, want 0.091", got.SpreadTimeWeighted.Raw())
	}
	if got.TwoSidedMillis != 10_000 {
		t.Errorf("TwoSidedMillis = %d, want 10000", got.TwoSidedMillis)
	}
}

// D3: an average over no time at all is not zero, it is unknown. Reporting zero
// would be inventing a number the consumer then acts on.
func TestWindowReportsNothingWhenTheBookNeverHadTwoSides(t *testing.T) {
	tr := New("111", Options{})
	tr.ApplySnapshot(wire.Book{
		Market:    "0xmarket",
		Timestamp: "1000",
		Bids:      []book.Level{lvl("0.40", "100")},
	}, at(0))

	got := tr.Finalize(at(10 * time.Second)).Window

	if !got.SpreadTimeWeighted.Absent() {
		t.Errorf("SpreadTimeWeighted = %q, want absent so it serializes as null", got.SpreadTimeWeighted.Raw())
	}
	if !got.MidHigh.Absent() || !got.MidLow.Absent() {
		t.Errorf("mid range = %q..%q, want absent", got.MidLow.Raw(), got.MidHigh.Raw())
	}
	if got.TwoSidedMillis != 0 {
		t.Errorf("TwoSidedMillis = %d, want 0", got.TwoSidedMillis)
	}
}

// Time spent one-sided contributes nothing to the average, and the reported
// coverage says how much of the window the average actually covers.
func TestWindowExcludesOneSidedTimeFromTheAverage(t *testing.T) {
	tr := New("111", Options{})

	tr.ApplySnapshot(twoSided("0.40", "0.50", "1000"), at(0))
	tr.ApplySnapshot(wire.Book{
		Market:    "0xmarket",
		Timestamp: "2000",
		Bids:      []book.Level{lvl("0.40", "100")},
	}, at(2*time.Second))

	got := tr.Finalize(at(10 * time.Second)).Window

	if got.TwoSidedMillis != 2_000 {
		t.Errorf("TwoSidedMillis = %d, want the 2000ms the book actually had two sides", got.TwoSidedMillis)
	}
	if got.SpreadTimeWeighted.Raw() != "0.1" {
		t.Errorf("SpreadTimeWeighted = %q, want 0.1 from the two-sided period alone", got.SpreadTimeWeighted.Raw())
	}
}

func TestWindowCountsAppliedUpdatesOnly(t *testing.T) {
	tr := New("111", Options{})

	// Dropped: no snapshot yet.
	tr.ApplyChange(change("0.98", "1", wire.SideBuy, "h0"), "999", at(0))

	tr.ApplySnapshot(snapshotAt("1000"), at(time.Second))
	tr.ApplyChange(change("0.98", "1", wire.SideBuy, "h1"), "1001", at(2*time.Second))
	tr.ApplyChange(change("0.981", "2", wire.SideBuy, "h2"), "1002", at(3*time.Second))

	// Dropped: unrecognised side.
	tr.ApplyChange(change("0.982", "3", "SIDEWAYS", "h3"), "1003", at(4*time.Second))

	got := tr.Finalize(at(10 * time.Second)).Window
	if got.Updates != 2 {
		t.Errorf("Updates = %d, want 2", got.Updates)
	}
}

// The window closes at finalize, so the last interval is counted rather than
// silently discarded.
func TestWindowClosesTheFinalInterval(t *testing.T) {
	tr := New("111", Options{})
	tr.ApplySnapshot(twoSided("0.40", "0.50", "1000"), at(0))

	early := tr.Finalize(at(time.Second)).Window
	if early.TwoSidedMillis != 1_000 {
		t.Errorf("TwoSidedMillis at 1s = %d, want 1000", early.TwoSidedMillis)
	}
}

// The accumulator is bounded by the widest possible spread times the window
// length, independent of how many samples arrive, which is what makes constant
// space safe here.
func TestWindowAccumulatorSurvivesManySamples(t *testing.T) {
	tr := New("111", Options{})
	tr.ApplySnapshot(twoSided("0", "1", "1000"), at(0))

	for i := 1; i <= 10_000; i++ {
		tr.ApplySnapshot(twoSided("0", "1", "1000"), at(time.Duration(i)*time.Millisecond))
	}

	got := tr.Finalize(at(20 * time.Second)).Window
	if got.SpreadTimeWeighted.Raw() != "1" {
		t.Errorf("SpreadTimeWeighted = %q, want 1", got.SpreadTimeWeighted.Raw())
	}
	if got.TwoSidedMillis != 20_000 {
		t.Errorf("TwoSidedMillis = %d, want 20000", got.TwoSidedMillis)
	}
}

// Statistics enter the output as decimals like every other value, so they carry
// the same guarantee: no floating-point artifacts, ever.
func TestWindowValuesAreExactDecimals(t *testing.T) {
	tr := New("111", Options{})
	tr.ApplySnapshot(twoSided("0.111111111", "0.222222222", "1000"), at(0))

	got := tr.Finalize(at(time.Second)).Window

	if _, ok := decimal.Parse(got.SpreadTimeWeighted.Raw()).Nano(); !ok {
		t.Errorf("SpreadTimeWeighted = %q, which does not parse back", got.SpreadTimeWeighted.Raw())
	}
	if got.SpreadTimeWeighted.Raw() != "0.111111111" {
		t.Errorf("SpreadTimeWeighted = %q, want 0.111111111", got.SpreadTimeWeighted.Raw())
	}
	if got.MidHigh.Raw() != "0.1666666665" {
		t.Errorf("MidHigh = %q, want 0.1666666665", got.MidHigh.Raw())
	}
}
