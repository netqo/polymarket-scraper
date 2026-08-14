package engine

import (
	"context"
	"testing"
	"time"

	"github.com/netqo/polymarket-scraper/internal/config"
	"github.com/netqo/polymarket-scraper/internal/report"
	"github.com/netqo/polymarket-scraper/internal/testsupport"
	"github.com/netqo/polymarket-scraper/internal/tokenlist"
	"github.com/netqo/polymarket-scraper/internal/tracker"
)

// Frames the fake market channel plays. Written out rather than generated, so a
// failing test shows the exact bytes that produced it.
const (
	wsSnapshot111 = `{"market":"0xmarket","asset_id":"111","timestamp":"1000","hash":"h1",` +
		`"bids":[{"price":"0.97","size":"100"}],"asks":[{"price":"0.99","size":"50"}],` +
		`"tick_size":"0.001","event_type":"book"}`

	wsSnapshot222 = `{"market":"0xmarket","asset_id":"222","timestamp":"1000","hash":"h2",` +
		`"bids":[{"price":"0.40","size":"10"}],"asks":[{"price":"0.60","size":"10"}],` +
		`"tick_size":"0.01","event_type":"book"}`

	wsChange111 = `{"market":"0xmarket","price_changes":[` +
		`{"asset_id":"111","price":"0.98","size":"250","side":"BUY","hash":"c1",` +
		`"best_bid":"0.98","best_ask":"0.99"}],"timestamp":"1001","event_type":"price_change"}`
)

// websocketConfig collects over the websocket against the two fakes, with
// timings compressed so the suite runs quickly.
func websocketConfig(wsURL, restURL string) config.Config {
	cfg := config.New()
	cfg.TokensPath = "tokens.txt"
	cfg.OutPath = "books.json"
	cfg.WSURL = wsURL
	cfg.RESTURL = restURL
	cfg.RESTRate = 10_000
	cfg.RESTBatchSize = 50
	cfg.Duration = 400 * time.Millisecond
	cfg.Grace = 400 * time.Millisecond
	cfg.PingInterval = 20 * time.Millisecond
	cfg.IdleTimeout = 150 * time.Millisecond
	cfg.RESTOnly = false

	return cfg
}

// tokenListOf builds a token list for a test.
func tokenListOf(ids ...string) tokenlist.List {
	return tokenlist.List{IDs: ids}
}

