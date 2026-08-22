//go:build live

// Test data: real, which is the point. Everything else in this package runs
// against in-process fakes, because the failures worth testing are ones the
// exchange will not perform on request. This file covers the other half: whether
// the design matches the exchange at all.
//
// These tests reach the real Polymarket API. They are a manual gate, never a CI
// gate, and they skip themselves unless POLYMARKET_LIVE_TOKENS names a token
// file.
//
// They exist because the offline suite can only prove the scraper behaves as
// designed against a server that behaves as expected. What it cannot prove is
// that the design matches the exchange, and that is precisely where the
// expensive surprises have been: the published documentation gets the order of
// both sides of the book backwards, the initial snapshot arrives in a different
// framing from every other message, and a subscription past a certain size is
// accepted and then silently not honoured. Every one of those was found by
// running against production and would have passed a fully mocked suite.
//
// Run with: POLYMARKET_LIVE_TOKENS=/path/to/tokens.txt make test-live

package engine

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/netqo/polymarket-scraper/internal/config"
	"github.com/netqo/polymarket-scraper/internal/report"
	"github.com/netqo/polymarket-scraper/internal/restclient"
	"github.com/netqo/polymarket-scraper/internal/tokenlist"
	"github.com/netqo/polymarket-scraper/internal/tracker"
)

// liveTokens loads the operator-supplied token list, skipping if there is none.
func liveTokens(t *testing.T) tokenlist.List {
	t.Helper()

	path := os.Getenv("POLYMARKET_LIVE_TOKENS")
	if path == "" {
		t.Skip("set POLYMARKET_LIVE_TOKENS to a token file to run the live acceptance tests")
	}

	tokens, err := tokenlist.Load(path)
	if err != nil {
		t.Fatalf("loading %s: %v", path, err)
	}

	return tokens
}

// liveRun collects against the real exchange for the given window.
func liveRun(t *testing.T, tokens tokenlist.List, window time.Duration) report.Document {
	t.Helper()

	cfg := config.New()
	cfg.TokensPath = os.Getenv("POLYMARKET_LIVE_TOKENS")
	cfg.OutPath = "books.json"
	cfg.Duration = window

	collector, err := New(Options{Config: cfg, Tokens: tokens})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	document, err := collector.Run(context.Background())
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	return document
}

// H1: the happy path against the real exchange.
func TestLiveHappyPath(t *testing.T) {
	tokens := liveTokens(t)
	document := liveRun(t, tokens, 30*time.Second)

	if len(document.Books) < len(tokens.IDs) {
		t.Fatalf("got %d books for %d requested tokens", len(document.Books), len(tokens.IDs))
	}

	for id, token := range document.Books {
		if token.Status != string(tracker.StatusOK) {
			t.Logf("token %s: %s %v", id, token.Status, token.Flags)
			continue
		}

		if token.ExchangeTimestamp == nil || token.ReceivedAt == nil {
			t.Errorf("token %s is ok but missing a timestamp", id)
		}
		assertOutputOrder(t, id, token)
	}

	t.Logf("%d/%d ok, %d reconnects, %d REST requests, %d errors",
		document.TokensOK, document.TokensRequested,
		document.Connection.Reconnects, document.Connection.RESTRequests, len(document.Errors))
	for _, message := range document.Errors {
		t.Logf("  %s", message)
	}
}

// The ordering the published documentation gets backwards, checked against the
// real feed rather than against a fake that was built from the same
// understanding as the code.
func assertOutputOrder(t *testing.T, id string, token report.Token) {
	t.Helper()

	for i := 1; i < len(token.Bids); i++ {
		if lessThan(token.Bids[i-1].Price.Raw(), token.Bids[i].Price.Raw()) {
			t.Errorf("token %s bids are not descending at %d: %q then %q",
				id, i, token.Bids[i-1].Price.Raw(), token.Bids[i].Price.Raw())
			return
		}
	}
	for i := 1; i < len(token.Asks); i++ {
		if lessThan(token.Asks[i].Price.Raw(), token.Asks[i-1].Price.Raw()) {
			t.Errorf("token %s asks are not ascending at %d: %q then %q",
				id, i, token.Asks[i-1].Price.Raw(), token.Asks[i].Price.Raw())
			return
		}
	}
}

