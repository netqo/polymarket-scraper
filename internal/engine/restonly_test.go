package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/netqo/polymarket-scraper/internal/book"
	"github.com/netqo/polymarket-scraper/internal/config"
	"github.com/netqo/polymarket-scraper/internal/decimal"
	"github.com/netqo/polymarket-scraper/internal/report"
	"github.com/netqo/polymarket-scraper/internal/testsupport"
	"github.com/netqo/polymarket-scraper/internal/tokenlist"
	"github.com/netqo/polymarket-scraper/internal/tracker"
)

func levels(price, size string) []book.Level {
	return []book.Level{{Price: decimal.Parse(price), Size: decimal.Parse(size)}}
}

// restOnlyConfig is a configuration that collects over REST against a fake, at
// a rate high enough that pacing does not slow the suite down.
func restOnlyConfig(restURL string) config.Config {
	cfg := config.New()
	cfg.TokensPath = "tokens.txt"
	cfg.OutPath = "books.json"
	cfg.RESTOnly = true
	cfg.RESTURL = restURL
	cfg.RESTRate = 10_000
	cfg.RESTBatchSize = 2
	cfg.Duration = 2 * time.Second

	return cfg
}

// runRESTOnlyOver collects the given tokens against the fake and returns the
// document.
func runRESTOnlyOver(t *testing.T, fake *testsupport.FakeREST, ids ...string) report.Document {
	t.Helper()

	collector, err := New(Options{
		Config: restOnlyConfig(fake.URL()),
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

// H1, the happy path: known tokens, everything ok, the book in output order,
// both timestamps present.
func TestHappyPath(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	ids := []string{"111", "222", "333", "444", "555"}
	for _, id := range ids {
		fake.ServeBook(id, levels("0.97", "100"), levels("0.99", "50"))
	}

	document := runRESTOnlyOver(t, fake, ids...)

	if document.TokensRequested != 5 || document.TokensOK != 5 {
		t.Fatalf("counts = %d requested, %d ok; want 5 and 5", document.TokensRequested, document.TokensOK)
	}

	for _, id := range ids {
		token, present := document.Books[id]
		if !present {
			t.Fatalf("token %s is missing from the document", id)
		}
		if token.Status != string(tracker.StatusOK) {
			t.Errorf("token %s status = %q, want ok", id, token.Status)
		}
		if token.Source == nil || *token.Source != string(tracker.SourceRESTOnly) {
			t.Errorf("token %s source = %v, want rest_only", id, token.Source)
		}
		if token.ExchangeTimestamp == nil || token.ReceivedAt == nil {
			t.Errorf("token %s is missing a timestamp: exchange %v, received %v",
				id, token.ExchangeTimestamp, token.ReceivedAt)
		}
		if len(token.Bids) != 1 || len(token.Asks) != 1 {
			t.Errorf("token %s book = %d bids, %d asks", id, len(token.Bids), len(token.Asks))
		}
		if token.MinOrderSize.Absent() || token.NegRisk == nil {
			t.Errorf("token %s is missing the metadata only REST can supply", id)
		}
	}
}

// H2: a malformed token id must reach the document with a failure status, and
// must not disturb the tokens around it.
func TestOneBadTokenDoesNotDisturbTheOthers(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	good := []string{"111", "222", "333", "444"}
	for _, id := range good {
		fake.ServeBook(id, levels("0.97", "100"), levels("0.99", "50"))
	}
	fake.Serve("not-a-token", testsupport.RESTBehaviour{NotFound: true})

	document := runRESTOnlyOver(t, fake, append(good, "not-a-token")...)

	if document.TokensOK != 4 {
		t.Errorf("TokensOK = %d, want the 4 good tokens", document.TokensOK)
	}

	bad, present := document.Books["not-a-token"]
	if !present {
		t.Fatal("the malformed token is missing from the document entirely")
	}
	if bad.Status == string(tracker.StatusOK) {
		t.Error("the malformed token was reported as ok")
	}
	if !containsFlag(bad.Flags, tracker.FlagTokenNotFound) {
		t.Errorf("flags = %v, want %q", bad.Flags, tracker.FlagTokenNotFound)
	}
	if len(bad.Bids) != 0 || len(bad.Asks) != 0 {
		t.Error("the malformed token was reported with a book")
	}

	for _, id := range good {
		if document.Books[id].Status != string(tracker.StatusOK) {
			t.Errorf("token %s was disturbed by the bad one: status %q", id, document.Books[id].Status)
		}
	}
}

// Asking for hundreds of tokens must not mean hundreds of requests.
func TestTokensAreFetchedInBatches(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	ids := make([]string, 0, 10)
	for i := range 10 {
		id := string(rune('a' + i))
		ids = append(ids, id)
		fake.ServeBook(id, levels("0.97", "100"), nil)
	}

	document := runRESTOnlyOver(t, fake, ids...)

	if document.TokensOK != 10 {
		t.Fatalf("TokensOK = %d, want 10", document.TokensOK)
	}
	// Ten tokens at a batch size of two is five requests, not ten.
	if sizes := fake.BatchSizes(); len(sizes) != 5 {
		t.Errorf("made %d batch requests, want 5: %v", len(sizes), sizes)
	}
	if document.Connection.RESTRequests != 5 {
		t.Errorf("RESTRequests = %d, want 5", document.Connection.RESTRequests)
	}
}

// A token the batch could not answer for gets an individual request, which is
// what turns "absent from the response" into a specific per-token answer.
func TestTokensMissingFromABatchAreFetchedIndividually(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	fake.ServeBook("111", levels("0.97", "100"), nil)
	fake.Serve("222", testsupport.RESTBehaviour{NotFound: true})

	document := runRESTOnlyOver(t, fake, "111", "222")

	// One batch request, then one individual request for the token it omitted.
	if got := document.Connection.RESTRequests; got != 2 {
		t.Errorf("RESTRequests = %d, want 2", got)
	}
	if !containsFlag(document.Books["222"].Flags, tracker.FlagTokenNotFound) {
		t.Errorf("flags = %v, want the individual fetch to have identified the token",
			document.Books["222"].Flags)
	}
}

// A token we asked for and could not get is a failure, not an absence of
// liquidity. The two must never be confused.
func TestATokenThatCannotBeFetchedIsReportedAsAFailure(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	fake.ServeBook("111", levels("0.97", "100"), nil)
	fake.Serve("222", testsupport.RESTBehaviour{AlwaysFail: true})

	document := runRESTOnlyOver(t, fake, "111", "222")

	failed := document.Books["222"]
	if failed.Status != string(tracker.StatusResyncFailed) {
		t.Errorf("status = %q, want %q", failed.Status, tracker.StatusResyncFailed)
	}
	if failed.Source != nil {
		t.Errorf("source = %q, want null: there is no trustworthy book", *failed.Source)
	}
	if len(failed.Bids) != 0 || len(failed.Asks) != 0 {
		t.Error("a token that could not be fetched was reported with a book")
	}

	if !mentions(document.Errors, "222") {
		t.Errorf("errors = %v, want the failing token named", document.Errors)
	}
}

// A live token with nothing resting on it is a real answer, and it must not
// look like a failure.
func TestAnEmptyBookIsSuccess(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	fake.ServeBook("111", nil, nil)

	document := runRESTOnlyOver(t, fake, "111")

	token := document.Books["111"]
	if token.Status != string(tracker.StatusOK) {
		t.Errorf("status = %q, want ok: an empty book is not a failure", token.Status)
	}
	if token.Bids == nil || token.Asks == nil {
		t.Error("an empty book serialized its sides as null rather than []")
	}
	if len(token.Bids) != 0 || len(token.Asks) != 0 {
		t.Errorf("book = %d bids, %d asks; want both empty", len(token.Bids), len(token.Asks))
	}
}

// C4: every requested token appears, whatever happened, and duplicates are
// collapsed rather than reported twice.
func TestEveryRequestedTokenAppearsExactlyOnce(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	fake.ServeBook("111", levels("0.97", "100"), nil)

	tokens, err := tokenlist.Parse([]byte("111\n222\n111\n"))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	collector, err := New(Options{Config: restOnlyConfig(fake.URL()), Tokens: tokens})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	document, err := collector.Run(context.Background())
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if len(document.Books) != 2 {
		t.Errorf("got %d books, want 2 after collapsing the duplicate", len(document.Books))
	}
	if document.TokensRequested != 2 {
		t.Errorf("TokensRequested = %d, want 2", document.TokensRequested)
	}
	if !mentions(document.Errors, "duplicate") {
		t.Errorf("errors = %v, want the collapsed duplicate recorded", document.Errors)
	}
}

// A run that is cut short still produces an honest document: the tokens it did
// not reach say so, which is far more useful than an empty output path.
func TestAnInterruptedRunStillProducesADocument(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	fake.ServeBook("111", levels("0.97", "100"), nil)
	fake.ServeBook("222", levels("0.97", "100"), nil)

	collector, err := New(Options{
		Config: restOnlyConfig(fake.URL()),
		Tokens: tokenlist.List{IDs: []string{"111", "222"}},
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

	if len(document.Books) != 2 {
		t.Fatalf("got %d books, want both tokens reported", len(document.Books))
	}
	for id, token := range document.Books {
		if token.Status == string(tracker.StatusOK) {
			t.Errorf("token %s is ok despite the run being cancelled before it started", id)
		}
	}
}

// H6: the document's shape must not depend on how the run went.
func TestTheDocumentIsAlwaysWellFormed(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	fake.ServeBook("111", levels("0.97", "100"), nil)
	fake.Serve("222", testsupport.RESTBehaviour{AlwaysFail: true})
	fake.Serve("333", testsupport.RESTBehaviour{NotFound: true})

	document := runRESTOnlyOver(t, fake, "111", "222", "333")

	if document.SchemaVersion != report.SchemaVersion {
		t.Errorf("SchemaVersion = %q, want %q", document.SchemaVersion, report.SchemaVersion)
	}
	if document.WindowSeconds != 2 {
		t.Errorf("WindowSeconds = %d, want 2", document.WindowSeconds)
	}
	if document.Events.NewMarkets == nil || document.Events.Resolved == nil {
		t.Error("the events block has a nil list, which would serialize as null")
	}
	if document.Errors == nil {
		t.Error("the error list is nil, which would serialize as null")
	}
	if document.Connection.WSConnections != 0 {
		t.Errorf("WSConnections = %d, want 0 in a REST-only run", document.Connection.WSConnections)
	}
}

// The websocket path does not exist yet, and a build that cannot do what was
// asked has to say so rather than quietly doing something else.
func TestTheWebsocketPathReportsThatItIsNotImplemented(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	cfg := restOnlyConfig(fake.URL())
	cfg.RESTOnly = false

	collector, err := New(Options{Config: cfg, Tokens: tokenlist.List{IDs: []string{"111"}}})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if _, err := collector.Run(context.Background()); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Run returned %v, want ErrNotImplemented", err)
	}
}

func TestChunk(t *testing.T) {
	tests := []struct {
		name string
		ids  []string
		size int
		want int
	}{
		{"exact multiple", []string{"a", "b", "c", "d"}, 2, 2},
		{"with a remainder", []string{"a", "b", "c"}, 2, 2},
		{"one batch", []string{"a", "b"}, 10, 1},
		{"empty", nil, 2, 0},
		{"invalid size", []string{"a"}, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(chunk(tt.ids, tt.size)); got != tt.want {
				t.Errorf("chunk produced %d batches, want %d", got, tt.want)
			}
		})
	}
}

func containsFlag(flags []string, want tracker.Flag) bool {
	for _, flag := range flags {
		if flag == string(want) {
			return true
		}
	}

	return false
}

func mentions(messages []string, substring string) bool {
	for _, message := range messages {
		if strings.Contains(message, substring) {
			return true
		}
	}

	return false
}
