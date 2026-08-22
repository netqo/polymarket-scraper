// Test data: Invented announcements, shaped like the captured ones in
// internal/wire/frame_test.go. What is under test is which tokens a run takes on
// and where it puts them, which is this program's policy rather than the
// exchange's.

package engine

import (
	"context"
	"slices"
	"strconv"
	"strings"
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

	if got := discoveredCount(shard); got != 3 {
		t.Errorf("took on %d announced tokens, want the limit of 3", got)
	}
	if !engine.discoveryClosed.Load() {
		t.Error("discovery is still open after hitting the limit")
	}
	if !mentions(engine.errors.Messages(), "limit of 3") {
		t.Errorf("errors = %v, want the limit recorded", engine.errors.Messages())
	}
}

// A connection has a hard ceiling: past roughly 750 assets the server accepts
// the subscription and silently stops sending snapshots. Announced tokens must
// not push a connection over it, because doing so would cost the tokens that
// were actually requested. Reaching it used to stop discovery outright; now the
// run opens another connection and carries on.
func TestDiscoveryOpensAnotherConnectionAtTheCeiling(t *testing.T) {
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
	armDiscovery(t, engine)
	shard := engine.shards.all()[0]

	// More than the 50 of headroom the first connection has left.
	announced := make(wire.StringList, 0, 80)
	for i := range 80 {
		announced = append(announced, "announced-"+strconv.Itoa(i))
	}
	engine.admitAnnounced(shard, wire.NewMarket{ID: "1", AssetIDs: announced}, time.Now())

	if got := len(shard.trackers); got > config.MaxAssetsCeiling {
		t.Errorf("the first connection holds %d assets, past the ceiling of %d",
			got, config.MaxAssetsCeiling)
	}
	if got := discoveredCount(shard); got != 50 {
		t.Errorf("the first connection took %d announced tokens, want its 50 of headroom", got)
	}

	// The rest went somewhere rather than being dropped.
	if got := engine.shards.count(); got != 2 {
		t.Fatalf("got %d connections, want a second one opened for the overflow", got)
	}
	if got := int(engine.discovered.Load()); got != 80 {
		t.Errorf("%d announced tokens were taken on in total, want all 80", got)
	}
	if engine.discoveryClosed.Load() {
		t.Error("discovery closed itself despite a connection being available")
	}
}

// Growth is bounded. Without a ceiling on connections a run could answer a
// stream of announcements by opening sockets without end.
func TestDiscoveryStopsWhenNoMoreConnectionsMayBeOpened(t *testing.T) {
	rest := testsupport.NewFakeREST(t)

	cfg := websocketConfig("ws://127.0.0.1:1/none", rest.URL())
	cfg.MaxAssetsPerConnection = 10
	cfg.DiscoverLimit = 500

	engine, err := New(Options{Config: cfg, Tokens: tokenlist.List{IDs: scaleIDs(10)}})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	engine.buildShards()
	armDiscovery(t, engine)
	shard := engine.shards.all()[0]

	// Far more than the budget allows connections for.
	for round := range 60 {
		announced := make(wire.StringList, 0, 10)
		for i := range 10 {
			announced = append(announced, "announced-"+strconv.Itoa(round*10+i))
		}
		engine.admitAnnounced(shard, wire.NewMarket{ID: strconv.Itoa(round), AssetIDs: announced}, time.Now())
	}

	if got := engine.shards.count(); got > engine.maxShards() {
		t.Errorf("opened %d connections, past the bound of %d", got, engine.maxShards())
	}
	if !engine.discoveryClosed.Load() {
		t.Error("discovery is still open despite every connection being full")
	}
}

// Zero means off, and an off switch has to actually be off.
func TestDiscoveryCanBeDisabled(t *testing.T) {
	engine := engineForDiscovery(t, 0)
	shard := newShardState(0, []string{"111"}, tracker.Options{})

	engine.admitAnnounced(shard, wire.NewMarket{ID: "1", AssetIDs: wire.StringList{"a", "b"}}, time.Now())

	if discoveredCount(shard) != 0 {
		t.Errorf("took on %d tokens with discovery disabled", discoveredCount(shard))
	}
}

