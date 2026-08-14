package tracker

import (
	"testing"
	"time"

	"github.com/netqo/polymarket-scraper/internal/decimal"
	"github.com/netqo/polymarket-scraper/internal/wire"
)

// seedThenBreak names a way to get a token into a state where its book must not
// be reported, having first put a real book into it. If any of these leaked the
// pre-gap book, the consuming agent would see stale prices as current, which is
// the failure that costs real money.
var seedThenBreak = []struct {
	name    string
	breakIt func(*Tracker)
}{
	{
		name:    "connection dropped",
		breakIt: func(tr *Tracker) { tr.NoteDisconnect() },
	},
	{
		name:    "connection dropped and the re-seed failed",
		breakIt: func(tr *Tracker) { tr.NoteDisconnect(); tr.NoteResyncFailed() },
	},
	{
		name:    "a message could not be decoded",
		breakIt: func(tr *Tracker) { tr.NoteDecodeError() },
	},
	{
		name: "an update arrived far out of order",
		breakIt: func(tr *Tracker) {
			tr.ApplyChange(change("0.98", "1", wire.SideBuy, "h1"), "60000", at(time.Second))
			tr.ApplyChange(change("0.98", "2", wire.SideBuy, "h2"), "1000", at(2*time.Second))
		},
	},
	{
		name: "the published top of book disagreed, in strict mode",
		breakIt: func(tr *Tracker) {
			tr.ApplyBestBidAsk(wire.BestBidAsk{
				BestBid: decimal.Parse("0.10"),
				BestAsk: decimal.Parse("0.11"),
			}, at(time.Second))
		},
	},
}

// D2, stated as a property rather than as a set of examples: there is no
// sequence of events after which a book that is not current appears in the
// output. The status is what the consumer reads, and every status other than ok
// carries empty sides and no timestamps.
func TestABookThatIsNotCurrentIsNeverReported(t *testing.T) {
	for _, tt := range seedThenBreak {
		t.Run(tt.name, func(t *testing.T) {
			tr := New("111", Options{ReorderTolerance: time.Second, StrictBestBidAsk: true})
			tr.ApplySnapshot(snapshotAt("1000"), at(0))
			tr.ApplyChange(change("0.985", "500", wire.SideBuy, "seed"), "1001", at(time.Millisecond))

			if before := tr.Finalize(at(time.Second)); len(before.Bids) == 0 {
				t.Fatal("setup: the tracker had no book to leak in the first place")
			}

			tt.breakIt(tr)

			got := tr.Finalize(at(time.Minute))

			if got.Status == StatusOK {
				t.Fatalf("Status = %q after %s, want a failure status", got.Status, tt.name)
			}
			if len(got.Bids) != 0 || len(got.Asks) != 0 {
				t.Errorf("a book that is not current was reported: %d bids, %d asks", len(got.Bids), len(got.Asks))
			}
			if got.ExchangeTimestamp != "" {
				t.Errorf("ExchangeTimestamp = %q, want empty so it serializes as null", got.ExchangeTimestamp)
			}
			if !got.ReceivedAt.IsZero() {
				t.Errorf("ReceivedAt = %v, want the zero time", got.ReceivedAt)
			}
			if got.Source != SourceNone {
				t.Errorf("Source = %q, want empty: there is no trustworthy book to attribute", got.Source)
			}
		})
	}
}

// The flags are counts of what happened rather than market values, so they stay
// even when the book does not. Without them a failure would be unexplained.
func TestFailureStatusesStillExplainThemselves(t *testing.T) {
	tr := New("111", Options{})
	tr.ApplySnapshot(snapshotAt("1000"), at(0))
	tr.NoteDisconnect()
	tr.NoteResyncFailed()

	got := tr.Finalize(at(time.Minute))
	if len(got.Flags) == 0 {
		t.Fatal("a failed token reported no flags at all")
	}
	if got.TickSize.Absent() {
		t.Error("metadata learned before the failure was discarded along with the book")
	}
}

// Recovering after a gap must genuinely restore trust, or the resync machinery
// would be pointless: every disconnect would be terminal.
func TestTrustIsRestoredByReSeeding(t *testing.T) {
	reseeds := []struct {
		name   string
		reseed func(*Tracker)
		want   Source
	}{
		{"websocket snapshot", func(tr *Tracker) { tr.ApplySnapshot(snapshotAt("5000"), at(20*time.Second)) }, SourceWS},
		{"rest book", func(tr *Tracker) { tr.ApplyRESTBook(restBookAt("5000"), at(20*time.Second)) }, SourceWSResync},
	}

	for _, tt := range reseeds {
		t.Run(tt.name, func(t *testing.T) {
			tr := New("111", Options{})
			tr.ApplySnapshot(snapshotAt("1000"), at(0))
			tr.NoteDisconnect()

			tt.reseed(tr)

			got := tr.Finalize(at(time.Minute))
			if got.Status != StatusOK {
				t.Fatalf("Status = %q, want %q after re-seeding", got.Status, StatusOK)
			}
			if got.Source != tt.want {
				t.Errorf("Source = %q, want %q", got.Source, tt.want)
			}
			if len(got.Bids) == 0 {
				t.Error("the re-seeded book is empty")
			}
		})
	}
}

// Updates that arrive while a token is untrusted must not be applied: the base
// they would build on is the one already in doubt.
func TestUpdatesDuringDistrustAreNotApplied(t *testing.T) {
	tr := New("111", Options{})
	tr.ApplySnapshot(snapshotAt("1000"), at(0))
	tr.NoteDisconnect()

	tr.ApplyChange(change("0.5", "999", wire.SideBuy, "h9"), "2000", at(time.Second))
	tr.ApplyRESTBook(restBookAt("3000"), at(2*time.Second))

	got := tr.Finalize(at(time.Minute))
	if got.Status != StatusOK {
		t.Fatalf("Status = %q, want %q", got.Status, StatusOK)
	}
	for _, level := range got.Bids {
		if level.Price.Raw() == "0.5" {
			t.Error("an update received while the token was untrusted survived into the re-seeded book")
		}
	}
}
