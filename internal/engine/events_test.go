package engine

import (
	"strconv"
	"testing"
	"time"

	"github.com/netqo/polymarket-scraper/internal/config"
	"github.com/netqo/polymarket-scraper/internal/testsupport"
	"github.com/netqo/polymarket-scraper/internal/tracker"
	"github.com/netqo/polymarket-scraper/internal/wire"
)

const (
	wsNewMarket = `{"id":"5551","market":"0xnew","condition_id":"0xcond",` +
		`"question":"Bitcoin Up or Down - Aug 14, 3:15PM ET","slug":"btc-up-or-down",` +
		`"assets_ids":["777","888"],"outcomes":["Up","Down"],` +
		`"timestamp":"1786728400000","event_type":"new_market"}`

	wsMarketResolved = `{"event_type":"market_resolved","id":"1031769","market":"0xmarket",` +
		`"assets_ids":["111","222"],"winning_asset_id":"111",` +
		`"winning_outcome":"Yes","timestamp":"1786790415550"}`
)

// A consumer sweeping for freshly created markets depends on these, and the
// short-duration crypto series it cares about most are created minutes before
// they start.
func TestNewMarketAnnouncementsAreReported(t *testing.T) {
	ws := testsupport.NewFakeWS(t,
		testsupport.WSStep{Send: wsSnapshot111},
		testsupport.WSStep{After: 20 * time.Millisecond, Send: wsNewMarket},
	)
	rest := testsupport.NewFakeREST(t)
	rest.ServeBook("111", levels("0.97", "100"), nil)

	document := collectOver(t, ws, rest, "111")

	if len(document.Events.NewMarkets) != 1 {
		t.Fatalf("got %d announcements, want 1. errors: %v", len(document.Events.NewMarkets), document.Errors)
	}

	announced := document.Events.NewMarkets[0]
	if announced.Question != "Bitcoin Up or Down - Aug 14, 3:15PM ET" {
		t.Errorf("question = %q", announced.Question)
	}
	if announced.ConditionID == nil || *announced.ConditionID != "0xcond" {
		t.Errorf("condition id = %v", announced.ConditionID)
	}
	if len(announced.AssetIDs) != 2 || announced.AssetIDs[0] != "777" {
		t.Errorf("asset ids = %v, want [777 888]", announced.AssetIDs)
	}
	if len(announced.Outcomes) != 2 || announced.Outcomes[0] != "Up" {
		t.Errorf("outcomes = %v, want [Up Down]", announced.Outcomes)
	}
	if announced.ReceivedAt == "" {
		t.Error("the announcement has no arrival time")
	}
}

// A market that settled during the window must be visible, so a consumer does
// not recommend something already dead.
func TestResolutionAnnouncementsAreReportedAndFlagTheirTokens(t *testing.T) {
	ws := testsupport.NewFakeWS(t,
		testsupport.WSStep{Send: wsSnapshot111},
		testsupport.WSStep{After: 20 * time.Millisecond, Send: wsMarketResolved},
	)
	rest := testsupport.NewFakeREST(t)
	rest.ServeBook("111", levels("0.97", "100"), nil)

	document := collectOver(t, ws, rest, "111")

	if len(document.Events.Resolved) != 1 {
		t.Fatalf("got %d resolutions, want 1. errors: %v", len(document.Events.Resolved), document.Errors)
	}

	settled := document.Events.Resolved[0]
	if settled.WinningOutcome == nil || *settled.WinningOutcome != "Yes" {
		t.Errorf("winning outcome = %v", settled.WinningOutcome)
	}
	if settled.WinningAssetID == nil || *settled.WinningAssetID != "111" {
		t.Errorf("winning asset = %v", settled.WinningAssetID)
	}

	// The token itself is flagged too, so it is visible without cross
	// referencing the events block.
	if !containsFlag(document.Books["111"].Flags, tracker.FlagMarketResolved) {
		t.Errorf("flags = %v, want %q", document.Books["111"].Flags, tracker.FlagMarketResolved)
	}
}

