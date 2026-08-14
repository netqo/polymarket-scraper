package engine

import (
	"strconv"
	"testing"
	"time"

	"github.com/netqo/polymarket-scraper/internal/config"
	"github.com/netqo/polymarket-scraper/internal/testsupport"
	"github.com/netqo/polymarket-scraper/internal/tokenlist"
	"github.com/netqo/polymarket-scraper/internal/tracker"
	"github.com/netqo/polymarket-scraper/internal/wire"
)

// The short-duration crypto series creates instances minutes before they start,
// so a token list assembled before the run began does not contain them, and by
// the next run they may already be resolving.
func TestAnnouncedTokensAreSubscribedToAndReported(t *testing.T) {
	ws := testsupport.NewFakeWS(t,
		testsupport.WSStep{Send: wsSnapshot111},
		testsupport.WSStep{After: 20 * time.Millisecond, Send: wsNewMarket},
	)
	rest := testsupport.NewFakeREST(t)
	rest.ServeBook("111", levels("0.97", "100"), nil)
	rest.ServeBook("777", levels("0.10", "5"), levels("0.90", "5"))
	rest.ServeBook("888", levels("0.90", "5"), levels("0.10", "5"))

	document := collectOver(t, ws, rest, "111")

	if document.TokensDiscovered != 2 {
		t.Fatalf("TokensDiscovered = %d, want 2. errors: %v", document.TokensDiscovered, document.Errors)
	}
	if document.TokensRequested != 1 {
		t.Errorf("TokensRequested = %d, want the input count unchanged", document.TokensRequested)
	}
	if len(document.Books) != 3 {
		t.Errorf("got %d books, want the requested one plus two announced", len(document.Books))
	}

	for _, id := range []string{"777", "888"} {
		token, present := document.Books[id]
		if !present {
			t.Fatalf("announced token %s is missing from the document", id)
		}
		if !containsFlag(token.Flags, tracker.FlagDiscoveredMidWindow) {
			t.Errorf("token %s flags = %v, want %q", id, token.Flags, tracker.FlagDiscoveredMidWindow)
		}
	}

	// The subscription change must actually have gone out on the wire.
	updates := ws.ReceivedMatching(`"operation":"subscribe"`)
	if len(updates) == 0 {
		t.Errorf("no subscription change was sent: %v", ws.Received())
	}
}

// The announcement feed is global, so without a budget a long run would
// gradually subscribe to every market on the exchange.
func TestDiscoveryStopsAtTheLimit(t *testing.T) {
	shard := newShardState(0, []string{"111"}, tracker.Options{})
	engine := engineForDiscovery(t, 3)

	for i := range 10 {
		engine.admitAnnounced(shard, wire.NewMarket{
			ID:       strconv.Itoa(i),
			AssetIDs: wire.StringList{"a" + strconv.Itoa(i), "b" + strconv.Itoa(i)},
		}, time.Now())
	}

	if got := len(shard.discovered); got != 3 {
		t.Errorf("took on %d announced tokens, want the limit of 3", got)
	}
	if !shard.closedToDiscovery {
		t.Error("the shard is still open to discovery after hitting the limit")
	}
	if !mentions(engine.errors.Messages(), "limit of 3") {
		t.Errorf("errors = %v, want the limit recorded", engine.errors.Messages())
	}
}

// A connection still has a hard ceiling: past roughly 750 assets the server
// accepts the subscription and silently stops sending snapshots. Announced
// tokens must not push a connection over it, because doing so would cost the
// tokens that were actually requested.
func TestDiscoveryStopsAtTheConnectionCeiling(t *testing.T) {
	rest := testsupport.NewFakeREST(t)

	cfg := websocketConfig("ws://127.0.0.1:1/none", rest.URL())
	// Wide enough that the width plus the discovery budget exceeds the ceiling,
	// so the ceiling is what binds rather than the budget.
	cfg.MaxAssetsPerConnection = config.MaxAssetsCeiling - 50
	cfg.DiscoverLimit = 100

	ids := scaleIDs(cfg.MaxAssetsPerConnection)

	engine, err := New(Options{Config: cfg, Tokens: tokenlist.List{IDs: ids}})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	engine.buildShards()
	shard := engine.shards[0]

	// Ask for more than the remaining headroom.
	announced := make(wire.StringList, 0, 80)
	for i := range 80 {
		announced = append(announced, "announced-"+strconv.Itoa(i))
	}
	engine.admitAnnounced(shard, wire.NewMarket{ID: "1", AssetIDs: announced}, time.Now())

	if len(shard.trackers) > config.MaxAssetsCeiling {
		t.Errorf("the shard holds %d assets, past the ceiling of %d",
			len(shard.trackers), config.MaxAssetsCeiling)
	}
	if len(shard.discovered) != 50 {
		t.Errorf("took on %d announced tokens, want the 50 of headroom below the ceiling",
			len(shard.discovered))
	}
	if !mentions(engine.errors.Messages(), "ceiling") {
		t.Errorf("errors = %v, want the ceiling recorded", engine.errors.Messages())
	}
}

