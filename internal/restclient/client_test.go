package restclient

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/netqo/polymarket-scraper/internal/book"
	"github.com/netqo/polymarket-scraper/internal/decimal"
	"github.com/netqo/polymarket-scraper/internal/testsupport"
	"github.com/netqo/polymarket-scraper/internal/wire"
)

// fastClient points at a fake and runs at a rate high enough that pacing does
// not slow the suite down, except where the pacing itself is under test.
func fastClient(t *testing.T, fake *testsupport.FakeREST) *Client {
	t.Helper()

	client, err := New(Options{
		BaseURL:       fake.URL(),
		Rate:          10_000,
		Attempts:      3,
		MaxRetryAfter: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	return client
}

func levels(price, size string) []book.Level {
	return []book.Level{{Price: decimal.Parse(price), Size: decimal.Parse(size)}}
}

func TestNewRejectsAnUnusableConfiguration(t *testing.T) {
	tests := []struct {
		name string
		opts Options
	}{
		{"no host", Options{BaseURL: "/book", Rate: 10}},
		{"unparseable url", Options{BaseURL: "://nope", Rate: 10}},
		{"zero rate", Options{BaseURL: "https://example.test", Rate: 0}},
		{"negative rate", Options{BaseURL: "https://example.test", Rate: -1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.opts); err == nil {
				t.Fatal("New accepted an unusable configuration")
			}
		})
	}
}

func TestBookFetchesAToken(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	fake.ServeBook("111", levels("0.97", "100"), levels("0.99", "50"))

	got, err := fastClient(t, fake).Book(context.Background(), "111")
	if err != nil {
		t.Fatalf("Book returned error: %v", err)
	}

	if got.AssetID != "111" {
		t.Errorf("AssetID = %q, want 111", got.AssetID)
	}
	if len(got.Bids) != 1 || got.Bids[0].Price.Raw() != "0.97" {
		t.Errorf("bids = %v", got.Bids)
	}
	// The three fields the websocket never sends are the reason REST is part of
	// every run rather than only a fallback.
	if got.MinOrderSize.Raw() != "5" || got.TickSize.Raw() != "0.001" || got.NegRisk == nil {
		t.Errorf("metadata = %q, %q, %v", got.MinOrderSize.Raw(), got.TickSize.Raw(), got.NegRisk)
	}
}

// A token the exchange has never heard of is a fact about the token, not a
// setback, so it is reported distinctly and never retried.
func TestBookReportsAnUnknownTokenDistinctlyAndDoesNotRetryIt(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	fake.Serve("111", testsupport.RESTBehaviour{NotFound: true})

	client := fastClient(t, fake)
	_, err := client.Book(context.Background(), "111")

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Book returned %v, want ErrNotFound", err)
	}
	if fake.Requests() != 1 {
		t.Errorf("the client made %d requests for an unknown token, want 1", fake.Requests())
	}
}

func TestBookRetriesATransientFailure(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	fake.Serve("111", testsupport.RESTBehaviour{
		FailTimes: 2,
		Status:    http.StatusInternalServerError,
		Book:      aBook("111"),
	})

	if _, err := fastClient(t, fake).Book(context.Background(), "111"); err != nil {
		t.Fatalf("Book returned error after retries: %v", err)
	}
	if fake.Requests() != 3 {
		t.Errorf("the client made %d requests, want 3", fake.Requests())
	}
}

// An unbounded retry is indistinguishable from a hang, and the run has a hard
// deadline to meet.
func TestBookGivesUpAfterTheConfiguredAttempts(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	fake.Serve("111", testsupport.RESTBehaviour{AlwaysFail: true})

	client, err := New(Options{BaseURL: fake.URL(), Rate: 10_000, Attempts: 3})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if _, err := client.Book(context.Background(), "111"); err == nil {
		t.Fatal("Book succeeded against a server that always fails")
	}
	if fake.Requests() != 3 {
		t.Errorf("the client made %d requests, want exactly 3", fake.Requests())
	}
}

func TestBookHonoursRetryAfter(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	fake.Serve("111", testsupport.RESTBehaviour{
		FailTimes:         1,
		Status:            http.StatusTooManyRequests,
		RetryAfterSeconds: 1,
		Book:              aBook("111"),
	})

	started := time.Now()
	if _, err := fastClient(t, fake).Book(context.Background(), "111"); err != nil {
		t.Fatalf("Book returned error: %v", err)
	}

	if waited := time.Since(started); waited < time.Second {
		t.Errorf("the client waited %v, want at least the requested 1s", waited)
	}
}