// collectOver runs a full websocket collection and returns the document.
func collectOver(t *testing.T, ws *testsupport.FakeWS, rest *testsupport.FakeREST, ids ...string) report.Document {
	t.Helper()

	collector, err := New(Options{
		Config: websocketConfig(ws.URL(), rest.URL()),
		Tokens: tokenlist.List{IDs: ids},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	document, err := collector.Run(context.Background())
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	return document
}

func TestWebsocketHappyPath(t *testing.T) {
	ws := testsupport.NewFakeWS(t,
		testsupport.WSStep{Send: "[" + wsSnapshot111 + "," + wsSnapshot222 + "]"},
		testsupport.WSStep{After: 20 * time.Millisecond, Send: wsChange111},
	)
	rest := testsupport.NewFakeREST(t)
	rest.ServeBook("111", levels("0.97", "100"), levels("0.99", "50"))
	rest.ServeBook("222", levels("0.40", "10"), levels("0.60", "10"))

	document := collectOver(t, ws, rest, "111", "222")

	if document.TokensOK != 2 {
		t.Fatalf("TokensOK = %d, want 2. errors: %v", document.TokensOK, document.Errors)
	}

	token := document.Books["111"]
	if token.Source == nil || *token.Source != string(tracker.SourceWS) {
		t.Errorf("source = %v, want ws", token.Source)
	}
	if token.UpdatesApplied != 1 {
		t.Errorf("UpdatesApplied = %d, want the one price change", token.UpdatesApplied)
	}
	if len(token.Bids) != 2 || token.Bids[0].Price.Raw() != "0.98" {
		t.Errorf("bids = %v, want the update on top", token.Bids)
	}
	// The metadata only REST can supply must be present even on a healthy
	// websocket run, which is why the seed is unconditional.
	if token.MinOrderSize.Absent() || token.NegRisk == nil {
		t.Errorf("token 111 is missing REST-only metadata: min_order_size %q, neg_risk %v",
			token.MinOrderSize.Raw(), token.NegRisk)
	}
	if document.Connection.WSConnections != 1 {
		t.Errorf("WSConnections = %d, want 1", document.Connection.WSConnections)
	}
}

// H4, the most important test in the suite, in its four variants. Every one of
// them asserts the same thing: after a gap, no token is reported as ok while
// carrying a book from before the gap. That is the failure that turns into a
// phantom arbitrage signal and a real losing trade.
func TestABookIsNeverReportedAsCurrentAfterAGap(t *testing.T) {
	tests := []struct {
		name string

		// script is what the market channel does; every variant seeds a real
		// book first, so there is genuinely something that could leak.
		script []testsupport.WSStep

		// restAvailable decides whether the re-seed can succeed.
		restAvailable bool

		wantStatus tracker.Status
		wantSource string
		wantFlag   tracker.Flag
	}{
		{
			name: "dropped connection, re-seed succeeds over REST",
			script: []testsupport.WSStep{
				{Send: wsSnapshot111},
				{After: 30 * time.Millisecond, Action: testsupport.WSDropConnection},
			},
			restAvailable: true,
			wantStatus:    tracker.StatusOK,
			wantSource:    string(tracker.SourceWSResync),
			wantFlag:      tracker.FlagDisconnected,
		},
		{
			name: "dropped connection, re-seed fails",
			script: []testsupport.WSStep{
				{Send: wsSnapshot111},
				{After: 30 * time.Millisecond, Action: testsupport.WSDropConnection},
			},
			restAvailable: false,
			wantStatus:    tracker.StatusResyncFailed,
			wantSource:    "",
			wantFlag:      tracker.FlagDisconnected,
		},
		{
			name: "connection goes silent, re-seed succeeds",
			script: []testsupport.WSStep{
				{Send: wsSnapshot111},
				{After: 20 * time.Millisecond, Action: testsupport.WSGoSilent},
			},
			restAvailable: true,
			wantStatus:    tracker.StatusOK,
			wantSource:    string(tracker.SourceWSResync),
			wantFlag:      tracker.FlagDisconnected,
		},
		{
			name: "connection goes silent, re-seed fails",
			script: []testsupport.WSStep{
				{Send: wsSnapshot111},
				{After: 20 * time.Millisecond, Action: testsupport.WSGoSilent},
			},
			restAvailable: false,
			wantStatus:    tracker.StatusResyncFailed,
			wantSource:    "",
			wantFlag:      tracker.FlagDisconnected,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := testsupport.NewFakeWS(t, tt.script...)
			rest := testsupport.NewFakeREST(t)
			if tt.restAvailable {
				// A visibly different book, so a re-seed cannot be mistaken for
				// the original having survived.
				rest.ServeBook("111", levels("0.50", "7"), levels("0.55", "7"))
			} else {
				rest.Serve("111", testsupport.RESTBehaviour{AlwaysFail: true})
			}

			document := collectOver(t, ws, rest, "111")
			token := document.Books["111"]

			if token.Status != string(tt.wantStatus) {
				t.Fatalf("status = %q, want %q. errors: %v", token.Status, tt.wantStatus, document.Errors)
			}

			source := ""
			if token.Source != nil {
				source = *token.Source
			}
			if source != tt.wantSource {
				t.Errorf("source = %q, want %q", source, tt.wantSource)
			}
			if !containsFlag(token.Flags, tt.wantFlag) {
				t.Errorf("flags = %v, want %q", token.Flags, tt.wantFlag)
			}

			// The assertion that matters, stated the same way in every variant.
			if token.Status == string(tracker.StatusOK) && source == string(tracker.SourceWS) {
				t.Error("a token that lived through a gap is reported as ok, attributed to the websocket, " +
					"which means its pre-gap book is being presented as current")
			}
			if token.Status != string(tracker.StatusOK) && (len(token.Bids) != 0 || len(token.Asks) != 0) {
				t.Errorf("a failed token was reported with a book: %d bids, %d asks",
					len(token.Bids), len(token.Asks))
			}
			// The pre-gap price must not survive into a re-seeded book.
			for _, level := range token.Bids {
				if level.Price.Raw() == "0.97" {
					t.Error("the pre-gap book survived into the re-seeded one")
				}
			}
		})
	}
}

// A disconnect distrusts every token on the connection at once. Fetched one at
// a time that would take most of a window; batched it is a couple of requests,
// which is the difference between recovering and not.
func TestAMassResyncIsBatched(t *testing.T) {
	ws := testsupport.NewFakeWS(t,
		testsupport.WSStep{Send: "[" + wsSnapshot111 + "," + wsSnapshot222 + "]"},
		testsupport.WSStep{After: 30 * time.Millisecond, Action: testsupport.WSDropConnection},
	)
	rest := testsupport.NewFakeREST(t)
	rest.ServeBook("111", levels("0.50", "7"), levels("0.55", "7"))
	rest.ServeBook("222", levels("0.30", "7"), levels("0.35", "7"))

	document := collectOver(t, ws, rest, "111", "222")

	if document.TokensOK != 2 {
		t.Fatalf("TokensOK = %d, want both recovered. errors: %v", document.TokensOK, document.Errors)
	}

	// The metadata seed is one batch, and the re-seed of both tokens should be
	// another. Anything approaching one request per token means the batching
	// did not happen.
	for _, size := range rest.BatchSizes() {
		if size < 1 {
			t.Errorf("a batch request carried %d tokens", size)
		}
	}
	if requests := document.Connection.RESTRequests; requests > 4 {
		t.Errorf("the run made %d REST requests for 2 tokens, want the re-seed batched", requests)
	}
	if document.Connection.Reconnects < 1 {
		t.Errorf("Reconnects = %d, want at least 1", document.Connection.Reconnects)
	}
}

// A token nobody ever sent a book for still has to reach the document, and the
// last-chance sweep is what gives it one more opportunity first.
func TestTokensThatNeverArriveAreSweptThenReported(t *testing.T) {
	ws := testsupport.NewFakeWS(t,
		testsupport.WSStep{Action: testsupport.WSIgnoreSubscription},
	)
	rest := testsupport.NewFakeREST(t)
	rest.ServeBook("111", levels("0.50", "7"), levels("0.55", "7"))
	rest.Serve("222", testsupport.RESTBehaviour{AlwaysFail: true})

	document := collectOver(t, ws, rest, "111", "222")

	if len(document.Books) != 2 {
		t.Fatalf("got %d books, want both tokens present", len(document.Books))
	}
	// The seed and sweep between them should have rescued the token REST can
	// serve, without the websocket ever sending anything.
	if document.Books["111"].Status != string(tracker.StatusOK) {
		t.Errorf("token 111 status = %q, want ok via the REST seed. errors: %v",
			document.Books["111"].Status, document.Errors)
	}
	if document.Books["222"].Status == string(tracker.StatusOK) {
		t.Error("token 222 is ok despite nothing ever serving it a book")
	}
}

// A connection that can never be opened means the tokens on it were never
// subscribed, which is a different thing to report than a subscription that
// worked and then broke.
func TestTokensOnAConnectionThatNeverOpensAreReportedAsFailures(t *testing.T) {
	rest := testsupport.NewFakeREST(t)
	rest.Serve("111", testsupport.RESTBehaviour{AlwaysFail: true})

	cfg := websocketConfig("ws://127.0.0.1:1/nothing-here", rest.URL())
	collector, err := New(Options{Config: cfg, Tokens: tokenlist.List{IDs: []string{"111"}}})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	document, err := collector.Run(context.Background())
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	token := document.Books["111"]
	if token.Status == string(tracker.StatusOK) {
		t.Error("a token on a connection that never opened is reported as ok")
	}
	if len(document.Errors) == 0 {
		t.Error("a run that could never connect recorded no errors")
	}
}

// A4: the run has to end within its window plus grace whatever happens, and it
// has to end by finishing rather than by being killed.
func TestTheRunEndsWithinItsBudget(t *testing.T) {
	ws := testsupport.NewFakeWS(t,
		testsupport.WSStep{Send: wsSnapshot111},
		// The connection then stays open and silent for the rest of the run,
		// which is the case a cooperative shutdown is least likely to notice.
		testsupport.WSStep{After: 10 * time.Millisecond, Action: testsupport.WSGoSilent},
	)
	rest := testsupport.NewFakeREST(t)
	rest.ServeBook("111", levels("0.50", "7"), levels("0.55", "7"))

	cfg := websocketConfig(ws.URL(), rest.URL())

	halted := make(chan struct{}, 1)
	collector, err := New(Options{
		Config: cfg,
		Tokens: tokenlist.List{IDs: []string{"111"}},
		Halt:   func() { halted <- struct{}{} },
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	started := time.Now()
	document, err := collector.Run(context.Background())
	elapsed := time.Since(started)

	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if budget := cfg.Duration + cfg.Grace; elapsed > budget {
		t.Errorf("the run took %v, want it inside the %v budget", elapsed, budget)
	}
	select {
	case <-halted:
		t.Error("the watchdog fired: the run had to be terminated rather than finishing")
	default:
	}
	if len(document.Books) != 1 {
		t.Errorf("got %d books, want 1", len(document.Books))
	}
}

// I9: an interrupted run still writes an honest document. The tokens it did not
// reach say so, which is far more useful to a consumer than an empty file.
func TestAnInterruptedWebsocketRunStillProducesADocument(t *testing.T) {
	ws := testsupport.NewFakeWS(t, testsupport.WSStep{Send: wsSnapshot111})
	rest := testsupport.NewFakeREST(t)
	rest.ServeBook("111", levels("0.50", "7"), levels("0.55", "7"))

	collector, err := New(Options{
		Config: websocketConfig(ws.URL(), rest.URL()),
		Tokens: tokenlist.List{IDs: []string{"111"}},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	document, err := collector.Run(ctx)
	if err != nil {
		t.Fatalf("Run returned error on a cancelled context: %v", err)
	}
	if len(document.Books) != 1 {
		t.Fatalf("got %d books, want the requested token reported", len(document.Books))
	}
	if document.SchemaVersion != report.SchemaVersion {
		t.Errorf("SchemaVersion = %q, want a complete document", document.SchemaVersion)
	}
}

// A frame that cannot be decoded means an update was lost for a token we cannot
// identify, so every token on the connection is re-seeded rather than carrying
// on with a book that may have diverged.
func TestAnUndecodableFrameCausesAReSeed(t *testing.T) {
	ws := testsupport.NewFakeWS(t,
		testsupport.WSStep{Send: wsSnapshot111},
		testsupport.WSStep{After: 20 * time.Millisecond, Send: `{"no_discriminator":true}`},
	)
	rest := testsupport.NewFakeREST(t)
	rest.ServeBook("111", levels("0.50", "7"), levels("0.55", "7"))

	document := collectOver(t, ws, rest, "111")

	token := document.Books["111"]
	if !containsFlag(token.Flags, tracker.FlagDecodeError) {
		t.Errorf("flags = %v, want %q", token.Flags, tracker.FlagDecodeError)
	}
	if token.Status == string(tracker.StatusOK) {
		source := ""
		if token.Source != nil {
			source = *token.Source
		}
		if source == string(tracker.SourceWS) {
			t.Error("a token whose frame could not be decoded is still attributed to the websocket")
		}
	}
	if !mentions(document.Errors, "could not be decoded") {
		t.Errorf("errors = %v, want the decode failure recorded", document.Errors)
	}
}