// A token already asked for must not be taken on again as a discovery: it would
// be counted twice and reported in the wrong place.
func TestAnAlreadyRequestedTokenIsNotRediscovered(t *testing.T) {
	engine := engineForDiscovery(t, 100)
	shard := newShardState(0, []string{"111"}, tracker.Options{})

	engine.admitAnnounced(shard, wire.NewMarket{ID: "1", AssetIDs: wire.StringList{"111", "999"}}, time.Now())

	if discoveredCount(shard) != 1 {
		t.Errorf("took on %d tokens, want only the genuinely new one", discoveredCount(shard))
	}
	if shard.trackers["999"] == nil {
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

	if discoveredCount(shard) != 0 {
		t.Errorf("took on %d tokens after the sweep closed discovery", discoveredCount(shard))
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

// A token taken on mid-window has to be asked for again when the connection is
// redialled. Subscriptions do not survive a reconnect, so a shard that resends
// only its original shortlist stops receiving the announced tokens while their
// books keep being reported as current, which is the one outcome the trust
// machinery exists to rule out.
func TestDiscoveredTokensAreResubscribedOnReconnect(t *testing.T) {
	engine := engineForDiscovery(t, 100)
	shard := newShardState(0, []string{"111"}, tracker.Options{})

	engine.admitAnnounced(shard, wire.NewMarket{ID: "1", AssetIDs: wire.StringList{"777", "888"}}, time.Now())

	subscribed := shard.subscribed()
	want := []string{"111", "777", "888"}
	if !slices.Equal(subscribed, want) {
		t.Errorf("a redial would subscribe to %v, want %v", subscribed, want)
	}
	if !slices.Equal(shard.assetIDs, []string{"111"}) {
		t.Errorf("assetIDs = %v, want the requested list unchanged: it is the completeness baseline",
			shard.assetIDs)
	}
}

// The same guarantee end to end: the connection drops after an announcement has
// been taken on, and the subscription the shard sends when it redials has to
// name the announced tokens as well. It is checked on the wire rather than in
// the shard's own state, because the server is what decides whether those tokens
// keep arriving.
func TestAReconnectResubscribesTheAnnouncedTokens(t *testing.T) {
	ws := testsupport.NewFakeWS(t,
		testsupport.WSStep{Send: wsSnapshot111},
		testsupport.WSStep{After: 20 * time.Millisecond, Send: wsNewMarket},
		testsupport.WSStep{After: 20 * time.Millisecond, Action: testsupport.WSDropConnection},
	)
	rest := testsupport.NewFakeREST(t)
	rest.ServeBook("111", levels("0.97", "100"), levels("0.99", "50"))
	rest.ServeBook("777", levels("0.10", "5"), levels("0.90", "5"))
	rest.ServeBook("888", levels("0.90", "5"), levels("0.10", "5"))

	cfg := websocketConfig(ws.URL(), rest.URL())
	// Long enough to outlast the reconnection backoff, which is what this test
	// is here to see the other side of.
	cfg.Duration = cfg.ReconnectInitialBackoff + 500*time.Millisecond

	collector, err := New(Options{Config: cfg, Tokens: tokenlist.List{IDs: []string{"111"}}})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if _, err := collector.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// Opening subscriptions only: the mid-window update carries no type field.
	openings := ws.ReceivedMatching(`"type":"market"`)
	if len(openings) < 2 {
		t.Fatalf("the server saw %d opening subscriptions, want a reconnect: %v", len(openings), ws.Received())
	}

	for _, id := range []string{"111", "777", "888"} {
		if !strings.Contains(openings[1], id) {
			t.Errorf("the resubscription %s leaves out %s, so its book would stop updating "+
				"while still being reported as current", openings[1], id)
		}
	}
}

// discoveredCount reports how many tokens a shard took on from announcements,
// which is everything it tracks beyond the ones it was assigned.
func discoveredCount(s *shardState) int { return len(s.trackers) - len(s.assetIDs) }

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
	armDiscovery(t, engine)

	return engine
}

// armDiscovery supplies the running state a collection would have set, so that
// a test can drive admitAnnounced directly. Growing a connection needs somewhere
// to start it into; in production that is always present, because the goroutine
// calling this was started into it.
func armDiscovery(t *testing.T, engine *Engine) {
	t.Helper()

	engine.initialShards = engine.shards.count()
	engine.running = &collection{
		collectCtx: t.Context(),
		drainCtx:   t.Context(),
		results:    make(chan shardResult, 16),
	}
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
	armDiscovery(t, engine)

	if engine.shards.count() != 1 {
		t.Fatalf("got %d shards, want 1 at exactly the connection width", engine.shards.count())
	}
	shard := engine.shards.all()[0]

	engine.admitAnnounced(shard, wire.NewMarket{
		ID:       "1",
		AssetIDs: wire.StringList{"announced-a", "announced-b"},
	}, time.Now())

	if discoveredCount(shard) != 2 {
		t.Fatalf("took on %d announced tokens on a full shortlist, want 2. errors: %v",
			discoveredCount(shard), engine.errors.Messages())
	}
}

// The feed carries no series field, so following the short-duration markets
// means matching on how they are worded. That belongs to whoever is running the
// scraper, not to this build.
func TestDiscoveryCanBeNarrowedToMatchingAnnouncements(t *testing.T) {
	tests := []struct {
		name     string
		pattern  string
		question string
		slug     string
		wantTake bool
	}{
		{"no pattern takes everything", "", "Some Election 2028", "election", true},
		{"question matches", "(?i)up or down", "Bitcoin Up or Down - Aug 14, 3:15PM ET", "btc-updown", true},
		{"slug matches", "up-or-down", "Bitcoin hourly", "btc-up-or-down", true},
		{"neither matches", "(?i)up or down", "Some Election 2028", "election", false},
		{"case matters unless asked otherwise", "up or down", "Bitcoin Up or Down", "btc", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rest := testsupport.NewFakeREST(t)
			cfg := websocketConfig("ws://127.0.0.1:1/none", rest.URL())
			cfg.DiscoverLimit = 100
			cfg.DiscoverMatch = tt.pattern

			engine, err := New(Options{Config: cfg, Tokens: tokenlist.List{IDs: []string{"111"}}})
			if err != nil {
				t.Fatalf("New returned error: %v", err)
			}
			armDiscovery(t, engine)

			shard := newShardState(0, []string{"111"}, tracker.Options{})
			engine.admitAnnounced(shard, wire.NewMarket{
				ID:       "1",
				Question: tt.question,
				Slug:     tt.slug,
				AssetIDs: wire.StringList{"777"},
			}, time.Now())

			if took := discoveredCount(shard) > 0; took != tt.wantTake {
				t.Errorf("took the announcement = %v, want %v", took, tt.wantTake)
			}
		})
	}
}

// A mistyped expression must stop the run rather than silently matching
// nothing, which would look exactly like a quiet announcement feed.
func TestAnInvalidDiscoveryPatternIsRejected(t *testing.T) {
	rest := testsupport.NewFakeREST(t)
	cfg := websocketConfig("ws://127.0.0.1:1/none", rest.URL())
	cfg.DiscoverMatch = "up or down("

	if _, err := New(Options{Config: cfg, Tokens: tokenlist.List{IDs: []string{"111"}}}); err == nil {
		t.Fatal("New accepted an invalid discovery pattern")
	}
}
