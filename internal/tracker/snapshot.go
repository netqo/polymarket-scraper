package tracker

import (
	"time"

	"github.com/netqo/polymarket-scraper/internal/book"
	"github.com/netqo/polymarket-scraper/internal/decimal"
)

// LastTrade is the most recent fill seen for a token.
//
// FeeRateBPS is the reason it is collected: it is a free cross-check on the fee
// rate a consumer verifies through other means.
type LastTrade struct {
	Price      decimal.Dec
	Size       decimal.Dec
	Side       string
	FeeRateBPS decimal.Dec
	Timestamp  string
}

// Snapshot is everything reported about one token, frozen at the end of the
// window. It is a value, not a view: once produced it does not change.
type Snapshot struct {
	// TokenID is the token this snapshot describes.
	TokenID string

	// Status says whether the book below can be trusted, and if not, why not.
	Status Status

	// Source records where a trusted book came from. It is empty, and therefore
	// null in the output, whenever there is no trusted book to attribute.
	Source Source

	// ConditionID is the market the token belongs to, when it was learned.
	ConditionID string

	// Bids and Asks are the final book, bids descending and asks ascending.
	// They are empty for any status other than ok, and that is enforced
	// structurally rather than by convention: a stale book is not merely
	// omitted here, it is unreachable.
	Bids []book.Level
	Asks []book.Level

	// TickSize, MinOrderSize and NegRisk are market metadata. NegRisk is a
	// pointer because absent and false are different answers.
	TickSize     decimal.Dec
	MinOrderSize decimal.Dec
	NegRisk      *bool

	// ExchangeTimestamp is the feed's own timestamp for the last update,
	// verbatim. ReceivedAt is the local wall clock when it arrived. Both are
	// reported so a consumer can tell how old the book is and whether the two
	// clocks agree.
	ExchangeTimestamp string
	ReceivedAt        time.Time

	// UpdatesApplied counts incremental changes actually applied.
	UpdatesApplied int

	// LastTrade is the most recent fill, when one was seen.
	LastTrade *LastTrade

	// Flags are observations the scraper made and deliberately did not act on.
	Flags []Flag

	// Window is volatility context over the collection window.
	Window WindowSummary
}

// Missing builds the snapshot for a token that was requested but that the
// collection pipeline never produced anything for at all.
//
// This exists so completeness is a property of the report builder's loop rather
// than of the pipeline having succeeded: every requested token is reported, and
// one that fell through every crack says so explicitly instead of disappearing.
func Missing(tokenID string) Snapshot {
	return Snapshot{
		TokenID: tokenID,
		Status:  StatusNoData,
		Source:  SourceNone,
		Bids:    []book.Level{},
		Asks:    []book.Level{},
		Flags:   []Flag{},
	}
}
