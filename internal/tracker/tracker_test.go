// Test data: Invented books and updates. What is under test is the trust state machine, and
// the events that drive it -- a disconnect, a decode failure, an update far out of
// order -- are ones the real exchange will not produce on request.

package tracker

import (
	"slices"
	"testing"
	"time"

	"github.com/netqo/polymarket-scraper/internal/book"
	"github.com/netqo/polymarket-scraper/internal/decimal"
	"github.com/netqo/polymarket-scraper/internal/wire"
)

// A fixed origin, so every test reads as offsets from a known point.
var t0 = time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)

func at(offset time.Duration) time.Time { return t0.Add(offset) }

func lvl(price, size string) book.Level {
	return book.Level{Price: decimal.Parse(price), Size: decimal.Parse(size)}
}

// snapshotAt builds a websocket snapshot with a two-sided book.
func snapshotAt(millis string) wire.Book {
	return wire.Book{
		Market:    "0xmarket",
		AssetID:   "111",
		Timestamp: millis,
		Bids:      []book.Level{lvl("0.97", "100")},
		Asks:      []book.Level{lvl("0.99", "100")},
		TickSize:  decimal.Parse("0.001"),
	}
}

func restBookAt(millis string) wire.RESTBook {
	negRisk := true

	return wire.RESTBook{
		Market:       "0xmarket",
		AssetID:      "111",
		Timestamp:    millis,
		Bids:         []book.Level{lvl("0.96", "50")},
		Asks:         []book.Level{lvl("0.98", "50")},
		TickSize:     decimal.Parse("0.001"),
		MinOrderSize: decimal.Parse("5"),
		NegRisk:      &negRisk,
	}
}

func change(price, size, side, hash string) wire.PriceChangeEntry {
	return wire.PriceChangeEntry{
		AssetID: "111",
		Price:   decimal.Parse(price),
		Size:    decimal.Parse(size),
		Side:    side,
		Hash:    hash,
	}
}

// newLive returns a tracker that has been seeded and is trusted.
func newLive(t *testing.T, opts Options) *Tracker {
	t.Helper()

	tr := New("111", opts)
	tr.ApplySnapshot(snapshotAt("1000"), at(0))
	if tr.State() != StateLive {
		t.Fatalf("setup: state = %v, want live", tr.State())
	}

	return tr
}

func TestNewStartsPendingWithNothingToReport(t *testing.T) {
	tr := New("111", Options{})

	if tr.State() != StatePending {
		t.Errorf("State() = %v, want pending", tr.State())
	}

	got := tr.Finalize(at(time.Minute))
	if got.Status != StatusNoData {
		t.Errorf("Status = %q, want %q", got.Status, StatusNoData)
	}
	if got.Source != SourceNone {
		t.Errorf("Source = %q, want empty", got.Source)
	}
	if got.TokenID != "111" {
		t.Errorf("TokenID = %q, want 111", got.TokenID)
	}
}

func TestSnapshotMakesTheTokenLive(t *testing.T) {
	tr := New("111", Options{})

	if effect := tr.ApplySnapshot(snapshotAt("1000"), at(time.Second)); effect != EffectNone {
		t.Errorf("effect = %v, want none", effect)
	}

	got := tr.Finalize(at(time.Minute))
	if got.Status != StatusOK {
		t.Fatalf("Status = %q, want %q", got.Status, StatusOK)
	}
	if got.Source != SourceWS {
		t.Errorf("Source = %q, want %q", got.Source, SourceWS)
	}
	if got.ConditionID != "0xmarket" {
		t.Errorf("ConditionID = %q", got.ConditionID)
	}
	if got.TickSize.Raw() != "0.001" {
		t.Errorf("TickSize = %q, want 0.001", got.TickSize.Raw())
	}
	if got.ExchangeTimestamp != "1000" {
		t.Errorf("ExchangeTimestamp = %q, want the feed value verbatim", got.ExchangeTimestamp)
	}
	if !got.ReceivedAt.Equal(at(time.Second)) {
		t.Errorf("ReceivedAt = %v, want %v", got.ReceivedAt, at(time.Second))
	}
}

