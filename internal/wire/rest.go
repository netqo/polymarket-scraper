package wire

import (
	"github.com/netqo/polymarket-scraper/internal/book"
	"github.com/netqo/polymarket-scraper/internal/decimal"
)

// RESTBook is the response body of the CLOB order book endpoints.
//
// It is richer than the websocket snapshot in exactly the ways that matter:
// min_order_size and neg_risk never appear on the websocket at all, and the
// output document is required to report both per token. That is why the REST
// metadata seed is a first-class part of every run rather than a fallback.
type RESTBook struct {
	Market    string `json:"market"`
	AssetID   string `json:"asset_id"`
	Timestamp string `json:"timestamp"`
	Hash      string `json:"hash"`

	Bids []book.Level `json:"bids"`
	Asks []book.Level `json:"asks"`

	MinOrderSize   decimal.Dec `json:"min_order_size"`
	TickSize       decimal.Dec `json:"tick_size"`
	LastTradePrice decimal.Dec `json:"last_trade_price"`

	// NegRisk is a pointer because absent and false are different answers:
	// defaulting a missing field to false would be inventing a value, and the
	// consuming agent uses this to decide whether a market is part of a
	// mutually exclusive set.
	NegRisk *bool `json:"neg_risk"`
}

// BookRequest is one entry in a batched book request body.
type BookRequest struct {
	TokenID string `json:"token_id"`
}

// NewBookRequests builds the body for a batched book request.
func NewBookRequests(tokenIDs []string) []BookRequest {
	requests := make([]BookRequest, len(tokenIDs))
	for i, id := range tokenIDs {
		requests[i] = BookRequest{TokenID: id}
	}

	return requests
}
