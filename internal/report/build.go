package report

import (
	"math"
	"time"

	"github.com/netqo/polymarket-scraper/internal/book"
	"github.com/netqo/polymarket-scraper/internal/tracker"
)

// Input is everything needed to build the document.
type Input struct {
	StartedAt  time.Time
	FinishedAt time.Time

	// Window is the length of the collection window as configured, which is not
	// the same as FinishedAt minus StartedAt: the difference between them is
	// the shutdown time, and reporting that as the window would misstate how
	// long the data was actually gathered over.
	Window time.Duration

	// Requested is the token list as loaded, deduplicated and in order. It is
	// the authority on what "requested" means.
	Requested []string

	// Snapshots holds whatever the collection pipeline produced, keyed by token
	// id. A requested token missing from it is reported as having produced
	// nothing, rather than being left out.
	Snapshots map[string]tracker.Snapshot

	// Discovered holds tokens picked up from announcements during the run.
	Discovered []tracker.Snapshot

	Connection Connection
	Events     Events
	Errors     []string
}

// Build assembles the output document.
//
// Completeness is a property of the loop below rather than of the pipeline
// having succeeded: the requested token list is iterated as the authority, and
// anything missing from the results is reported explicitly as having produced
// nothing. A token cannot silently disappear because a shard died.
func Build(in Input) Document {
	doc := Document{
		SchemaVersion:   SchemaVersion,
		StartedAt:       FormatTime(in.StartedAt),
		FinishedAt:      FormatTime(in.FinishedAt),
		WindowSeconds:   int(math.Round(in.Window.Seconds())),
		TokensRequested: len(in.Requested),
		Connection:      in.Connection,
		Books:           make(map[string]Token, len(in.Requested)+len(in.Discovered)),
		Events:          normalizeEvents(in.Events),
		Errors:          nonNilStrings(in.Errors),
	}

	for _, id := range in.Requested {
		snapshot, produced := in.Snapshots[id]
		if !produced {
			snapshot = tracker.Missing(id)
		}

		token := renderToken(snapshot)
		doc.Books[id] = token

		// Counted over the requested tokens alone. A consumer reads this
		// against tokens_requested as a health ratio, so letting a token the
		// run picked up on its own contribute would make that ratio wrong, and
		// able to exceed one.
		if token.Status == string(tracker.StatusOK) {
			doc.TokensOK++
		}
	}

	for _, snapshot := range in.Discovered {
		if _, alreadyRequested := doc.Books[snapshot.TokenID]; alreadyRequested {
			continue
		}
		doc.Books[snapshot.TokenID] = renderToken(snapshot)
		doc.TokensDiscovered++
	}

	return doc
}

// renderToken converts one tracker snapshot into its document form.
func renderToken(snapshot tracker.Snapshot) Token {
	return Token{
		Status:            string(snapshot.Status),
		Source:            optionalSource(snapshot.Source),
		ConditionID:       Optional(snapshot.ConditionID),
		Bids:              renderLevels(snapshot.Bids),
		Asks:              renderLevels(snapshot.Asks),
		TickSize:          snapshot.TickSize,
		MinOrderSize:      snapshot.MinOrderSize,
		NegRisk:           snapshot.NegRisk,
		ExchangeTimestamp: Optional(snapshot.ExchangeTimestamp),
		ReceivedAt:        optionalTime(snapshot.ReceivedAt),
		UpdatesApplied:    snapshot.UpdatesApplied,
		LastTrade:         renderLastTrade(snapshot.LastTrade),
		Flags:             renderFlags(snapshot.Flags),
		Window:            renderWindow(snapshot.Window),
	}
}

// renderLevels converts book levels, never returning nil: an empty book is an
// empty array, which is a different statement from null.
func renderLevels(levels []book.Level) []Level {
	rendered := make([]Level, len(levels))
	for i, level := range levels {
		rendered[i] = Level{Price: level.Price, Size: level.Size}
	}

	return rendered
}

func renderLastTrade(trade *tracker.LastTrade) *LastTrade {
	if trade == nil {
		return nil
	}

	return &LastTrade{
		Price:      trade.Price,
		Size:       trade.Size,
		Side:       trade.Side,
		FeeRateBPS: trade.FeeRateBPS,
		Timestamp:  trade.Timestamp,
	}
}

func renderFlags(flags []tracker.Flag) []string {
	rendered := make([]string, len(flags))
	for i, flag := range flags {
		rendered[i] = string(flag)
	}

	return rendered
}

func renderWindow(summary tracker.WindowSummary) Window {
	return Window{
		Updates:            summary.Updates,
		MidHigh:            summary.MidHigh,
		MidLow:             summary.MidLow,
		SpreadTimeWeighted: summary.SpreadTimeWeighted,
		TwoSidedMillis:     summary.TwoSidedMillis,
	}
}

// normalizeEvents replaces nil slices with empty ones, so the document never
// carries a null where a list belongs.
func normalizeEvents(events Events) Events {
	if events.NewMarkets == nil {
		events.NewMarkets = []NewMarket{}
	}
	if events.Resolved == nil {
		events.Resolved = []Resolved{}
	}

	for i := range events.NewMarkets {
		events.NewMarkets[i].AssetIDs = nonNilStrings(events.NewMarkets[i].AssetIDs)
		events.NewMarkets[i].Outcomes = nonNilStrings(events.NewMarkets[i].Outcomes)
	}
	for i := range events.Resolved {
		events.Resolved[i].AssetIDs = nonNilStrings(events.Resolved[i].AssetIDs)
	}

	return events
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}

	return values
}
