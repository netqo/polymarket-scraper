package tracker

import (
	"slices"
	"strconv"
	"time"

	"github.com/netqo/polymarket-scraper/internal/book"
	"github.com/netqo/polymarket-scraper/internal/decimal"
	"github.com/netqo/polymarket-scraper/internal/wire"
)

// Options configure one token's tracker.
type Options struct {
	// ReorderTolerance is how far a timestamp may go backwards before the book
	// is distrusted rather than merely flagged. The feed carries no sequence
	// numbers, only timestamps, so treating every regression as a missed update
	// would turn one server clock quirk into a resync of every token at once.
	ReorderTolerance time.Duration

	// StrictBestBidAsk turns a disagreement between the published top of book
	// and the maintained one into a re-seed instead of a flag.
	StrictBestBidAsk bool

	// RESTOnly records that this run never opens a websocket, which changes how
	// the reported source is described.
	RESTOnly bool

	// Discovered records that the token was picked up from an announcement
	// during the run rather than requested.
	Discovered bool

	// Observer is told about this token's changes as they happen, so that a run
	// can be watched while it is running rather than only read once it has
	// finished.
	//
	// An interface rather than a field of function values because the three
	// things worth reporting are one concern: they are wanted together or not
	// at all, and a caller that implements one implements all of them. It also
	// keeps this package free of I/O and of any knowledge of where the changes
	// go, which is what lets the trust rules be verified with no clock, no
	// network and no concurrency in the way.
	//
	// Its methods run on whichever goroutine owns the tracker, so none of them
	// may block. Nil disables reporting.
	Observer Observer
}

// Tracker holds the state of one token.
//
// Every method takes the time at which the event was received rather than
// reading a clock, so the whole type is a pure function of its inputs and the
// window statistics can be exercised with no sleeping.
type Tracker struct {
	tokenID string
	opts    Options

	state State
	book  book.Book
	flags []Flag

	conditionID  string
	tickSize     decimal.Dec
	minOrderSize decimal.Dec
	negRisk      *bool

	exchangeTimestamp string
	receivedAt        time.Time

	updatesApplied int
	lastTrade      *LastTrade

	// source records where the reported book came from, set when it is seeded
	// rather than derived afterwards from a flag and the run's mode. The two
	// spellings of the same fact were free to disagree; this one cannot.
	source Source

	lastAppliedMillis int64
	haveLastMillis    bool
	lastDeltaHash     string

	// lastQuote is the top of book most recently reported to the observer, so
	// that an update leaving it where it was reports nothing.
	lastQuote Quote
	haveQuote bool

	window window
}

// New builds a tracker for a token.
func New(tokenID string, opts Options) *Tracker {
	t := &Tracker{tokenID: tokenID, opts: opts, state: StatePending}
	if opts.Discovered {
		t.flag(FlagDiscoveredMidWindow)
	}

	return t
}

// TokenID reports which token this tracker follows.
func (t *Tracker) TokenID() string { return t.tokenID }

// State reports the current lifecycle state. It exists for logging and tests;
// the output document reports a Status, which is a coarser thing.
func (t *Tracker) State() State { return t.state }

// ApplySnapshot seeds the book from a websocket snapshot.
//
// A snapshot always restores trust: it is a complete picture, so whatever was
// missed before it no longer matters.
func (t *Tracker) ApplySnapshot(snapshot wire.Book, at time.Time) Effect {
	if !t.opts.RESTOnly && !t.state.trusted() && t.state != StatePending {
		t.flag(FlagDeltaGapResynced)
	}

	t.book.Replace(book.Bids, snapshot.Bids)
	t.book.Replace(book.Asks, snapshot.Asks)
	t.notePriceParsing(snapshot.Bids, snapshot.Asks)

	if snapshot.Market != "" {
		t.conditionID = snapshot.Market
	}
	if !snapshot.TickSize.Absent() {
		t.tickSize = snapshot.TickSize
	}

	t.source = SourceWS
	t.state = StateLive
	t.noteFreshness(snapshot.Timestamp, at)
	t.window.sample(&t.book, at)
	t.noteQuote()

	return EffectNone
}

