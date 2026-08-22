// Package stream writes a run's changes to a file as they happen.
//
// The output document is complete, but it arrives at the end. Anything deciding
// what to do during the window cannot use it, and neither can anything watching
// to see whether a run is going wrong. This is the same run, told as it
// happens.
//
// One JSON object per line, appended and flushed per record, so a reader can
// tail the file and parse each line the moment it lands. That framing is chosen
// for the reader: a single JSON array would not be parseable until the run
// finished, which is precisely the problem being solved.
//
// It is a second view of the same facts, never a replacement for the document.
// The document remains the contract, is written atomically, and carries the
// guarantees; this stream is allowed to stop mid-record if the process is
// killed, and a consumer is expected to discard a trailing partial line.
package stream

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/netqo/polymarket-scraper/internal/decimal"
	"github.com/netqo/polymarket-scraper/internal/tracker"
)

// Version identifies the shape of the records below.
//
// Separate from the document's schema_version because the two change for
// different reasons and a consumer may well read one and not the other.
const Version = "1.0"

// fileMode matches the output document's. A stream names the tokens a run
// collected and what they were priced at, so there is no reason for it to be
// readable more widely than the document it accompanies.
const fileMode = 0o600

// Record kinds.
const (
	// KindHeader opens the file, naming the version and when the run began.
	KindHeader = "header"

	// KindQuote is a token's top of book, written when it moves.
	KindQuote = "quote"

	// KindTrade is a fill.
	KindTrade = "trade"

	// KindFlag is an observation raised against a token.
	KindFlag = "flag"

	// KindMarket is a market announced during the window.
	KindMarket = "market"

	// KindResolved is a market that settled during the window.
	KindResolved = "resolved"
)

// timeFormat is ISO-8601 UTC with milliseconds, the same convention the
// document uses for timestamps the scraper generates.
const timeFormat = "2006-01-02T15:04:05.000Z"