// B3: an incremental update against nothing produces a book that looks real and
// is not, so deltas before the first snapshot are discarded.
func TestDeltasBeforeAnySnapshotAreDropped(t *testing.T) {
	tr := New("111", Options{})

	if effect := tr.ApplyChange(change("0.98", "10", wire.SideBuy, "h1"), "1000", at(0)); effect != EffectNone {
		t.Errorf("effect = %v, want none", effect)
	}

	got := tr.Finalize(at(time.Minute))
	if got.Status != StatusNoData {
		t.Errorf("Status = %q, want %q: a dropped delta is not data", got.Status, StatusNoData)
	}
	if !slices.Contains(got.Flags, FlagPreSnapshotDeltaDropped) {
		t.Errorf("flags = %v, want %q", got.Flags, FlagPreSnapshotDeltaDropped)
	}
	if got.UpdatesApplied != 0 {
		t.Errorf("UpdatesApplied = %d, want 0", got.UpdatesApplied)
	}
}

func TestDeltasUpdateTheBook(t *testing.T) {
	tr := newLive(t, Options{})

	tr.ApplyChange(change("0.98", "25", wire.SideBuy, "h1"), "1001", at(time.Second))
	tr.ApplyChange(change("0.995", "40", wire.SideSell, "h2"), "1002", at(2*time.Second))

	got := tr.Finalize(at(time.Minute))
	if got.UpdatesApplied != 2 {
		t.Fatalf("UpdatesApplied = %d, want 2", got.UpdatesApplied)
	}
	if len(got.Bids) != 2 || got.Bids[0].Price.Raw() != "0.98" {
		t.Errorf("bids = %v, want the new level on top", got.Bids)
	}
	if got.ExchangeTimestamp != "1002" {
		t.Errorf("ExchangeTimestamp = %q, want 1002", got.ExchangeTimestamp)
	}
	if slices.Contains(got.Flags, FlagSnapshotOnly) {
		t.Error("snapshot_only was flagged despite deltas having been applied")
	}
}

func TestSnapshotWithoutDeltasIsFlagged(t *testing.T) {
	tr := newLive(t, Options{})

	got := tr.Finalize(at(time.Minute))
	if !slices.Contains(got.Flags, FlagSnapshotOnly) {
		t.Errorf("flags = %v, want %q", got.Flags, FlagSnapshotOnly)
	}
}

func TestUnknownSideIsNotApplied(t *testing.T) {
	tr := newLive(t, Options{})

	tr.ApplyChange(change("0.98", "25", "SIDEWAYS", "h1"), "1001", at(time.Second))

	got := tr.Finalize(at(time.Minute))
	if got.UpdatesApplied != 0 {
		t.Errorf("UpdatesApplied = %d, want 0", got.UpdatesApplied)
	}
	if !slices.Contains(got.Flags, FlagUnknownSide) {
		t.Errorf("flags = %v, want %q", got.Flags, FlagUnknownSide)
	}
}

func TestDuplicateDeltasAreDropped(t *testing.T) {
	tr := newLive(t, Options{})

	entry := change("0.98", "25", wire.SideBuy, "same-hash")
	tr.ApplyChange(entry, "1001", at(time.Second))
	tr.ApplyChange(entry, "1001", at(2*time.Second))

	got := tr.Finalize(at(time.Minute))
	if got.UpdatesApplied != 1 {
		t.Errorf("UpdatesApplied = %d, want 1", got.UpdatesApplied)
	}
	if !slices.Contains(got.Flags, FlagDuplicateDeltaDropped) {
		t.Errorf("flags = %v, want %q", got.Flags, FlagDuplicateDeltaDropped)
	}
}

