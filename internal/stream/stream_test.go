package stream

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/netqo/polymarket-scraper/internal/decimal"
	"github.com/netqo/polymarket-scraper/internal/tracker"
)

// A fixed origin, so every test reads as offsets from a known point.
var t0 = time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)

// open starts a stream in a temporary directory.
func open(t *testing.T) (*Writer, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "changes.jsonl")
	writer, err := New(Options{
		Path:      path,
		Run:       "abc123",
		StartedAt: t0,
		Now:       func() time.Time { return t0 },
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	return writer, path
}

// lines reads the stream back as decoded records.
//
// Each line is decoded on its own, which is the property the format exists for:
// a reader tailing the file can act on a record the moment it lands rather than
// waiting for the run to finish.
func lines(t *testing.T, path string) []map[string]any {
	t.Helper()

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the stream: %v", err)
	}

	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
		if line == "" {
			continue
		}

		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line %q does not parse on its own: %v", line, err)
		}
		records = append(records, record)
	}

	return records
}

func TestTheStreamOpensWithAHeader(t *testing.T) {
	_, path := open(t)

	records := lines(t, path)
	if len(records) != 1 {
		t.Fatalf("got %d records, want just the header", len(records))
	}

	header := records[0]
	checks := map[string]any{
		"kind":       KindHeader,
		"version":    Version,
		"run":        "abc123",
		"started_at": "2026-08-14T15:00:00.000Z",
	}
	for key, want := range checks {
		if got := header[key]; got != want {
			t.Errorf("header %s = %v, want %v", key, got, want)
		}
	}
}

func TestQuotesAreWrittenAsTheyHappen(t *testing.T) {
	writer, path := open(t)

	writer.Quoted("111", tracker.Quote{
		Bid:               decimal.Parse("0.978"),
		Ask:               decimal.Parse("0.982"),
		Spread:            decimal.Parse("0.004"),
		Mid:               decimal.Parse("0.98"),
		ExchangeTimestamp: "1786554102412",
	})

	// Read before anything is closed. That is the whole guarantee.
	records := lines(t, path)
	if len(records) != 2 {
		t.Fatalf("got %d records, want the header and the quote", len(records))
	}

	quote := records[1]
	checks := map[string]any{
		"kind":               KindQuote,
		"token":              "111",
		"bid":                "0.978",
		"ask":                "0.982",
		"spread":             "0.004",
		"mid":                "0.98",
		"exchange_timestamp": "1786554102412",
		"at":                 "2026-08-14T15:00:00.000Z",
	}
	for key, want := range checks {
		if got := quote[key]; got != want {
			t.Errorf("quote %s = %v, want %v", key, got, want)
		}
	}
}

// C6 applies here as much as to the document: whatever the API said is what
// comes out, with no floating point anywhere near it.
func TestPricesSurviveVerbatim(t *testing.T) {
	writer, path := open(t)

	writer.Quoted("111", tracker.Quote{
		Bid: decimal.Parse("0.9819999999999999"),
		Ask: decimal.Parse("0.980"),
	})

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the stream: %v", err)
	}

	for _, want := range []string{`"bid":"0.9819999999999999"`, `"ask":"0.980"`} {
		if !strings.Contains(string(contents), want) {
			t.Errorf("stream does not contain %s", want)
		}
	}
	if strings.Contains(string(contents), "0.98199999999999995") {
		t.Error("stream shows a floating-point artifact")
	}
}

// A one-sided book has no spread. Absent rather than zero, because zero is a
// spread a reader would act on.
func TestAbsentValuesAreOmittedRatherThanZeroed(t *testing.T) {
	writer, path := open(t)

	writer.Quoted("111", tracker.Quote{Bid: decimal.Parse("0.97")})

	quote := lines(t, path)[1]
	for _, key := range []string{"ask", "spread", "mid"} {
		if got, present := quote[key]; present {
			t.Errorf("%s = %v, want it left out entirely", key, got)
		}
	}
	if quote["bid"] != "0.97" {
		t.Errorf("bid = %v, want 0.97", quote["bid"])
	}
}

