// Package report builds and writes the output document.
//
// This package is the output contract. SCHEMA.md is written from it and a test
// checks the two against each other, so documentation that disagrees with the
// code is a failing build rather than a surprise for whoever is reading it.
//
// Three rules govern the shape, and all three are enforced by tests over the
// type graph rather than by review:
//
//   - No field is ever omitted. A value that is not known is null. A consumer
//     has to be able to tell "no liquidity" from "we did not find out", and a
//     missing key destroys that distinction.
//   - No field is a float. Prices and sizes carry the API's own text, and even
//     the statistics the scraper computes itself are rendered by integer
//     arithmetic, so no number in the document can have been rounded.
//   - Every requested token appears exactly once, whatever happened during the
//     run.
package report

import (
	"time"

	"github.com/netqo/polymarket-scraper/internal/decimal"
	"github.com/netqo/polymarket-scraper/internal/tracker"
)

// SchemaVersion identifies the shape of the document.
//
// Any change that could break a consumer parsing it bumps this, and SCHEMA.md
// is updated in the same commit. Adding an optional field does not.
const SchemaVersion = "1.0"

// TimeFormat is the one convention for every timestamp the scraper generates:
// ISO-8601, UTC, milliseconds, with an explicit Z.
//
// Timestamps that came from the feed are not reformatted. They are reported as
// the exact text the API sent, because re-rendering them would be the same kind
// of lossy round trip the decimal handling exists to avoid.
const TimeFormat = "2006-01-02T15:04:05.000Z"

// Document is the output document.
type Document struct {
	SchemaVersion string `json:"schema_version"`

	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at"`
	WindowSeconds int    `json:"window_seconds"`

	TokensRequested  int `json:"tokens_requested"`
	TokensOK         int `json:"tokens_ok"`
	TokensDiscovered int `json:"tokens_discovered"`

	Connection Connection `json:"connection"`

	// Books is keyed by token id. It always contains every requested token and
	// may contain more, when tokens were discovered during the run.
	Books map[string]Token `json:"books"`

	Events Events `json:"events"`

	// Errors are human-readable descriptions of anything abnormal. They are
	// meant to be quoted verbatim by whatever reads this document, so they name
	// what happened rather than merely that something did.
	Errors []string `json:"errors"`
}

// Connection reports how the data was obtained, which is context for judging
// how much to trust it.
type Connection struct {
	WSConnections int `json:"ws_connections"`
	Reconnects    int `json:"reconnects"`
	RESTRequests  int `json:"rest_requests"`
	RESTResyncs   int `json:"rest_resyncs"`
}

// Token is everything reported about one token.
//
// Every field is always present. For any status other than ok the book is
// empty and the timestamps are null, because a book that is not current is not
// reported at all.
type Token struct {
	Status string  `json:"status"`
	Source *string `json:"source"`

	ConditionID *string `json:"condition_id"`

	Bids []Level `json:"bids"`
	Asks []Level `json:"asks"`

	TickSize     decimal.Dec `json:"tick_size"`
	MinOrderSize decimal.Dec `json:"min_order_size"`
	NegRisk      *bool       `json:"neg_risk"`

	// ExchangeTimestamp is the feed's own timestamp, verbatim. ReceivedAt is
	// the scraper's wall clock when it arrived. Both are reported so a consumer
	// can compute how old the book is and see whether the clocks agree.
	ExchangeTimestamp *string `json:"exchange_timestamp"`
	ReceivedAt        *string `json:"received_at"`

	UpdatesApplied int `json:"updates_applied"`

	LastTrade *LastTrade `json:"last_trade"`

	// Flags name things the scraper observed and deliberately did not correct.
	Flags []string `json:"flags"`

	Window Window `json:"window"`
}