// The feed has no sequence numbers. A small regression is a clock artifact and
// is noted; a large one is evidence that something was missed. Treating every
// regression as a gap would resync every token at once over one server hiccup.
func TestTimestampRegressionBelowToleranceIsFlaggedNotDistrusted(t *testing.T) {
	tr := newLive(t, Options{ReorderTolerance: 5 * time.Second})

	tr.ApplyChange(change("0.98", "25", wire.SideBuy, "h1"), "10000", at(time.Second))
	effect := tr.ApplyChange(change("0.981", "26", wire.SideBuy, "h2"), "8000", at(2*time.Second))

	if effect != EffectNone {
		t.Errorf("effect = %v, want none for a 2s regression under a 5s tolerance", effect)
	}

	got := tr.Finalize(at(time.Minute))
	if got.Status != StatusOK {
		t.Errorf("Status = %q, want %q", got.Status, StatusOK)
	}
	if !slices.Contains(got.Flags, FlagTimestampRegression) {
		t.Errorf("flags = %v, want %q", got.Flags, FlagTimestampRegression)
	}
	if got.UpdatesApplied != 2 {
		t.Errorf("UpdatesApplied = %d, want both updates applied", got.UpdatesApplied)
	}
}

func TestTimestampRegressionBeyondToleranceDistrustsTheToken(t *testing.T) {
	tr := newLive(t, Options{ReorderTolerance: time.Second})

	tr.ApplyChange(change("0.98", "25", wire.SideBuy, "h1"), "20000", at(time.Second))
	effect := tr.ApplyChange(change("0.981", "26", wire.SideBuy, "h2"), "10000", at(2*time.Second))

	if effect != EffectRequestResync {
		t.Fatalf("effect = %v, want a resync request", effect)
	}
	if tr.State() != StateResyncing {
		t.Errorf("State() = %v, want resyncing", tr.State())
	}

	got := tr.Finalize(at(time.Minute))
	if got.Status != StatusResyncFailed {
		t.Errorf("Status = %q, want %q", got.Status, StatusResyncFailed)
	}
	if !slices.Contains(got.Flags, FlagDeltaGap) {
		t.Errorf("flags = %v, want %q", got.Flags, FlagDeltaGap)
	}
}

// B4, and the reason the whole project exists. Missed updates are never
// replayed, so a book that lived through a disconnect is no longer known to be
// correct, whatever it looks like.
func TestDisconnectDistrustsALiveToken(t *testing.T) {
	tr := newLive(t, Options{})

	if effect := tr.NoteDisconnect(); effect != EffectRequestResync {
		t.Fatalf("effect = %v, want a resync request", effect)
	}
	if tr.State() != StateResyncing {
		t.Errorf("State() = %v, want resyncing", tr.State())
	}
}

func TestDisconnectOnAnUntrustedTokenAsksForNothingNew(t *testing.T) {
	tr := New("111", Options{})

	if effect := tr.NoteDisconnect(); effect != EffectNone {
		t.Errorf("effect = %v, want none for a token that was never seeded", effect)
	}
	if tr.State() != StatePending {
		t.Errorf("State() = %v, want pending", tr.State())
	}

	live := newLive(t, Options{})
	live.NoteDisconnect()
	if effect := live.NoteDisconnect(); effect != EffectNone {
		t.Errorf("effect = %v, want none: a resync is already in flight", effect)
	}
}

func TestRecoveryFromAGapViaWebsocketStaysAttributedToTheWebsocket(t *testing.T) {
	tr := newLive(t, Options{})
	tr.NoteDisconnect()

	tr.ApplySnapshot(snapshotAt("2000"), at(10*time.Second))

	got := tr.Finalize(at(time.Minute))
	if got.Status != StatusOK {
		t.Fatalf("Status = %q, want %q", got.Status, StatusOK)
	}
	if got.Source != SourceWS {
		t.Errorf("Source = %q, want %q: the recovery came from the websocket", got.Source, SourceWS)
	}
	if !slices.Contains(got.Flags, FlagDeltaGapResynced) {
		t.Errorf("flags = %v, want %q so the gap is still visible", got.Flags, FlagDeltaGapResynced)
	}
	if !slices.Contains(got.Flags, FlagDisconnected) {
		t.Errorf("flags = %v, want %q", got.Flags, FlagDisconnected)
	}
}

func TestRecoveryFromAGapViaRESTIsAttributedToREST(t *testing.T) {
	tr := newLive(t, Options{})
	tr.NoteDisconnect()

	tr.ApplyRESTBook(restBookAt("2000"), at(10*time.Second))

	got := tr.Finalize(at(time.Minute))
	if got.Status != StatusOK {
		t.Fatalf("Status = %q, want %q", got.Status, StatusOK)
	}
	if got.Source != SourceWSResync {
		t.Errorf("Source = %q, want %q", got.Source, SourceWSResync)
	}
	if len(got.Bids) != 1 || got.Bids[0].Price.Raw() != "0.96" {
		t.Errorf("bids = %v, want the re-seeded book", got.Bids)
	}
}