// record is one line of the stream.
//
// A single type with omitted fields rather than one type per kind, because a
// reader consuming a mixed stream has to switch on the kind anyway, and one
// shape means one thing to document. This is the only place in the project
// where omitempty is used: here a field's absence is the encoding of "this kind
// does not have one", not of "we did not find out".
type record struct {
	Kind string `json:"kind"`

	// At is when the scraper wrote the record.
	At string `json:"at"`

	// Token is the outcome token a record concerns, absent on records that
	// concern the run or a market rather than a token.
	Token string `json:"token,omitempty"`

	// Header fields.
	Version   string `json:"version,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	Run       string `json:"run,omitempty"`

	// Quote fields. Prices are the API's own decimal strings; spread and mid
	// are computed by integer arithmetic and rendered as strings for the same
	// reason. Null means there was no honest answer, never zero.
	Bid     *decimal.Dec `json:"bid,omitempty"`
	Ask     *decimal.Dec `json:"ask,omitempty"`
	Spread  *decimal.Dec `json:"spread,omitempty"`
	Mid     *decimal.Dec `json:"mid,omitempty"`
	Crossed bool         `json:"crossed,omitempty"`

	// Trade fields.
	Price      *decimal.Dec `json:"price,omitempty"`
	Size       *decimal.Dec `json:"size,omitempty"`
	Side       string       `json:"side,omitempty"`
	FeeRateBPS *decimal.Dec `json:"fee_rate_bps,omitempty"`

	// Flag field.
	Flag string `json:"flag,omitempty"`

	// Market fields.
	Question       string   `json:"question,omitempty"`
	ConditionID    string   `json:"condition_id,omitempty"`
	AssetIDs       []string `json:"assets_ids,omitempty"`
	Outcomes       []string `json:"outcomes,omitempty"`
	WinningAssetID string   `json:"winning_asset_id,omitempty"`
	WinningOutcome string   `json:"winning_outcome,omitempty"`

	// Category fields, market only. SportsMarketType is the only grouping the
	// feed offers and is absent for everything that is not a sports market.
	SportsMarketType string       `json:"sports_market_type,omitempty"`
	StartsAt         string       `json:"starts_at,omitempty"`
	MinTickSize      *decimal.Dec `json:"min_tick_size,omitempty"`

	// ExchangeTimestamp is the feed's own timestamp, verbatim, for records that
	// came from a message. Epoch milliseconds as a string, not reformatted, for
	// the same reason the document does not reformat it.
	ExchangeTimestamp string `json:"exchange_timestamp,omitempty"`
}

// Writer appends a run's changes to a file.
//
// Safe for concurrent use: the engine reports from a goroutine per connection,
// and a mutex here is what keeps two records from interleaving into one
// unparseable line.
type Writer struct {
	mu   sync.Mutex
	file *os.File
	now  func() time.Time

	// dropped counts records that could not be written, so a run can say that
	// its stream is incomplete rather than leaving a reader to assume it is not.
	dropped int
}

// Options configure a Writer.
type Options struct {
	// Path is the file to append to.
	Path string

	// Run identifies the run, so successive runs sharing a file can be told
	// apart the way they can in the log.
	Run string

	// StartedAt is when collection began.
	StartedAt time.Time

	// Now supplies the clock. Tests replace it; production leaves it nil.
	Now func() time.Time
}

// New opens a stream and writes its header.
//
// Appending rather than truncating, like the log file: a run is usually one of
// a series, and the header plus the run identifier is what separates them.
func New(opts Options) (*Writer, error) {
	// #nosec G304 -- the path is an operator-supplied setting; writing to an
	// arbitrary file is the entire purpose of it.
	file, err := os.OpenFile(filepath.Clean(opts.Path), os.O_APPEND|os.O_CREATE|os.O_WRONLY, fileMode)
	if err != nil {
		return nil, fmt.Errorf("cannot open the change stream %s: %w", opts.Path, err)
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	w := &Writer{file: file, now: now}
	w.write(record{
		Kind:      KindHeader,
		Version:   Version,
		Run:       opts.Run,
		StartedAt: format(opts.StartedAt),
	})

	return w, nil
}

// Flagged implements tracker.Observer.
func (w *Writer) Flagged(tokenID string, flag tracker.Flag) {
	w.write(record{Kind: KindFlag, Token: tokenID, Flag: string(flag)})
}

// Quoted implements tracker.Observer.
func (w *Writer) Quoted(tokenID string, quote tracker.Quote) {
	w.write(record{
		Kind:              KindQuote,
		Token:             tokenID,
		Bid:               present(quote.Bid),
		Ask:               present(quote.Ask),
		Spread:            present(quote.Spread),
		Mid:               present(quote.Mid),
		Crossed:           quote.Crossed,
		ExchangeTimestamp: quote.ExchangeTimestamp,
	})
}

// Traded implements tracker.Observer.
func (w *Writer) Traded(tokenID string, trade tracker.LastTrade) {
	w.write(record{
		Kind:              KindTrade,
		Token:             tokenID,
		Price:             present(trade.Price),
		Size:              present(trade.Size),
		Side:              trade.Side,
		FeeRateBPS:        present(trade.FeeRateBPS),
		ExchangeTimestamp: trade.Timestamp,
	})
}

// Market is a market announced during the window.
//
// A struct rather than a long argument list: the announcement now carries enough
// that positional arguments would be a row of strings nobody could read at the
// call site.
type Market struct {
	Question         string
	ConditionID      string
	AssetIDs         []string
	Outcomes         []string
	SportsMarketType string
	StartsAt         string
	MinTickSize      decimal.Dec

	// ExchangeTimestamp is the feed's own timestamp, verbatim.
	ExchangeTimestamp string
}

// Announced records a market created during the window.
func (w *Writer) Announced(market Market) {
	w.write(record{
		Kind:              KindMarket,
		Question:          market.Question,
		ConditionID:       market.ConditionID,
		AssetIDs:          market.AssetIDs,
		Outcomes:          market.Outcomes,
		SportsMarketType:  market.SportsMarketType,
		StartsAt:          market.StartsAt,
		MinTickSize:       present(market.MinTickSize),
		ExchangeTimestamp: market.ExchangeTimestamp,
	})
}

// Resolved records a market that settled during the window.
func (w *Writer) Resolved(conditionID string, assetIDs []string, winningAssetID, winningOutcome, exchangeTimestamp string) {
	w.write(record{
		Kind:              KindResolved,
		ConditionID:       conditionID,
		AssetIDs:          assetIDs,
		WinningAssetID:    winningAssetID,
		WinningOutcome:    winningOutcome,
		ExchangeTimestamp: exchangeTimestamp,
	})
}

// Dropped reports how many records could not be written, so a run can say its
// stream is incomplete rather than leaving a reader to assume otherwise.
func (w *Writer) Dropped() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.dropped
}

// Close closes the file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.file.Close()
}

// write appends one record.
//
// A failure is counted rather than returned. Every caller is on a goroutine
// whose job is collecting books, and none of them can do anything useful about
// a full disk except stop collecting, which would trade a degraded stream for
// no document at all.
func (w *Writer) write(r record) {
	r.At = format(w.now())

	encoded, err := json.Marshal(r)
	if err != nil {
		w.mu.Lock()
		w.dropped++
		w.mu.Unlock()

		return
	}
	encoded = append(encoded, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := w.file.Write(encoded); err != nil {
		w.dropped++
	}
}

// present returns a pointer to a value the feed actually provided, and nil
// otherwise, so that "not known" encodes as null rather than as an empty string.
func present(value decimal.Dec) *decimal.Dec {
	if value.Absent() {
		return nil
	}

	return &value
}

// format renders a timestamp the scraper generated.
func format(t time.Time) string {
	return t.UTC().Format(timeFormat)
}