// ApplyRESTBook applies a book fetched over REST.
//
// The metadata is always taken, because minimum order size and the negative
// risk flag never appear on the websocket at all and the output document
// requires both. The levels are taken only when the token is not already
// trusted: overwriting a live book with a separately fetched one would replace
// current data with data of unknown relative age for no benefit.
func (t *Tracker) ApplyRESTBook(fetched wire.RESTBook, at time.Time) Effect {
	if fetched.Market != "" {
		t.conditionID = fetched.Market
	}
	if !fetched.TickSize.Absent() {
		t.tickSize = fetched.TickSize
	}
	if !fetched.MinOrderSize.Absent() {
		t.minOrderSize = fetched.MinOrderSize
	}
	if fetched.NegRisk != nil {
		t.negRisk = fetched.NegRisk
	}

	if t.state.trusted() {
		return EffectNone
	}

	if t.state == StateResyncing || t.state == StateFailed {
		t.flag(FlagDeltaGapResynced)
	}

	t.book.Replace(book.Bids, fetched.Bids)
	t.book.Replace(book.Asks, fetched.Asks)
	t.notePriceParsing(fetched.Bids, fetched.Asks)

	// A rest-only run has no websocket to attribute anything to; every other
	// run reaching here is recovering a token the websocket could not.
	t.source = SourceWSResync
	if t.opts.RESTOnly {
		t.source = SourceRESTOnly
	}
	t.state = StateLive
	t.noteFreshness(fetched.Timestamp, at)
	t.window.sample(&t.book, at)
	t.noteQuote()

	return EffectNone
}

// ApplyChange applies one incremental update.
//
// A delta that arrives before any snapshot is discarded rather than applied to
// an empty book: an incremental update against nothing produces a book that
// looks real and is not. A delta that arrives while the token is untrusted is
// discarded for the same reason, since the base it would build on is the one
// already in doubt.
func (t *Tracker) ApplyChange(entry wire.PriceChangeEntry, timestamp string, at time.Time) Effect {
	if t.state == StatePending {
		t.flag(FlagPreSnapshotDeltaDropped)
		return EffectNone
	}
	if !t.state.trusted() {
		return EffectNone
	}

	if entry.Hash != "" && entry.Hash == t.lastDeltaHash {
		t.flag(FlagDuplicateDeltaDropped)
		return EffectNone
	}

	if effect, ok := t.checkOrdering(timestamp); !ok {
		return effect
	}

	side, known := entry.BookSide()
	if !known {
		t.flag(FlagUnknownSide)
		return EffectNone
	}

	level := entry.Level()
	t.book.Apply(side, level)
	t.notePriceParsing([]book.Level{level})

	t.lastDeltaHash = entry.Hash
	t.updatesApplied++
	t.window.updates++

	t.noteFreshness(timestamp, at)
	t.window.sample(&t.book, at)
	t.noteQuote()

	return EffectNone
}

// checkOrdering decides whether an update may be applied given its timestamp.
//
// Delivery order on a single connection is the ordering authority, because the
// feed has no sequence numbers. A timestamp that goes backwards by less than
// the tolerance is a clock artifact and is noted; one that goes back further is
// treated as evidence that something was missed.
func (t *Tracker) checkOrdering(timestamp string) (Effect, bool) {
	millis, parsed := parseMillis(timestamp)
	if !parsed || !t.haveLastMillis || millis >= t.lastAppliedMillis {
		return EffectNone, true
	}

	regression := time.Duration(t.lastAppliedMillis-millis) * time.Millisecond
	if regression <= t.opts.ReorderTolerance {
		t.flag(FlagTimestampRegression)
		return EffectNone, true
	}

	t.flag(FlagDeltaGap)

	return t.distrust(), false
}

// ApplyTickSize records a change to the minimum price increment.
//
// A tick size change for a token that has never been seeded says the market is
// live and we are not, so it is treated as a reason to fetch the book rather
// than to keep waiting.
func (t *Tracker) ApplyTickSize(change wire.TickSizeChange, _ time.Time) Effect {
	if !change.NewTickSize.Absent() {
		t.tickSize = change.NewTickSize
	}
	t.flag(FlagTickSizeChanged)

	if t.state == StatePending {
		return t.requestSeed()
	}

	return EffectNone
}