// The metadata the websocket never sends has to come from somewhere, and taking
// it must not disturb a book that is already current.
func TestRESTSeedOnALiveTokenTakesMetadataOnly(t *testing.T) {
	tr := newLive(t, Options{})

	tr.ApplyRESTBook(restBookAt("2000"), at(10*time.Second))

	got := tr.Finalize(at(time.Minute))
	if got.Source != SourceWS {
		t.Errorf("Source = %q, want %q: the live book was not replaced", got.Source, SourceWS)
	}
	if len(got.Bids) != 1 || got.Bids[0].Price.Raw() != "0.97" {
		t.Errorf("bids = %v, want the websocket book untouched", got.Bids)
	}
	if got.MinOrderSize.Raw() != "5" {
		t.Errorf("MinOrderSize = %q, want it taken from REST", got.MinOrderSize.Raw())
	}
	if got.NegRisk == nil || !*got.NegRisk {
		t.Errorf("NegRisk = %v, want it taken from REST", got.NegRisk)
	}
}

func TestRESTSeedOnAPendingTokenMakesItLive(t *testing.T) {
	tr := New("111", Options{})

	tr.ApplyRESTBook(restBookAt("2000"), at(5*time.Second))

	got := tr.Finalize(at(time.Minute))
	if got.Status != StatusOK {
		t.Fatalf("Status = %q, want %q", got.Status, StatusOK)
	}
	if got.Source != SourceWSResync {
		t.Errorf("Source = %q, want %q", got.Source, SourceWSResync)
	}
	if slices.Contains(got.Flags, FlagDeltaGapResynced) {
		t.Error("a first seed was reported as a gap recovery")
	}
}

func TestRESTOnlyRunsReportTheirSource(t *testing.T) {
	tr := New("111", Options{RESTOnly: true})

	tr.ApplyRESTBook(restBookAt("2000"), at(time.Second))

	got := tr.Finalize(at(time.Minute))
	if got.Source != SourceRESTOnly {
		t.Errorf("Source = %q, want %q", got.Source, SourceRESTOnly)
	}
}

func TestSubscribeFailureIsReportedAsItself(t *testing.T) {
	tr := New("111", Options{})
	tr.NoteSubscribeFailed()

	got := tr.Finalize(at(time.Minute))
	if got.Status != StatusSubscribeFailed {
		t.Errorf("Status = %q, want %q", got.Status, StatusSubscribeFailed)
	}
	if got.Source != SourceNone {
		t.Errorf("Source = %q, want empty", got.Source)
	}
}

func TestResyncFailureIsTerminal(t *testing.T) {
	tr := newLive(t, Options{})
	tr.NoteDisconnect()
	tr.NoteResyncFailed()

	if tr.State() != StateFailed {
		t.Fatalf("State() = %v, want failed", tr.State())
	}

	got := tr.Finalize(at(time.Minute))
	if got.Status != StatusResyncFailed {
		t.Errorf("Status = %q, want %q", got.Status, StatusResyncFailed)
	}
}

func TestSweepAsksForABookOnlyForTokensThatNeverGotOne(t *testing.T) {
	pending := New("111", Options{})
	if effect := pending.Sweep(); effect != EffectRequestResync {
		t.Errorf("effect on a pending token = %v, want a resync request", effect)
	}

	live := newLive(t, Options{})
	if effect := live.Sweep(); effect != EffectNone {
		t.Errorf("effect on a live token = %v, want none", effect)
	}
}

func TestTickSizeChangeIsRecordedAndFlagged(t *testing.T) {
	tr := newLive(t, Options{})

	tr.ApplyTickSize(wire.TickSizeChange{
		OldTickSize: decimal.Parse("0.01"),
		NewTickSize: decimal.Parse("0.001"),
	}, at(time.Second))

	got := tr.Finalize(at(time.Minute))
	if got.TickSize.Raw() != "0.001" {
		t.Errorf("TickSize = %q, want 0.001", got.TickSize.Raw())
	}
	if !slices.Contains(got.Flags, FlagTickSizeChanged) {
		t.Errorf("flags = %v, want %q", got.Flags, FlagTickSizeChanged)
	}
}

