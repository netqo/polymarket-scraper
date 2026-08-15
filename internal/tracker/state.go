// Package tracker holds everything the scraper knows about one token.
//
// This is where the promise the whole tool exists to keep is implemented: a
// book reported as current really is current. A silently stale book becomes a
// phantom arbitrage signal and then a real losing trade, so a token that has
// been through a disconnect, a decode failure or a suspected gap is untrusted
// until it has been re-seeded, and if re-seeding does not happen it is reported
// as a failure rather than having its pre-gap book passed off as fresh.
//
// The state machine performs no side effects. Every method returns an Effect
// describing what it would like the caller to do, which makes the whole thing a
// pure function of (state, event) and lets the transition table be checked
// exhaustively with no concurrency, no clock and no network in the way.
package tracker

// State is where a token sits in its trust lifecycle.
type State int

// The lifecycle. A token starts Pending, becomes Live once seeded, and drops
// out of Live the moment anything casts doubt on its book.
const (
	// StatePending means subscribed but not yet seeded. The book is empty and
	// carries no information: nothing has been received to put in it.
	StatePending State = iota

	// StateLive means seeded and trusted. Deltas are being applied.
	StateLive

	// StateResyncing means the book is not trusted and a re-seed has been asked
	// for. Doubt and the request for a fresh book are one step: nothing casts
	// doubt on a book without also asking for it to be replaced, so there is no
	// separate "stale but not yet resyncing" state to be in.
	StateResyncing

	// StateFailed means re-seeding was attempted and did not succeed, or the
	// window closed while the token was untrusted.
	StateFailed

	// StateSubscribeFailed means a subscription was never established.
	StateSubscribeFailed
)

// String implements fmt.Stringer for logs and test failures.
func (s State) String() string {
	switch s {
	case StatePending:
		return "pending"
	case StateLive:
		return "live"
	case StateResyncing:
		return "resyncing"
	case StateFailed:
		return "failed"
	case StateSubscribeFailed:
		return "subscribe_failed"
	default:
		return "unknown"
	}
}

// trusted reports whether the book may be shown as current.
func (s State) trusted() bool { return s == StateLive }

// Status is the per-token status in the output document.
//
// The set is closed at exactly these four values. A consuming agent's prompt
// hardcodes them, so a fifth would be a breaking change to the output contract;
// anything else worth saying is said with a flag.
type Status string

// The four statuses.
const (
	// StatusOK means the book is current. Empty sides with this status mean the
	// token genuinely has no liquidity, which is a real answer and not a
	// failure.
	StatusOK Status = "ok"

	// StatusNoData means nothing was ever received for the token.
	StatusNoData Status = "no_data"

	// StatusSubscribeFailed means no subscription was established.
	StatusSubscribeFailed Status = "subscribe_failed"

	// StatusResyncFailed means the book became untrusted and could not be
	// re-seeded. The pre-gap book is not reported.
	StatusResyncFailed Status = "resync_failed"
)

// Source records where the reported book actually came from, which feeds the
// consuming agent's confidence assessment.
type Source string

// The sources. The empty value means the question has no honest answer, which
// is the case whenever there is no trustworthy book to attribute.
const (
	SourceNone     Source = ""
	SourceWS       Source = "ws"
	SourceWSResync Source = "ws+rest_resync"
	SourceRESTOnly Source = "rest_only"
)

// Effect is what a transition would like the caller to do next.
//
// Returning an intent rather than acting on it is what keeps this package free
// of I/O, and therefore free of the concurrency hazards that would otherwise
// make the trust rules hard to verify.
type Effect int

// The effects.
const (
	// EffectNone means the caller has nothing to do.
	EffectNone Effect = iota

	// EffectRequestResync means the token needs a fresh book over REST before
	// it can be trusted again.
	EffectRequestResync
)

// String implements fmt.Stringer for logs and test failures.
func (e Effect) String() string {
	if e == EffectRequestResync {
		return "request_resync"
	}

	return "none"
}

// Flag is a note about something the scraper observed and deliberately did not
// correct.
//
// Flags are the extension point that keeps the status set closed. The scraper
// never repairs suspicious data: it labels it and lets the consumer decide,
// because repairing it would destroy the evidence that something is wrong.
type Flag string

// The flags.
const (
	// FlagCrossedBook means the best bid was at or above the best ask when the
	// window closed, which usually means the data is stale.
	FlagCrossedBook Flag = "crossed_book"

	// FlagDeltaGap means an update was missed or arrived too far out of order
	// to be trusted.
	FlagDeltaGap Flag = "delta_gap"

	// FlagDeltaGapResynced means a gap was recovered from by re-seeding.
	FlagDeltaGapResynced Flag = "delta_gap_resynced"

	// FlagDisconnected means the connection carrying this token dropped at
	// least once during the window.
	FlagDisconnected Flag = "disconnected"

	// FlagSnapshotOnly means the book was seeded and no delta ever arrived.
	FlagSnapshotOnly Flag = "snapshot_only"

	// FlagPreSnapshotDeltaDropped means deltas arrived before any snapshot and
	// were discarded rather than applied to an empty book.
	FlagPreSnapshotDeltaDropped Flag = "pre_snapshot_delta_dropped"

	// FlagDuplicateDeltaDropped means an update was received more than once.
	FlagDuplicateDeltaDropped Flag = "duplicate_delta_dropped"

	// FlagTimestampRegression means an update's timestamp went backwards, but
	// by less than the configured tolerance, so it was applied.
	FlagTimestampRegression Flag = "timestamp_regression"

	// FlagDecodeError means a message for this token could not be decoded.
	FlagDecodeError Flag = "decode_error"

	// FlagUnknownSide means an update named a side this build does not
	// recognise and was therefore not applied.
	FlagUnknownSide Flag = "unknown_side"

	// FlagBestBidAskMismatch means the published top of book disagreed with the
	// book maintained from snapshots and deltas.
	FlagBestBidAskMismatch Flag = "best_bid_ask_mismatch"

	// FlagUnparsablePrice means a level carried a price that could not be
	// interpreted numerically. The level is still reported.
	FlagUnparsablePrice Flag = "unparsable_price"

	// FlagTokenNotFound means the exchange does not know this token id.
	FlagTokenNotFound Flag = "token_not_found"

	// FlagTickSizeChanged means the minimum price increment changed during the
	// window, which matters to anything sizing a buffer in ticks.
	FlagTickSizeChanged Flag = "tick_size_changed"

	// FlagMarketResolved means the market settled during the window, so the
	// book is of historical interest only.
	FlagMarketResolved Flag = "market_resolved"

	// FlagDiscoveredMidWindow means the token was not requested but was picked
	// up from an announcement during the run.
	FlagDiscoveredMidWindow Flag = "discovered_mid_window"
)
