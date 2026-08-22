// Test data: Invented tokens, at the size the consuming agent actually uses. The ids are long
// decimal strings like the real ones on purpose: the subscribe frame's limit is on
// bytes rather than on the number of assets, so short ids would make a shard look
// narrower than it is.

package engine

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/netqo/polymarket-scraper/internal/report"
	"github.com/netqo/polymarket-scraper/internal/testsupport"
	"github.com/netqo/polymarket-scraper/internal/tokenlist"
	"github.com/netqo/polymarket-scraper/internal/tracker"
)

// scaleTokens is the size the specification names, and the size the consuming
// agent actually uses.
const scaleTokens = 400

// scaleIDs builds a token list of the given size.
func scaleIDs(count int) []string {
	ids := make([]string, count)
	for i := range ids {
		// Long decimal ids, like the real ones. The length matters: the
		// subscribe frame's limit is on bytes rather than on the number of
		// assets, so short ids would make a shard look narrower than it is.
		ids[i] = "710000000000000000000000000000000000000000000000000000000000000000000" + strconv.Itoa(100000+i)
	}

	return ids
}

// snapshotFor renders a book snapshot frame for one token.
func snapshotFor(id string) string {
	return fmt.Sprintf(`{"market":"0x%s","asset_id":"%s","timestamp":"1000","hash":"h",`+
		`"bids":[{"price":"0.97","size":"100"},{"price":"0.96","size":"200"}],`+
		`"asks":[{"price":"0.99","size":"50"},{"price":"0.98","size":"75"}],`+
		`"tick_size":"0.001","event_type":"book"}`, id[len(id)-6:], id)
}

// E1 is a MUST at this size, and until now the largest test used ten tokens.
// It is also the first case that produces more than one connection, so the
// sharding path had never actually run in a test.
func TestScaleAcrossShards(t *testing.T) {
	ids := scaleIDs(scaleTokens)

	// The initial snapshot arrives as one array holding every subscribed asset,
	// which at this size is also the largest frame the reader ever handles.
	snapshots := make([]string, len(ids))
	for i, id := range ids {
		snapshots[i] = snapshotFor(id)
	}

	ws := testsupport.NewFakeWS(t,
		testsupport.WSStep{Send: "[" + strings.Join(snapshots, ",") + "]"},
	)
	rest := testsupport.NewFakeREST(t)
	for _, id := range ids {
		rest.ServeBook(id, levels("0.97", "100"), levels("0.99", "50"))
	}

	cfg := websocketConfig(ws.URL(), rest.URL())
	cfg.Duration = 2 * time.Second
	cfg.Grace = 2 * time.Second
	// The real width, so the shard count matches what a production run would
	// use rather than being forced by the test.
	cfg.MaxAssetsPerConnection = 150
	cfg.RESTBatchSize = 250

	collector, err := New(Options{Config: cfg, Tokens: tokenlist.List{IDs: ids}})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	document, err := runWithin(t, collector, cfg.Duration+cfg.Grace)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if document.Connection.WSConnections != 3 {
		t.Errorf("WSConnections = %d, want 400 tokens at a width of 150 to produce 3",
			document.Connection.WSConnections)
	}
	if len(document.Books) != scaleTokens {
		t.Fatalf("got %d books, want %d", len(document.Books), scaleTokens)
	}

	ratio := float64(document.TokensOK) / float64(document.TokensRequested)
	if ratio < 0.95 {
		t.Errorf("%d/%d tokens ok (%.1f%%), want at least 95%%. errors: %v",
			document.TokensOK, document.TokensRequested, ratio*100, document.Errors)
	}

	// Every shard has to report, or its tokens are recorded as failures and the
	// ratio above would have caught it. Checking the statuses directly says
	// which failure it was.
	statuses := make(map[string]int)
	for _, token := range document.Books {
		statuses[token.Status]++
	}
	if statuses[string(tracker.StatusOK)] != scaleTokens {
		t.Errorf("statuses = %v, want all %d ok", statuses, scaleTokens)
	}
}

// Books are bounded, so memory has to scale with the number of tokens and not
// with how long the run went on or how many updates arrived. That is the part
// of E1 a correctness test cannot see.
func TestMemoryDoesNotGrowWithUpdateCount(t *testing.T) {
	const (
		tokens  = 50
		updates = 200
	)

	ids := scaleIDs(tokens)

	script := []testsupport.WSStep{
		{Send: "[" + strings.Join(snapshotsFor(ids), ",") + "]"},
	}
	// A steady stream of updates against the same levels, which is what a
	// liquid market looks like and what would grow an unbounded accumulator.
	for i := range updates {
		script = append(script, testsupport.WSStep{
			After: time.Millisecond,
			Send:  changeFor(ids[i%tokens], i),
		})
	}

	ws := testsupport.NewFakeWS(t, script...)
	rest := testsupport.NewFakeREST(t)
	for _, id := range ids {
		rest.ServeBook(id, levels("0.97", "100"), levels("0.99", "50"))
	}

	cfg := websocketConfig(ws.URL(), rest.URL())
	cfg.Duration = 2 * time.Second
	cfg.Grace = time.Second

	collector, err := New(Options{Config: cfg, Tokens: tokenlist.List{IDs: ids}})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	document, err := runWithin(t, collector, cfg.Duration+cfg.Grace)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	applied := 0
	for _, token := range document.Books {
		applied += token.UpdatesApplied
	}
	if applied == 0 {
		t.Fatal("no updates were applied, so this measures nothing")
	}

	// A generous ceiling: the point is to catch retention that scales with the
	// update stream, not to pin an exact allocation figure that would make the
	// test fail on unrelated changes.
	const ceiling = 64 << 20
	growth := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if growth > ceiling {
		t.Errorf("the heap grew %d bytes across %d applied updates, want under %d",
			growth, applied, ceiling)
	}

	t.Logf("%d updates applied, heap grew %d bytes", applied, growth)
}

func snapshotsFor(ids []string) []string {
	frames := make([]string, len(ids))
	for i, id := range ids {
		frames[i] = snapshotFor(id)
	}

	return frames
}

// changeFor renders a price change for one token, varying the size so each is
// a distinct update rather than a duplicate the tracker would drop.
func changeFor(id string, seq int) string {
	return fmt.Sprintf(`{"market":"0xmarket","price_changes":[`+
		`{"asset_id":"%s","price":"0.97","size":"%d","side":"BUY","hash":"h%d",`+
		`"best_bid":"0.97","best_ask":"0.99"}],"timestamp":"%d","event_type":"price_change"}`,
		id, 100+seq, seq, 2000+seq)
}

// runWithin runs a collection and fails if it overruns its own budget, which is
// the guarantee the whole shutdown timeline exists to provide.
func runWithin(t *testing.T, collector *Engine, budget time.Duration) (document report.Document, err error) {
	t.Helper()

	type outcome struct {
		document report.Document
		err      error
	}
	done := make(chan outcome, 1)

	started := time.Now()
	go func() {
		doc, runErr := collector.Run(t.Context())
		done <- outcome{doc, runErr}
	}()

	select {
	case result := <-done:
		if elapsed := time.Since(started); elapsed > budget {
			t.Errorf("the run took %v, want it inside its %v budget", elapsed, budget)
		}
		return result.document, result.err

	case <-time.After(budget * 2):
		t.Fatalf("the run did not finish within twice its %v budget", budget)
		return document, nil
	}
}