// A tick size change for a token we have never seeded says the market is live
// and we are not.
func TestTickSizeChangeWhilePendingAsksForABook(t *testing.T) {
	tr := New("111", Options{})

	effect := tr.ApplyTickSize(wire.TickSizeChange{NewTickSize: decimal.Parse("0.001")}, at(time.Second))
	if effect != EffectRequestResync {
		t.Errorf("effect = %v, want a resync request", effect)
	}
}

func TestBestBidAskAgreementIsSilent(t *testing.T) {
	tr := newLive(t, Options{})

	effect := tr.ApplyBestBidAsk(wire.BestBidAsk{
		BestBid: decimal.Parse("0.970"),
		BestAsk: decimal.Parse("0.99"),
	}, at(time.Second))

	if effect != EffectNone {
		t.Errorf("effect = %v, want none", effect)
	}

	got := tr.Finalize(at(time.Minute))
	if slices.Contains(got.Flags, FlagBestBidAskMismatch) {
		t.Errorf("flags = %v, want no mismatch: 0.970 and 0.97 are the same price", got.Flags)
	}
}

func TestBestBidAskMismatchIsFlaggedAndOptionallyFatal(t *testing.T) {
	quote := wire.BestBidAsk{
		BestBid: decimal.Parse("0.50"),
		BestAsk: decimal.Parse("0.51"),
	}

	lenient := newLive(t, Options{})
	if effect := lenient.ApplyBestBidAsk(quote, at(time.Second)); effect != EffectNone {
		t.Errorf("lenient effect = %v, want none", effect)
	}
	if got := lenient.Finalize(at(time.Minute)); !slices.Contains(got.Flags, FlagBestBidAskMismatch) {
		t.Errorf("lenient flags = %v, want %q", got.Flags, FlagBestBidAskMismatch)
	}
	if lenient.State() != StateLive {
		t.Errorf("lenient state = %v, want live", lenient.State())
	}

	strict := newLive(t, Options{StrictBestBidAsk: true})
	if effect := strict.ApplyBestBidAsk(quote, at(time.Second)); effect != EffectRequestResync {
		t.Errorf("strict effect = %v, want a resync request", effect)
	}
}

func TestTradesAreRecordedVerbatim(t *testing.T) {
	tr := newLive(t, Options{})

	tr.ApplyTrade(wire.LastTrade{
		Market:     "0xmarket",
		Price:      decimal.Parse("0.980"),
		Size:       decimal.Parse("120"),
		Side:       wire.SideBuy,
		FeeRateBPS: decimal.Parse("0"),
		Timestamp:  "1500",
	}, at(time.Second))

	got := tr.Finalize(at(time.Minute))
	if got.LastTrade == nil {
		t.Fatal("LastTrade is nil")
	}
	if got.LastTrade.Price.Raw() != "0.980" {
		t.Errorf("price = %q, want the feed spelling 0.980", got.LastTrade.Price.Raw())
	}
	if got.LastTrade.FeeRateBPS.Raw() != "0" {
		t.Errorf("fee rate = %q, want 0", got.LastTrade.FeeRateBPS.Raw())
	}
}

func TestCrossedBookIsFlaggedNotRepaired(t *testing.T) {
	tr := New("111", Options{})
	tr.ApplySnapshot(wire.Book{
		Market:    "0xmarket",
		Timestamp: "1000",
		Bids:      []book.Level{lvl("0.99", "100")},
		Asks:      []book.Level{lvl("0.97", "100")},
	}, at(0))

	got := tr.Finalize(at(time.Minute))
	if !slices.Contains(got.Flags, FlagCrossedBook) {
		t.Errorf("flags = %v, want %q", got.Flags, FlagCrossedBook)
	}
	if len(got.Bids) != 1 || len(got.Asks) != 1 {
		t.Error("the crossed book was repaired instead of reported")
	}
}