// Level is one price level.
//
// Structurally identical to book.Level, and deliberately not the same type. The
// duplication is the point: it is the boundary between what the scraper works
// with and what it promises, and a field added to the internal type must not
// appear in a frozen output contract because someone forgot the two were the
// same declaration. renderLevels is the one place the crossing happens, so
// there is exactly one place to notice.
//
// The same reasoning keeps LastTrade separate from tracker.LastTrade and Token
// separate from tracker.Snapshot: those carry the wire's optionality, where a
// pointer means null, while the internal types carry the domain's.
type Level struct {
	Price decimal.Dec `json:"price"`
	Size  decimal.Dec `json:"size"`
}

// LastTrade is the most recent fill seen during the window.
type LastTrade struct {
	Price      decimal.Dec `json:"price"`
	Size       decimal.Dec `json:"size"`
	Side       string      `json:"side"`
	FeeRateBPS decimal.Dec `json:"fee_rate_bps"`
	Timestamp  string      `json:"timestamp"`
}

// Window is volatility context over the collection window.
type Window struct {
	Updates int `json:"updates"`

	MidHigh decimal.Dec `json:"mid_high"`
	MidLow  decimal.Dec `json:"mid_low"`

	// SpreadTimeWeighted is averaged over the time the book had two sides, not
	// over the number of updates. TwoSidedMillis says how much of the window
	// that was, so a consumer can see how representative the average is.
	SpreadTimeWeighted decimal.Dec `json:"spread_time_weighted"`
	TwoSidedMillis     int64       `json:"two_sided_millis"`
}

// Events are the announcements seen during the window.
type Events struct {
	NewMarkets []NewMarket `json:"new_markets"`
	Resolved   []Resolved  `json:"resolved"`
}

// NewMarket is a market announced during the window.
type NewMarket struct {
	Question    string   `json:"question"`
	ConditionID *string  `json:"condition_id"`
	AssetIDs    []string `json:"assets_ids"`
	Outcomes    []string `json:"outcomes"`
	ReceivedAt  string   `json:"received_at"`

	// SportsMarketType is the only categorisation the feed offers, and it is
	// null for everything that is not a sports market. The scraper reports it
	// as it arrives and groups nothing itself: which markets belong together is
	// a judgement, and judgement belongs to whatever reads this.
	SportsMarketType *string `json:"sports_market_type"`

	// StartsAt is when the underlying event begins, in the exchange's own
	// spelling and not reformatted. Null when there is no scheduled event.
	StartsAt *string `json:"starts_at"`

	// MinTickSize is the market's minimum price increment, known from the
	// announcement itself rather than waiting for a book.
	MinTickSize decimal.Dec `json:"min_tick_size"`
}

// Resolved is a market that settled during the window.
type Resolved struct {
	ConditionID    *string  `json:"condition_id"`
	AssetIDs       []string `json:"assets_ids"`
	WinningAssetID *string  `json:"winning_asset_id"`
	WinningOutcome *string  `json:"winning_outcome"`
	ReceivedAt     string   `json:"received_at"`
}

// FormatTime renders a scraper-generated timestamp.
//
// It is exported so that everything producing a timestamp for the document goes
// through one place. Two spellings of the same convention would eventually
// diverge, and a consumer parsing the result would have no way to tell which it
// was looking at.
func FormatTime(t time.Time) string {
	return t.UTC().Format(TimeFormat)
}

// optionalTime renders a scraper-generated timestamp, or null when there is
// none. The zero time means the event never happened, not the epoch.
func optionalTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}

	formatted := FormatTime(t)

	return &formatted
}

// Optional renders a string, or null when it is empty. Empty means "not
// learned", and reporting it as "" would look like a value.
//
// It is exported for the same reason FormatTime is: everything that fills in
// this schema, including the engine that builds the event lists, has to apply
// the rule the same way, and two spellings of it would eventually disagree.
func Optional(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

// optionalSource renders a source attribution, or null when there is no
// trustworthy book to attribute.
func optionalSource(s tracker.Source) *string {
	return Optional(string(s))
}