// ApplyBestBidAsk cross-checks the published top of book against the maintained
// one.
//
// There are two independent answers to the same question here. The scraper
// computes its own best bid and ask from the snapshot it was given plus every
// delta since; the exchange separately publishes what it thinks they are. When
// the two disagree, the local book has diverged from the exchange's, which in
// practice means a delta was missed or misapplied. That is worth knowing about,
// because a book built on a missed update stays plausible while being wrong.
//
// The published quote is never used as the source of the book, because it is
// not guaranteed to be delivered and correctness must not depend on an optional
// message. By default a disagreement raises a flag and nothing more;
// StrictBestBidAsk promotes it to a full re-seed.
func (t *Tracker) ApplyBestBidAsk(quote wire.BestBidAsk, _ time.Time) Effect {
	if !t.state.trusted() {
		return EffectNone
	}
	if t.topOfBookAgrees(quote) {
		return EffectNone
	}

	t.flag(FlagBestBidAskMismatch)
	if !t.opts.StrictBestBidAsk {
		return EffectNone
	}

	return t.distrust()
}

// topOfBookAgrees reports whether a published quote matches the maintained book.
// A side the feed did not quote is not evidence of disagreement.
func (t *Tracker) topOfBookAgrees(quote wire.BestBidAsk) bool {
	return t.sideAgrees(book.Bids, quote.BestBid) && t.sideAgrees(book.Asks, quote.BestAsk)
}

func (t *Tracker) sideAgrees(side book.Side, quoted decimal.Dec) bool {
	if quoted.Absent() {
		return true
	}

	best, have := t.book.Best(side)
	if !have {
		return false
	}

	return decimal.Cmp(best.Price, quoted) == 0
}

// ApplyTrade records the most recent fill.
func (t *Tracker) ApplyTrade(trade wire.LastTrade, _ time.Time) Effect {
	if trade.Market != "" {
		t.conditionID = trade.Market
	}
	fill := LastTrade{
		Price:      trade.Price,
		Size:       trade.Size,
		Side:       trade.Side,
		FeeRateBPS: trade.FeeRateBPS,
		Timestamp:  trade.Timestamp,
	}
	t.lastTrade = &fill

	if t.opts.Observer != nil {
		t.opts.Observer.Traded(t.tokenID, fill)
	}

	return EffectNone
}

// NoteDisconnect records that the connection carrying this token dropped.
//
// This is the case requirement B4 exists for. Missed updates are never
// replayed, so a book that was live across a disconnect is no longer known to
// be correct, whatever it looks like.
func (t *Tracker) NoteDisconnect() Effect {
	t.flag(FlagDisconnected)

	if !t.state.trusted() {
		return EffectNone
	}

	return t.distrust()
}

// NoteDecodeError records that a message for this token could not be decoded,
// which means an update was lost.
func (t *Tracker) NoteDecodeError() Effect {
	t.flag(FlagDecodeError)

	if !t.state.trusted() {
		return EffectNone
	}

	return t.distrust()
}

// NoteSubscribeFailed records that no subscription was ever established.
func (t *Tracker) NoteSubscribeFailed() Effect {
	t.state = StateSubscribeFailed

	return EffectNone
}

// NoteResyncFailed records that re-seeding was attempted and did not succeed.
// The token stays untrusted, and its pre-gap book is not reported.
func (t *Tracker) NoteResyncFailed() Effect {
	if t.state == StateResyncing {
		t.state = StateFailed
	}

	return EffectNone
}

// NoteTokenNotFound records that the exchange does not recognise this token id.
func (t *Tracker) NoteTokenNotFound() Effect {
	t.flag(FlagTokenNotFound)

	return EffectNone
}

// NoteMarketResolved records that the market settled during the window.
func (t *Tracker) NoteMarketResolved() Effect {
	t.flag(FlagMarketResolved)

	return EffectNone
}