func TestBookReportsAMalformedResponse(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	fake.Serve("111", testsupport.RESTBehaviour{Malformed: true})

	if _, err := fastClient(t, fake).Book(context.Background(), "111"); err == nil {
		t.Fatal("Book accepted a malformed response")
	}
}

// This is what makes a whole-shortlist REST pass cheap: hundreds of tokens in
// one call rather than hundreds of calls.
func TestBooksFetchesABatchInOneRequest(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	for _, id := range []string{"111", "222", "333"} {
		fake.ServeBook(id, levels("0.97", "100"), nil)
	}

	got, err := fastClient(t, fake).Books(context.Background(), []string{"111", "222", "333"})
	if err != nil {
		t.Fatalf("Books returned error: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("got %d books, want 3", len(got))
	}
	if fake.Requests() != 1 {
		t.Errorf("the client made %d requests for a batch, want 1", fake.Requests())
	}
	if sizes := fake.BatchSizes(); len(sizes) != 1 || sizes[0] != 3 {
		t.Errorf("batch sizes = %v, want [3]", sizes)
	}
}

// The response omits tokens the exchange does not recognise, so a caller must
// not assume it gets one book back per id it asked for.
func TestBooksOmitsUnknownTokensRatherThanFailing(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	fake.ServeBook("111", levels("0.97", "100"), nil)
	fake.Serve("222", testsupport.RESTBehaviour{NotFound: true})

	got, err := fastClient(t, fake).Books(context.Background(), []string{"111", "222"})
	if err != nil {
		t.Fatalf("Books returned error: %v", err)
	}

	if len(got) != 1 || got[0].AssetID != "111" {
		t.Errorf("got %d books, want only the recognised one", len(got))
	}
}

func TestBooksOnAnEmptyListMakesNoRequest(t *testing.T) {
	fake := testsupport.NewFakeREST(t)

	got, err := fastClient(t, fake).Books(context.Background(), nil)
	if err != nil {
		t.Fatalf("Books returned error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d books, want none", len(got))
	}
	if fake.Requests() != 0 {
		t.Errorf("the client made %d requests for an empty batch, want 0", fake.Requests())
	}
}

// The pacing applies across everything the client does, retries included, so
// backoff and the rate ceiling compose rather than racing each other.
func TestRequestsArePaced(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	for _, id := range []string{"111", "222", "333"} {
		fake.ServeBook(id, levels("0.97", "100"), nil)
	}

	client, err := New(Options{BaseURL: fake.URL(), Rate: 20})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	started := time.Now()
	for _, id := range []string{"111", "222", "333"} {
		if _, err := client.Book(context.Background(), id); err != nil {
			t.Fatalf("Book(%s) returned error: %v", id, err)
		}
	}

	// Twenty per second with a burst of one means at least 50ms between the
	// second and third requests, so three requests take at least 100ms.
	if elapsed := time.Since(started); elapsed < 100*time.Millisecond {
		t.Errorf("three requests at 20/s took %v, want at least 100ms", elapsed)
	}
}

// A run that is ending must not be held up by a client waiting to retry.
func TestCancellationStopsTheClientPromptly(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	fake.Serve("111", testsupport.RESTBehaviour{AlwaysFail: true})

	client, err := New(Options{BaseURL: fake.URL(), Rate: 10_000, Attempts: 10})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	started := time.Now()
	if _, err := client.Book(ctx, "111"); err == nil {
		t.Fatal("Book succeeded despite cancellation")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("the client took %v to notice cancellation", elapsed)
	}
}

func TestRequestsCounterIncludesRetries(t *testing.T) {
	fake := testsupport.NewFakeREST(t)
	fake.Serve("111", testsupport.RESTBehaviour{FailTimes: 1, Book: aBook("111")})

	client := fastClient(t, fake)
	if _, err := client.Book(context.Background(), "111"); err != nil {
		t.Fatalf("Book returned error: %v", err)
	}

	if got := client.Requests(); got != 2 {
		t.Errorf("Requests() = %d, want 2", got)
	}
}

// tunedClient builds a client with the retry policy spelled out, so the tests
// below assert against known numbers rather than whatever the defaults are.
func tunedClient(t *testing.T, initial, maximum, retryAfter time.Duration) *Client {
	t.Helper()

	client, err := New(Options{
		BaseURL:        "https://example.test",
		Rate:           10,
		InitialBackoff: initial,
		MaxBackoff:     maximum,
		MaxRetryAfter:  retryAfter,
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	return client
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	const (
		initial = 250 * time.Millisecond
		maximum = 4 * time.Second
	)
	client := tunedClient(t, initial, maximum, 0)

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{2, initial},
		{3, 2 * initial},
		{4, 4 * initial},
		{9, maximum},
	}

	for _, tt := range tests {
		if got := client.backoffFor(tt.attempt, nil); got != tt.want {
			t.Errorf("backoffFor(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

// The policy is configuration now, so a client told to wait differently has to
// actually wait differently.
func TestBackoffFollowsTheConfiguredBounds(t *testing.T) {
	client := tunedClient(t, time.Second, 2*time.Second, 0)

	if got := client.backoffFor(2, nil); got != time.Second {
		t.Errorf("first backoff = %v, want the configured 1s", got)
	}
	if got := client.backoffFor(9, nil); got != 2*time.Second {
		t.Errorf("capped backoff = %v, want the configured 2s ceiling", got)
	}
}

// A ceiling below the floor would shorten the first wait rather than lengthen
// the later ones, which is the opposite of a backoff.
func TestBackoffCeilingIsRaisedToTheFloor(t *testing.T) {
	client := tunedClient(t, 2*time.Second, time.Second, 0)

	if got := client.backoffFor(2, nil); got != 2*time.Second {
		t.Errorf("first backoff = %v, want the floor honoured despite a lower ceiling", got)
	}
}

func TestBackoffPrefersTheServersOwnInstruction(t *testing.T) {
	client := tunedClient(t, 250*time.Millisecond, 4*time.Second, 10*time.Second)
	err := &statusError{Code: http.StatusTooManyRequests, RetryAfter: 3 * time.Second}

	if got := client.backoffFor(2, err); got != 3*time.Second {
		t.Errorf("backoffFor with Retry-After = %v, want 3s", got)
	}
}

// A cooperative client waits, but not past the point where waiting costs more
// than the data is worth: the run has a deadline of its own.
func TestRetryAfterIsCapped(t *testing.T) {
	const cap = 10 * time.Second
	client := tunedClient(t, 250*time.Millisecond, 4*time.Second, cap)

	response := &http.Response{Header: http.Header{}}
	response.Header.Set("Retry-After", "3600")

	if got := client.retryAfter(response); got != cap {
		t.Errorf("retryAfter = %v, want it capped at %v", got, cap)
	}
}

// Zero is a way of saying the header should not be obeyed at all, which is what
// a run with a very short deadline wants.
func TestRetryAfterCanBeDisabled(t *testing.T) {
	client := tunedClient(t, 250*time.Millisecond, 4*time.Second, 0)

	response := &http.Response{Header: http.Header{}}
	response.Header.Set("Retry-After", "5")

	if got := client.retryAfter(response); got != 0 {
		t.Errorf("retryAfter = %v, want 0 when the cap is 0", got)
	}
}

func TestRetryAfterIgnoresWhatItCannotRead(t *testing.T) {
	client := tunedClient(t, 250*time.Millisecond, 4*time.Second, 10*time.Second)

	tests := []string{"", "Wed, 21 Oct 2026 07:28:00 GMT", "-1", "0", "soon"}

	for _, value := range tests {
		response := &http.Response{Header: http.Header{}}
		response.Header.Set("Retry-After", value)

		if got := client.retryAfter(response); got != 0 {
			t.Errorf("retryAfter(%q) = %v, want 0", value, got)
		}
	}
}

// aBook builds a minimal successful response body for a token.
func aBook(tokenID string) wire.RESTBook {
	return wire.RESTBook{
		Market:       "0x" + tokenID,
		AssetID:      tokenID,
		Timestamp:    "1786728198766",
		TickSize:     decimal.Parse("0.001"),
		MinOrderSize: decimal.Parse("5"),
	}
}