// H3: the configured scale, in the configured window.
func TestLiveScale(t *testing.T) {
	tokens := liveTokens(t)
	if len(tokens.IDs) < 100 {
		t.Skipf("only %d tokens supplied; this check wants a few hundred", len(tokens.IDs))
	}

	started := time.Now()
	document := liveRun(t, tokens, 90*time.Second)
	elapsed := time.Since(started)

	if elapsed > 120*time.Second {
		t.Errorf("a 90s window took %v, want it inside the 90s plus 30s grace budget", elapsed)
	}

	ratio := float64(document.TokensOK) / float64(document.TokensRequested)
	if ratio < 0.95 {
		t.Errorf("%d/%d tokens ok (%.1f%%), want at least 95%%. errors: %v",
			document.TokensOK, document.TokensRequested, ratio*100, document.Errors)
	}

	t.Logf("%d/%d ok in %v across %d connections, %d reconnects",
		document.TokensOK, document.TokensRequested, elapsed,
		document.Connection.WSConnections, document.Connection.Reconnects)
}

// H8: values reach the document as the exchange's own text, checked by fetching
// the same books separately and comparing strings.
func TestLiveValuesAreVerbatim(t *testing.T) {
	tokens := liveTokens(t)
	document := liveRun(t, tokens, 15*time.Second)

	client, err := restclient.New(restclient.Options{
		BaseURL: config.DefaultRESTURL,
		Rate:    config.DefaultRESTRate,
	})
	if err != nil {
		t.Fatalf("building a REST client: %v", err)
	}

	checked := 0
	for _, id := range tokens.IDs {
		if checked >= 3 {
			break
		}
		token := document.Books[id]
		if token.Status != string(tracker.StatusOK) || len(token.Bids) == 0 {
			continue
		}

		fetched, err := client.Book(context.Background(), id)
		if err != nil {
			t.Logf("token %s could not be re-fetched: %v", id, err)
			continue
		}
		checked++

		// Prices move, so only levels present in both are comparable. What is
		// under test is the text, not the market.
		sizes := make(map[string]string, len(fetched.Bids))
		for _, level := range fetched.Bids {
			sizes[level.Price.Raw()] = level.Size.Raw()
		}

		compared := 0
		for _, level := range token.Bids {
			size, present := sizes[level.Price.Raw()]
			if !present {
				continue
			}
			compared++
			if size != level.Size.Raw() {
				t.Logf("token %s level %s: size %q here, %q on re-fetch (the book moved)",
					id, level.Price.Raw(), level.Size.Raw(), size)
			}
		}
		if compared == 0 {
			t.Logf("token %s: no overlapping levels to compare", id)
		} else {
			t.Logf("token %s: %d levels compared string for string", id, compared)
		}
	}

	if checked == 0 {
		t.Skip("no token produced a comparable book")
	}
}

// H7: the short-duration series creates instances constantly, so a window run
// during an active period should see announcements. It reports rather than
// fails when it does not, since that depends on what the exchange is doing.
func TestLiveMarketChurn(t *testing.T) {
	tokens := liveTokens(t)
	document := liveRun(t, tokens, 60*time.Second)

	t.Logf("%d markets announced, %d resolved, %d tokens picked up mid-window",
		len(document.Events.NewMarkets), len(document.Events.Resolved), document.TokensDiscovered)

	for _, announced := range document.Events.NewMarkets {
		t.Logf("  new: %s (%d tokens)", announced.Question, len(announced.AssetIDs))
	}

	if len(document.Events.NewMarkets) == 0 {
		t.Log("no announcements during this window; run again during an active crypto period")
	}
}

// lessThan compares two decimal strings numerically, independently of the
// production comparison, so the ordering check is a real cross-check.
func lessThan(a, b string) bool {
	return parseFloat(a) < parseFloat(b)
}

func parseFloat(s string) float64 {
	var value float64
	var fraction float64 = 1
	seenDot := false

	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '.':
			seenDot = true
		case s[i] >= '0' && s[i] <= '9':
			digit := float64(s[i] - '0')
			if seenDot {
				fraction /= 10
				value += digit * fraction
			} else {
				value = value*10 + digit
			}
		}
	}

	return value
}