// Zero means off, and an off switch has to actually be off.
func TestDiscoveryCanBeDisabled(t *testing.T) {
	engine := engineForDiscovery(t, 0)
	shard := newShardState(0, []string{"111"}, tracker.Options{})

	engine.admitAnnounced(shard, wire.NewMarket{ID: "1", AssetIDs: wire.StringList{"a", "b"}}, time.Now())

	if len(shard.discovered) != 0 {
		t.Errorf("took on %d tokens with discovery disabled", len(shard.discovered))
	}
}

// A token already asked for must not be taken on again as a discovery: it would
// be counted twice and reported in the wrong place.
func TestAnAlreadyRequestedTokenIsNotRediscovered(t *testing.T) {
	engine := engineForDiscovery(t, 100)
	shard := newShardState(0, []string{"111"}, tracker.Options{})

	engine.admitAnnounced(shard, wire.NewMarket{ID: "1", AssetIDs: wire.StringList{"111", "999"}}, time.Now())

	if shard.discovered["111"] {
		t.Error("a requested token was taken on as a discovery")
	}
	if !shard.discovered["999"] {
		t.Error("the genuinely new token was not taken on")
	}
}

// A token subscribed in the last seconds of a window has almost no history
// behind it and no time to recover from anything going wrong.
func TestDiscoveryClosesAtTheSweep(t *testing.T) {
	engine := engineForDiscovery(t, 100)
	shard := newShardState(0, []string{"111"}, tracker.Options{})

	engine.applyControl(shard, control{kind: ctrlSweep, at: time.Now()})
	engine.admitAnnounced(shard, wire.NewMarket{ID: "1", AssetIDs: wire.StringList{"a"}}, time.Now())

	if len(shard.discovered) != 0 {
		t.Errorf("took on %d tokens after the sweep closed discovery", len(shard.discovered))
	}
}

// The requested list is what completeness is measured against, so a discovered
// token must never stand in for one that was asked for and never arrived.
func TestDiscoveredTokensDoNotCountTowardsTheRequestedOnes(t *testing.T) {
	engine := engineForDiscovery(t, 100)

	all := map[string]tracker.Snapshot{
		"111": {TokenID: "111", Status: tracker.StatusOK},
		"999": {TokenID: "999", Status: tracker.StatusOK},
		"888": {TokenID: "888", Status: tracker.StatusOK},
	}

	requested, discovered := engine.splitDiscovered(all)

	if len(requested) != 1 || requested["111"].TokenID != "111" {
		t.Errorf("requested = %v, want only the token that was asked for", requested)
	}
	if len(discovered) != 2 {
		t.Fatalf("got %d discovered, want 2", len(discovered))
	}
	// Sorted, so a document does not change order between runs that saw the
	// same thing.
	if discovered[0].TokenID != "888" || discovered[1].TokenID != "999" {
		t.Errorf("discovered order = %v, want sorted", []string{discovered[0].TokenID, discovered[1].TokenID})
	}
}

// engineForDiscovery builds an engine whose only interesting configuration is
// the discovery budget.
func engineForDiscovery(t *testing.T, limit int) *Engine {
	t.Helper()

	rest := testsupport.NewFakeREST(t)
	cfg := websocketConfig("ws://127.0.0.1:1/none", rest.URL())
	cfg.DiscoverLimit = limit

	engine, err := New(Options{Config: cfg, Tokens: tokenlist.List{IDs: []string{"111"}}})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	return engine
}

// The consuming agent's documented configuration asks for 400 tokens, and the
// default connection width is also 400, so a full shortlist fills a connection
// exactly. Discovery must still work there: the short-duration series it exists
// for is that agent's most time-sensitive case, and a feature that switches
// itself off at the default configuration is not a feature.
func TestDiscoveryStillWorksOnAFullShortlist(t *testing.T) {
	rest := testsupport.NewFakeREST(t)

	cfg := websocketConfig("ws://127.0.0.1:1/none", rest.URL())
	cfg.MaxAssetsPerConnection = config.DefaultMaxAssetsPerConnection
	cfg.DiscoverLimit = config.DefaultDiscoverLimit

	ids := scaleIDs(cfg.MaxAssetsPerConnection)

	engine, err := New(Options{Config: cfg, Tokens: tokenlist.List{IDs: ids}})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	engine.buildShards()

	if len(engine.shards) != 1 {
		t.Fatalf("got %d shards, want 1 at exactly the connection width", len(engine.shards))
	}
	shard := engine.shards[0]

	engine.admitAnnounced(shard, wire.NewMarket{
		ID:       "1",
		AssetIDs: wire.StringList{"announced-a", "announced-b"},
	}, time.Now())

	if len(shard.discovered) != 2 {
		t.Fatalf("took on %d announced tokens on a full shortlist, want 2. errors: %v",
			len(shard.discovered), engine.errors.Messages())
	}
}
