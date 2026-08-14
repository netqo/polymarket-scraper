// Package testsupport provides in-process stand-ins for the Polymarket
// endpoints.
//
// Every test in this repository runs against these rather than the network. A
// suite that reaches the real API is a suite that fails when the network does,
// passes when the exchange happens to agree with a stale assumption, and cannot
// reproduce a disconnect on demand. The behaviours that matter most here, a
// connection dropping mid-window and a re-seed failing, are ones the real API
// will not perform on request.
package testsupport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"time"

	"github.com/netqo/polymarket-scraper/internal/book"
	"github.com/netqo/polymarket-scraper/internal/decimal"
	"github.com/netqo/polymarket-scraper/internal/wire"
)

// RESTBehaviour describes how the fake responds for one token.
type RESTBehaviour struct {
	// Book is returned on success.
	Book wire.RESTBook

	// NotFound makes every request for this token return 404, which is how the
	// exchange reports a token id it does not recognise.
	NotFound bool

	// FailTimes makes the first n requests for this token fail with Status
	// before succeeding, which is how a transient outage is reproduced.
	FailTimes int

	// Status is the failure status used by FailTimes and AlwaysFail. Zero means
	// 500.
	Status int

	// RetryAfterSeconds is sent with the failure status, so the client's
	// handling of a server-supplied wait can be exercised.
	RetryAfterSeconds int

	// AlwaysFail makes every request fail, which is how an exhausted retry
	// budget is reproduced.
	AlwaysFail bool

	// Malformed makes the response body unparseable.
	Malformed bool

	// Delay holds the response, for exercising client timeouts.
	Delay time.Duration
}

// FakeREST is an in-process stand-in for the CLOB REST endpoints.
type FakeREST struct {
	server *httptest.Server

	mu         sync.Mutex
	behaviours map[string]*RESTBehaviour
	failures   map[string]int
	requests   int
	batchSizes []int
	requestsAt []time.Time
}

// NewFakeREST starts a fake server. It is shut down when the test ends.
func NewFakeREST(t interface{ Cleanup(func()) }) *FakeREST {
	fake := &FakeREST{
		behaviours: make(map[string]*RESTBehaviour),
		failures:   make(map[string]int),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/book", fake.handleBook)
	mux.HandleFunc("/books", fake.handleBooks)

	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)

	return fake
}

// URL is the base to point a client at.
func (f *FakeREST) URL() string { return f.server.URL }

// Serve registers how the fake responds for a token.
func (f *FakeREST) Serve(tokenID string, behaviour RESTBehaviour) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.behaviours[tokenID] = &behaviour
}

// ServeBook is the common case: a token that answers with a two-sided book.
func (f *FakeREST) ServeBook(tokenID string, bids, asks []book.Level) {
	negRisk := false

	f.Serve(tokenID, RESTBehaviour{
		Book: wire.RESTBook{
			Market:       "0x" + tokenID,
			AssetID:      tokenID,
			Timestamp:    "1786728198766",
			Hash:         "hash-" + tokenID,
			Bids:         bids,
			Asks:         asks,
			MinOrderSize: decimal.Parse("5"),
			TickSize:     decimal.Parse("0.001"),
			NegRisk:      &negRisk,
		},
	})
}

// Requests reports how many HTTP requests the fake has served.
func (f *FakeREST) Requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.requests
}

// BatchSizes reports how many token ids each batch request asked for, so a test
// can assert that batching actually happened.
func (f *FakeREST) BatchSizes() []int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]int(nil), f.batchSizes...)
}

// RequestTimes reports when each request arrived, for pacing assertions.
func (f *FakeREST) RequestTimes() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]time.Time(nil), f.requestsAt...)
}

func (f *FakeREST) handleBook(w http.ResponseWriter, r *http.Request) {
	f.noteRequest()

	tokenID := r.URL.Query().Get("token_id")

	behaviour, delay, status := f.decide(tokenID)
	if delay > 0 {
		time.Sleep(delay)
	}
	if status != 0 {
		f.writeFailure(w, behaviour, status)
		return
	}
	if behaviour == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if behaviour.Malformed {
		_, _ = w.Write([]byte("{not json"))
		return
	}

	writeJSON(w, behaviour.Book)
}

func (f *FakeREST) handleBooks(w http.ResponseWriter, r *http.Request) {
	f.noteRequest()

	var requested []wire.BookRequest
	if err := json.NewDecoder(r.Body).Decode(&requested); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	f.batchSizes = append(f.batchSizes, len(requested))
	f.mu.Unlock()

	books := make([]wire.RESTBook, 0, len(requested))
	for _, request := range requested {
		behaviour, delay, status := f.decide(request.TokenID)
		if delay > 0 {
			time.Sleep(delay)
		}

		// A token the exchange does not recognise is simply absent from the
		// batch response rather than reported, which is why the caller cannot
		// assume one book back per id it asked for. Any other failure fails the
		// whole request, because there is one status code to give.
		if behaviour == nil || behaviour.NotFound {
			continue
		}
		if status != 0 {
			f.writeFailure(w, behaviour, status)
			return
		}

		books = append(books, behaviour.Book)
	}

	writeJSON(w, books)
}

// noteRequest records that an HTTP request arrived. It counts requests rather
// than the tokens inside them, which is the number a client's own counter and
// its pacing are measured against.
func (f *FakeREST) noteRequest() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.requests++
	f.requestsAt = append(f.requestsAt, time.Now())
}

// decide works out how to respond for one token.
func (f *FakeREST) decide(tokenID string) (behaviour *RESTBehaviour, delay time.Duration, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	behaviour = f.behaviours[tokenID]
	if behaviour == nil {
		return nil, 0, 0
	}

	if behaviour.NotFound {
		return behaviour, behaviour.Delay, http.StatusNotFound
	}
	if behaviour.AlwaysFail {
		return behaviour, behaviour.Delay, failureStatus(behaviour)
	}
	if f.failures[tokenID] < behaviour.FailTimes {
		f.failures[tokenID]++
		return behaviour, behaviour.Delay, failureStatus(behaviour)
	}

	return behaviour, behaviour.Delay, 0
}

func (f *FakeREST) writeFailure(w http.ResponseWriter, behaviour *RESTBehaviour, status int) {
	if behaviour != nil && behaviour.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(behaviour.RetryAfterSeconds))
	}
	w.WriteHeader(status)
}

func failureStatus(behaviour *RESTBehaviour) int {
	if behaviour.Status != 0 {
		return behaviour.Status
	}

	return http.StatusInternalServerError
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}