// The feed is global, so every connection receives the same announcement. A run
// with four shards must not report each new market four times, or a consumer
// counting fresh markets is wrong by a factor of four.
func TestAnnouncementsAreDeduplicatedAcrossShards(t *testing.T) {
	log := newEventLog(config.DefaultMaxEvents)
	at := time.Now()

	event := wire.NewMarket{
		ID:          "5551",
		ConditionID: "0xcond",
		Question:    "the same market seen by every shard",
		AssetIDs:    wire.StringList{"777"},
	}
	for range 4 {
		log.noteNewMarket(event, at)
	}

	if got := len(log.events().NewMarkets); got != 1 {
		t.Errorf("got %d announcements after four shards saw one, want 1", got)
	}
}

func TestResolutionsAreDeduplicated(t *testing.T) {
	log := newEventLog(config.DefaultMaxEvents)
	at := time.Now()

	event := wire.MarketResolved{ID: "1031769", Market: "0xmarket", WinningOutcome: "Yes"}
	for range 3 {
		log.noteResolved(event, at)
	}

	if got := len(log.events().Resolved); got != 1 {
		t.Errorf("got %d resolutions, want 1", got)
	}
}

// An announcement missing one identifier is still deduplicated on another,
// rather than being counted twice because one field happened to be absent.
func TestDeduplicationFallsBackThroughIdentifiers(t *testing.T) {
	log := newEventLog(config.DefaultMaxEvents)
	at := time.Now()

	withoutID := wire.NewMarket{ConditionID: "0xcond", Question: "no id"}
	log.noteNewMarket(withoutID, at)
	log.noteNewMarket(withoutID, at)

	if got := len(log.events().NewMarkets); got != 1 {
		t.Errorf("got %d announcements, want 1", got)
	}
}

// The feed's volume has nothing to do with how many tokens were asked for, so
// a long window could accumulate announcements without bound.
func TestAnnouncementsAreCapped(t *testing.T) {
	log := newEventLog(config.DefaultMaxEvents)
	at := time.Now()

	for i := range config.DefaultMaxEvents + 25 {
		log.noteNewMarket(wire.NewMarket{ID: strconv.Itoa(i)}, at)
	}

	if got := len(log.events().NewMarkets); got != config.DefaultMaxEvents {
		t.Errorf("got %d announcements, want the cap of %d", got, config.DefaultMaxEvents)
	}
	if got := log.suppressedCount(); got != 25 {
		t.Errorf("suppressedCount() = %d, want 25", got)
	}
}

// A truncated list that does not say it was truncated reads as a complete one.
func TestHittingTheCapIsRecordedInTheDocument(t *testing.T) {
	ws := testsupport.NewFakeWS(t, testsupport.WSStep{Send: wsSnapshot111})
	rest := testsupport.NewFakeREST(t)
	rest.ServeBook("111", levels("0.97", "100"), nil)

	collector, err := New(Options{
		Config: websocketConfig(ws.URL(), rest.URL()),
		Tokens: tokenListOf("111"),
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	at := time.Now()
	for i := range config.DefaultMaxEvents + 3 {
		collector.events.noteNewMarket(wire.NewMarket{ID: strconv.Itoa(i)}, at)
	}

	document := collector.finalizeDocument(at, at, nil)
	if !mentions(document.Errors, "announcements were not reported") {
		t.Errorf("errors = %v, want the truncation recorded", document.Errors)
	}
}

// Both lists are always present, so a run with no announcements reports empty
// arrays rather than nulls.
func TestTheEventsBlockIsAlwaysPresent(t *testing.T) {
	ws := testsupport.NewFakeWS(t, testsupport.WSStep{Send: wsSnapshot111})
	rest := testsupport.NewFakeREST(t)
	rest.ServeBook("111", levels("0.97", "100"), nil)

	document := collectOver(t, ws, rest, "111")

	if document.Events.NewMarkets == nil || document.Events.Resolved == nil {
		t.Error("an events list is nil, which would serialize as null rather than []")
	}
	if len(document.Events.NewMarkets) != 0 || len(document.Events.Resolved) != 0 {
		t.Errorf("a run with no announcements reported %d new and %d resolved",
			len(document.Events.NewMarkets), len(document.Events.Resolved))
	}
}
