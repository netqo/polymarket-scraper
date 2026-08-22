// Package wire decodes and encodes the Polymarket market channel protocol.
//
// It is the anti-corruption layer: everything the rest of the scraper knows
// about the shape of Polymarket's messages is confined here, so a protocol
// change is a change to one package rather than a change everywhere.
//
// Much of what is here exists because the live API and its published
// documentation disagree, and every one of those disagreements fails silently.
// PROTOCOL.md is the record of them, with captured payloads; the comments below
// say only what a reader of this file needs in order to change it safely.
//
// Ordering of book levels is not corrected here. That belongs to the book
// package, which sorts on ingest and does not trust either the documentation or
// the wire.
package wire

import (
	"encoding/json"

	"github.com/netqo/polymarket-scraper/internal/book"
	"github.com/netqo/polymarket-scraper/internal/decimal"
)

// EventType is the event_type discriminator carried by every message.
type EventType string

// The event types this build understands.
const (
	EventBook           EventType = "book"
	EventPriceChange    EventType = "price_change"
	EventLastTradePrice EventType = "last_trade_price"
	EventTickSizeChange EventType = "tick_size_change"
	EventBestBidAsk     EventType = "best_bid_ask"
	EventNewMarket      EventType = "new_market"
	EventMarketResolved EventType = "market_resolved"
)

// Side values as the feed spells them.
const (
	SideBuy  = "BUY"
	SideSell = "SELL"
)

// Event is one decoded message from the market channel.
type Event interface {
	// Type reports the event_type this event was decoded from.
	Type() EventType
}

// Book is a full order book snapshot for one token.
//
// TickSize and LastTradePrice are present on the initial snapshot but absent
// from the refreshes sent after a trade, so tick size is harvested here and
// cached rather than expected on every book event. MinOrderSize and NegRisk
// never appear on the websocket at all; they come from REST.
type Book struct {
	Market    string       `json:"market"`
	AssetID   string       `json:"asset_id"`
	Timestamp string       `json:"timestamp"`
	Hash      string       `json:"hash"`
	Bids      []book.Level `json:"bids"`
	Asks      []book.Level `json:"asks"`

	TickSize       decimal.Dec `json:"tick_size"`
	LastTradePrice decimal.Dec `json:"last_trade_price"`
}

// Type implements Event.
func (Book) Type() EventType { return EventBook }

// PriceChange is a batch of incremental book updates.
//
// The envelope carries no asset id: each element names its own token, and one
// message commonly touches both legs of a binary market at once.
type PriceChange struct {
	Market    string             `json:"market"`
	Timestamp string             `json:"timestamp"`
	Changes   []PriceChangeEntry `json:"price_changes"`
}

// Type implements Event.
func (PriceChange) Type() EventType { return EventPriceChange }

// PriceChangeEntry is one level update within a price change batch.
//
// Size is the new aggregate size resting at the price, not a delta against the
// previous one, and a size of zero means the level was removed.
type PriceChangeEntry struct {
	AssetID string      `json:"asset_id"`
	Price   decimal.Dec `json:"price"`
	Size    decimal.Dec `json:"size"`
	Side    string      `json:"side"`
	Hash    string      `json:"hash"`
	BestBid decimal.Dec `json:"best_bid"`
	BestAsk decimal.Dec `json:"best_ask"`
}

// BookSide maps the feed's side to the book side it updates, reporting false
// for a side this build does not recognise.
//
// A buy order rests on the bid side and a sell order on the ask side. Guessing
// when the value is unrecognised would put liquidity on the wrong half of the
// book, which is worse than not applying the update at all.
func (e PriceChangeEntry) BookSide() (book.Side, bool) {
	switch e.Side {
	case SideBuy:
		return book.Bids, true
	case SideSell:
		return book.Asks, true
	default:
		return 0, false
	}
}

// Level is the level this entry describes.
func (e PriceChangeEntry) Level() book.Level {
	return book.Level{Price: e.Price, Size: e.Size}
}

// LastTrade reports a fill.
//
// FeeRateBPS is the reason this event is collected at all: it is a free
// cross-check on the fee rate the consuming agent verifies separately.
type LastTrade struct {
	Market          string      `json:"market"`
	AssetID         string      `json:"asset_id"`
	Price           decimal.Dec `json:"price"`
	Size            decimal.Dec `json:"size"`
	Side            string      `json:"side"`
	FeeRateBPS      decimal.Dec `json:"fee_rate_bps"`
	Timestamp       string      `json:"timestamp"`
	TransactionHash string      `json:"transaction_hash"`
}

// Type implements Event.
func (LastTrade) Type() EventType { return EventLastTradePrice }

// TickSizeChange reports that a token's minimum price increment changed.
type TickSizeChange struct {
	Market      string      `json:"market"`
	AssetID     string      `json:"asset_id"`
	OldTickSize decimal.Dec `json:"old_tick_size"`
	NewTickSize decimal.Dec `json:"new_tick_size"`
	Timestamp   string      `json:"timestamp"`
}

// Type implements Event.
func (TickSizeChange) Type() EventType { return EventTickSizeChange }

// BestBidAsk is the published top of book.
//
// It is used only as a cross-check against the book maintained from snapshots
// and deltas, never as the source of it: it is not guaranteed to be delivered,
// so relying on it would make correctness depend on an optional message.
type BestBidAsk struct {
	Market    string      `json:"market"`
	AssetID   string      `json:"asset_id"`
	BestBid   decimal.Dec `json:"best_bid"`
	BestAsk   decimal.Dec `json:"best_ask"`
	Spread    decimal.Dec `json:"spread"`
	Timestamp string      `json:"timestamp"`
}

// Type implements Event.
func (BestBidAsk) Type() EventType { return EventBestBidAsk }

// NewMarket announces a market that has just been created.
//
// The announcement feed is global rather than filtered to the current
// subscription, so it doubles as a discovery feed and needs a hard budget
// wherever it drives subscriptions.
type NewMarket struct {
	ID          string     `json:"id"`
	Market      string     `json:"market"`
	ConditionID string     `json:"condition_id"`
	Question    string     `json:"question"`
	Slug        string     `json:"slug"`
	AssetIDs    StringList `json:"assets_ids"`
	Outcomes    StringList `json:"outcomes"`
	Timestamp   string     `json:"timestamp"`
}

// Type implements Event.
func (NewMarket) Type() EventType { return EventNewMarket }

// MarketResolved announces that a market has settled.
//
// Reporting it keeps the consuming agent from recommending a market that is
// already dead.
type MarketResolved struct {
	ID             string     `json:"id"`
	Market         string     `json:"market"`
	AssetIDs       StringList `json:"assets_ids"`
	WinningAssetID string     `json:"winning_asset_id"`
	WinningOutcome string     `json:"winning_outcome"`
	Timestamp      string     `json:"timestamp"`
}

// Type implements Event.
func (MarketResolved) Type() EventType { return EventMarketResolved }

// Unknown is a message whose event_type this build does not recognise.
//
// It is surfaced rather than dropped so that a protocol addition shows up in
// the run's error list instead of passing unnoticed.
type Unknown struct {
	EventType EventType
	Raw       json.RawMessage
}

// Type implements Event.
func (u Unknown) Type() EventType { return u.EventType }