// Sweep gives a token that never received a book one last chance before the
// window closes.
func (t *Tracker) Sweep() Effect {
	if t.state != StatePending {
		return EffectNone
	}

	return t.requestSeed()
}

// distrust moves a trusted token out of trust and asks for a re-seed.
func (t *Tracker) distrust() Effect {
	t.state = StateResyncing

	return EffectRequestResync
}

// requestSeed asks for a book for a token that has never had one.
func (t *Tracker) requestSeed() Effect {
	t.state = StateResyncing

	return EffectRequestResync
}

// Finalize freezes the tracker into the value the report writes out.
//
// The guarantee that a non-current book is never reported is enforced here
// structurally: the branch that copies levels is reachable only when the status
// is ok, so a stale book is not merely left out of the output, there is no path
// that could put it there.
func (t *Tracker) Finalize(at time.Time) Snapshot {
	status := t.status()

	snapshot := Snapshot{
		TokenID:      t.tokenID,
		Status:       status,
		Source:       t.attribution(status),
		ConditionID:  t.conditionID,
		Bids:         []book.Level{},
		Asks:         []book.Level{},
		TickSize:     t.tickSize,
		MinOrderSize: t.minOrderSize,
		NegRisk:      t.negRisk,
		LastTrade:    t.lastTrade,
		Window:       t.window.summarize(at),
	}

	if status == StatusOK {
		snapshot.Bids = t.book.Levels(book.Bids, 0)
		snapshot.Asks = t.book.Levels(book.Asks, 0)
		snapshot.ExchangeTimestamp = t.exchangeTimestamp
		snapshot.ReceivedAt = t.receivedAt
		snapshot.UpdatesApplied = t.updatesApplied

		if t.book.Crossed() {
			t.flag(FlagCrossedBook)
		}
		if t.updatesApplied == 0 {
			t.flag(FlagSnapshotOnly)
		}
	}

	snapshot.Flags = slices.Clone(t.flags)
	if snapshot.Flags == nil {
		snapshot.Flags = []Flag{}
	}

	return snapshot
}

// status maps the lifecycle state onto the output document's closed set.
func (t *Tracker) status() Status {
	switch t.state {
	case StateLive:
		return StatusOK
	case StateSubscribeFailed:
		return StatusSubscribeFailed
	case StateResyncing, StateFailed:
		return StatusResyncFailed
	case StatePending:
		return StatusNoData
	default:
		return StatusNoData
	}
}

// source describes where a trusted book came from, and says nothing when there
// is no trusted book to describe.
func (t *Tracker) attribution(status Status) Source {
	if status != StatusOK {
		return SourceNone
	}

	return t.source
}

// noteFreshness records both clocks for the most recent update: the feed's own
// timestamp verbatim, and when it reached us.
func (t *Tracker) noteFreshness(timestamp string, at time.Time) {
	if timestamp != "" {
		t.exchangeTimestamp = timestamp
		if millis, ok := parseMillis(timestamp); ok {
			t.lastAppliedMillis, t.haveLastMillis = millis, true
		}
	}
	t.receivedAt = at
}

// notePriceParsing flags levels whose price could not be interpreted. They are
// still reported; they are simply not comparable.
func (t *Tracker) notePriceParsing(sides ...[]book.Level) {
	for _, levels := range sides {
		for _, level := range levels {
			if !level.Price.Absent() && !level.Price.Valid() {
				t.flag(FlagUnparsablePrice)
				return
			}
		}
	}
}

// flag records an observation once. Flags keep first-occurrence order, which
// makes a failing test read like the sequence of events that produced it.
//
// The observer is notified only for a flag that is genuinely new, so a token
// whose connection drops repeatedly reports the fact once rather than on every
// attempt.
func (t *Tracker) flag(f Flag) {
	if slices.Contains(t.flags, f) {
		return
	}

	t.flags = append(t.flags, f)

	if t.opts.Observer != nil {
		t.opts.Observer.Flagged(t.tokenID, f)
	}
}

// parseMillis reads the feed's epoch-milliseconds timestamps, which arrive as
// strings and are kept as strings everywhere except here.
func parseMillis(timestamp string) (int64, bool) {
	millis, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return 0, false
	}

	return millis, true
}