func TestTradesAndFlagsAreWritten(t *testing.T) {
	writer, path := open(t)

	writer.Traded("111", tracker.LastTrade{
		Price:      decimal.Parse("0.98"),
		Size:       decimal.Parse("120"),
		Side:       "BUY",
		FeeRateBPS: decimal.Parse("0"),
		Timestamp:  "1786554100000",
	})
	writer.Flagged("222", tracker.FlagDeltaGap)

	records := lines(t, path)
	if len(records) != 3 {
		t.Fatalf("got %d records, want the header, a trade and a flag", len(records))
	}

	trade := records[1]
	if trade["kind"] != KindTrade || trade["price"] != "0.98" || trade["side"] != "BUY" {
		t.Errorf("trade = %v", trade)
	}
	if trade["fee_rate_bps"] != "0" {
		t.Errorf("fee rate = %v, want 0, which is the reason trades are collected", trade["fee_rate_bps"])
	}

	flag := records[2]
	if flag["kind"] != KindFlag || flag["token"] != "222" || flag["flag"] != string(tracker.FlagDeltaGap) {
		t.Errorf("flag = %v", flag)
	}
}

func TestAnnouncementsAreWritten(t *testing.T) {
	writer, path := open(t)

	writer.Announced("Bitcoin Up or Down", "0xcond", []string{"777", "888"}, []string{"Up", "Down"}, "1786728400000")
	writer.Resolved("0xmarket", []string{"111", "222"}, "111", "Yes", "1786790415550")

	records := lines(t, path)
	if len(records) != 3 {
		t.Fatalf("got %d records, want the header and both announcements", len(records))
	}

	if records[1]["kind"] != KindMarket || records[1]["question"] != "Bitcoin Up or Down" {
		t.Errorf("market = %v", records[1])
	}
	if records[2]["kind"] != KindResolved || records[2]["winning_outcome"] != "Yes" {
		t.Errorf("resolved = %v", records[2])
	}
}

// The engine reports from a goroutine per connection. Two records interleaving
// would produce a line that parses as neither.
func TestConcurrentWritesDoNotInterleave(t *testing.T) {
	writer, path := open(t)

	var wg sync.WaitGroup
	for token := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				writer.Quoted(string(rune('a'+token)), tracker.Quote{
					Bid: decimal.Parse("0.97"),
					Ask: decimal.Parse("0.99"),
				})
			}
		}()
	}
	wg.Wait()

	// lines fails the test if any line does not parse on its own.
	if got := len(lines(t, path)); got != 401 {
		t.Errorf("got %d records, want the header plus 400 quotes", got)
	}
	if writer.Dropped() != 0 {
		t.Errorf("%d records were dropped", writer.Dropped())
	}
}

// A run is usually one of a series, and the header plus the run identifier is
// what separates them within one file.
func TestTheStreamAppendsAcrossRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "changes.jsonl")

	for _, run := range []string{"first", "second"} {
		writer, err := New(Options{Path: path, Run: run, StartedAt: t0, Now: func() time.Time { return t0 }})
		if err != nil {
			t.Fatalf("New returned error: %v", err)
		}
		writer.Flagged("111", tracker.FlagSnapshotOnly)
		if err := writer.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	}

	records := lines(t, path)
	if len(records) != 4 {
		t.Fatalf("got %d records, want two runs of two", len(records))
	}
	if records[0]["run"] != "first" || records[2]["run"] != "second" {
		t.Errorf("run identifiers = %v and %v", records[0]["run"], records[2]["run"])
	}
}

// A stream names the tokens a run collected and what they were priced at, so it
// is no wider than the document it accompanies.
func TestTheStreamIsOwnerOnly(t *testing.T) {
	_, path := open(t)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != fileMode {
		t.Errorf("mode = %o, want %o", perm, fileMode)
	}
}

// A reader that does not know the stream is incomplete will assume it is
// complete, which is the mistake the output document goes to some length to
// prevent.
func TestDroppedRecordsAreCounted(t *testing.T) {
	writer, _ := open(t)

	// Closing underneath the writer is the cheapest stand-in for a full disk.
	if err := writer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	writer.Flagged("111", tracker.FlagDeltaGap)

	if writer.Dropped() != 1 {
		t.Errorf("Dropped() = %d, want 1", writer.Dropped())
	}
}

func TestAnUnwritablePathIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-directory", "changes.jsonl")

	if _, err := New(Options{Path: path}); err == nil {
		t.Fatal("New accepted an unwritable path")
	} else if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the path", err)
	}
}

// The Writer is what the tracker reports through, so it has to satisfy the
// interface without the engine having to adapt it.
func TestTheWriterIsAnObserver(t *testing.T) {
	writer, _ := open(t)

	var _ tracker.Observer = writer
}