func TestUnparsablePriceIsFlaggedAndStillReported(t *testing.T) {
	tr := New("111", Options{})
	tr.ApplySnapshot(wire.Book{
		Market:    "0xmarket",
		Timestamp: "1000",
		Bids:      []book.Level{lvl("0.97", "100"), lvl("not-a-price", "1")},
	}, at(0))

	got := tr.Finalize(at(time.Minute))
	if !slices.Contains(got.Flags, FlagUnparsablePrice) {
		t.Errorf("flags = %v, want %q", got.Flags, FlagUnparsablePrice)
	}
	if len(got.Bids) != 2 {
		t.Errorf("got %d bids, want the unparsable level reported too", len(got.Bids))
	}
}

func TestDiscoveredTokensAreFlaggedFromTheStart(t *testing.T) {
	tr := New("111", Options{Discovered: true})

	got := tr.Finalize(at(time.Minute))
	if !slices.Contains(got.Flags, FlagDiscoveredMidWindow) {
		t.Errorf("flags = %v, want %q", got.Flags, FlagDiscoveredMidWindow)
	}
}

// A run has to be able to report trouble while it is happening, not only in the
// document it writes at the end.
func TestOnFlagReportsEachFlagOnceAsItIsRaised(t *testing.T) {
	type raised struct {
		tokenID string
		flag    Flag
	}

	var seen []raised
	tr := New("111", Options{
		Observer: recorder{onFlag: func(tokenID string, flag Flag) {
			seen = append(seen, raised{tokenID, flag})
		}},
	})

	tr.ApplySnapshot(snapshotAt("1000"), at(0))

	// Three disconnects, but the token is only newly disconnected once.
	tr.NoteDisconnect()
	tr.NoteDisconnect()
	tr.NoteDisconnect()

	tr.NoteDecodeError()

	want := []raised{
		{"111", FlagDisconnected},
		{"111", FlagDecodeError},
	}
	if len(seen) != len(want) {
		t.Fatalf("observer saw %v, want %v", seen, want)
	}
	for i, got := range seen {
		if got != want[i] {
			t.Errorf("observation %d = %+v, want %+v", i, got, want[i])
		}
	}
}

// The observer is told about a flag exactly when the document would carry it,
// so the two can never disagree about what happened.
func TestOnFlagMatchesTheFlagsInTheSnapshot(t *testing.T) {
	var seen []Flag
	tr := New("111", Options{
		Discovered: true,
		Observer:   recorder{onFlag: func(_ string, flag Flag) { seen = append(seen, flag) }},
	})

	tr.ApplySnapshot(snapshotAt("1000"), at(0))
	tr.NoteDisconnect()
	tr.NoteMarketResolved()

	got := tr.Finalize(at(time.Minute))
	if len(seen) != len(got.Flags) {
		t.Fatalf("observer saw %v but the snapshot carries %v", seen, got.Flags)
	}
	for i, flag := range got.Flags {
		if seen[i] != flag {
			t.Errorf("observation %d = %q, snapshot has %q", i, seen[i], flag)
		}
	}
}

func TestMissingSnapshotIsCompleteAndEmpty(t *testing.T) {
	got := Missing("999")

	if got.TokenID != "999" || got.Status != StatusNoData {
		t.Errorf("Missing = %+v", got)
	}
	if got.Bids == nil || got.Asks == nil || got.Flags == nil {
		t.Error("Missing left a nil slice, which would serialize as null rather than []")
	}
}

// recorder is an Observer built from whichever callbacks a test cares about.
// The unset ones do nothing, so a test about flags says nothing about quotes.
type recorder struct {
	onFlag  func(tokenID string, flag Flag)
	onQuote func(tokenID string, quote Quote)
	onTrade func(tokenID string, trade LastTrade)
}

func (r recorder) Flagged(tokenID string, flag Flag) {
	if r.onFlag != nil {
		r.onFlag(tokenID, flag)
	}
}

func (r recorder) Quoted(tokenID string, quote Quote) {
	if r.onQuote != nil {
		r.onQuote(tokenID, quote)
	}
}

func (r recorder) Traded(tokenID string, trade LastTrade) {
	if r.onTrade != nil {
		r.onTrade(tokenID, trade)
	}
}
