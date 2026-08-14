package report

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/netqo/polymarket-scraper/internal/book"
	"github.com/netqo/polymarket-scraper/internal/decimal"
	"github.com/netqo/polymarket-scraper/internal/tracker"
)

var (
	started  = time.Date(2026, 8, 14, 15, 0, 12, 0, time.UTC)
	finished = time.Date(2026, 8, 14, 15, 1, 44, 120*int(time.Millisecond), time.UTC)
)

func okSnapshot(id string) tracker.Snapshot {
	negRisk := false

	return tracker.Snapshot{
		TokenID:     id,
		Status:      tracker.StatusOK,
		Source:      tracker.SourceWS,
		ConditionID: "0xabc",
		Bids: []book.Level{
			{Price: decimal.Parse("0.978"), Size: decimal.Parse("1500")},
		},
		Asks: []book.Level{
			{Price: decimal.Parse("0.982"), Size: decimal.Parse("1900")},
		},
		TickSize:          decimal.Parse("0.001"),
		MinOrderSize:      decimal.Parse("5"),
		NegRisk:           &negRisk,
		ExchangeTimestamp: "1786554102412",
		ReceivedAt:        time.Date(2026, 8, 14, 15, 1, 42, 410*int(time.Millisecond), time.UTC),
		UpdatesApplied:    41,
		Flags:             []tracker.Flag{},
	}
}

func baseInput() Input {
	return Input{
		StartedAt:  started,
		FinishedAt: finished,
		Window:     90 * time.Second,
		Requested:  []string{"111", "222"},
		Snapshots:  map[string]tracker.Snapshot{"111": okSnapshot("111")},
	}
}

// C4, and the reason the builder iterates the requested list rather than the
// results: a token cannot silently disappear because a shard died. Completeness
// is a property of the loop, not of the pipeline having succeeded.
func TestEveryRequestedTokenAppearsEvenWhenNothingWasCollected(t *testing.T) {
	in := baseInput()
	in.Requested = []string{"111", "222", "333"}

	doc := Build(in)

	if len(doc.Books) != 3 {
		t.Fatalf("got %d books, want 3", len(doc.Books))
	}
	for _, id := range in.Requested {
		if _, present := doc.Books[id]; !present {
			t.Errorf("token %s is missing from the document", id)
		}
	}

	missing := doc.Books["333"]
	if missing.Status != string(tracker.StatusNoData) {
		t.Errorf("status for an uncollected token = %q, want %q", missing.Status, tracker.StatusNoData)
	}
	if missing.Source != nil {
		t.Errorf("source = %q, want null", *missing.Source)
	}
	if doc.TokensRequested != 3 || doc.TokensOK != 1 {
		t.Errorf("counts = %d requested, %d ok; want 3 and 1", doc.TokensRequested, doc.TokensOK)
	}
}

func TestDiscoveredTokensExtendTheDocumentWithoutDisturbingTheCounts(t *testing.T) {
	in := baseInput()
	discovered := okSnapshot("999")
	discovered.Flags = []tracker.Flag{tracker.FlagDiscoveredMidWindow}
	in.Discovered = []tracker.Snapshot{discovered}

	doc := Build(in)

	if doc.TokensRequested != 2 {
		t.Errorf("TokensRequested = %d, want the input count unchanged", doc.TokensRequested)
	}
	if doc.TokensDiscovered != 1 {
		t.Errorf("TokensDiscovered = %d, want 1", doc.TokensDiscovered)
	}
	if len(doc.Books) != 3 {
		t.Errorf("got %d books, want the requested two plus one discovered", len(doc.Books))
	}
	// The health ratio a consumer computes must stay meaningful: a token the
	// run picked up on its own is not one that was asked for, and counting it
	// here would let tokens_ok exceed tokens_requested.
	if doc.TokensOK != 1 {
		t.Errorf("TokensOK = %d, want only the requested token that succeeded", doc.TokensOK)
	}
	if doc.TokensOK > doc.TokensRequested {
		t.Errorf("TokensOK %d exceeds TokensRequested %d", doc.TokensOK, doc.TokensRequested)
	}
}

// Stated as a property, because the only way it broke was by counting over the
// wrong collection, and that is easy to reintroduce.
func TestTokensOKNeverExceedsTokensRequested(t *testing.T) {
	in := baseInput()
	in.Requested = []string{"111", "222"}
	in.Snapshots = map[string]tracker.Snapshot{
		"111": okSnapshot("111"),
		"222": okSnapshot("222"),
	}
	for _, id := range []string{"901", "902", "903"} {
		in.Discovered = append(in.Discovered, okSnapshot(id))
	}

	doc := Build(in)

	if doc.TokensOK != 2 {
		t.Errorf("TokensOK = %d, want the 2 requested tokens", doc.TokensOK)
	}
	if doc.TokensOK > doc.TokensRequested {
		t.Errorf("TokensOK %d exceeds TokensRequested %d", doc.TokensOK, doc.TokensRequested)
	}
	if doc.TokensDiscovered != 3 {
		t.Errorf("TokensDiscovered = %d, want 3", doc.TokensDiscovered)
	}
	if len(doc.Books) != 5 {
		t.Errorf("got %d books, want all 5 reported", len(doc.Books))
	}
}

// A token that was requested and then also announced must not be counted twice
// or overwrite its own entry.
func TestDiscoveryDoesNotDuplicateARequestedToken(t *testing.T) {
	in := baseInput()
	in.Discovered = []tracker.Snapshot{okSnapshot("111")}

	doc := Build(in)

	if doc.TokensDiscovered != 0 {
		t.Errorf("TokensDiscovered = %d, want 0: the token was already requested", doc.TokensDiscovered)
	}
	if len(doc.Books) != 2 {
		t.Errorf("got %d books, want 2", len(doc.Books))
	}
}

// The window is what was configured, not the wall clock length of the run: the
// difference between them is shutdown, and reporting that as the window would
// misstate how long the data was gathered over.
func TestWindowSecondsReportsTheConfiguredWindow(t *testing.T) {
	in := baseInput()
	in.Window = 90 * time.Second

	doc := Build(in)
	if doc.WindowSeconds != 90 {
		t.Errorf("WindowSeconds = %d, want 90", doc.WindowSeconds)
	}
}

func TestTimestampsUseOneConventionForOurClockAndPassTheFeedThrough(t *testing.T) {
	doc := Build(baseInput())

	if doc.StartedAt != "2026-08-14T15:00:12.000Z" {
		t.Errorf("StartedAt = %q", doc.StartedAt)
	}
	if doc.FinishedAt != "2026-08-14T15:01:44.120Z" {
		t.Errorf("FinishedAt = %q", doc.FinishedAt)
	}

	token := doc.Books["111"]
	if token.ReceivedAt == nil || *token.ReceivedAt != "2026-08-14T15:01:42.410Z" {
		t.Errorf("ReceivedAt = %v", token.ReceivedAt)
	}
	if token.ExchangeTimestamp == nil || *token.ExchangeTimestamp != "1786554102412" {
		t.Errorf("ExchangeTimestamp = %v, want the feed value verbatim", token.ExchangeTimestamp)
	}
}

// D3: a list that is empty is [], and a value that is unknown is null. Neither
// is ever absent, and neither is ever the other.
func TestEmptyIsNotTheSameAsUnknown(t *testing.T) {
	doc := Build(baseInput())

	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	if bytes.Contains(encoded, []byte(`"errors":null`)) {
		t.Error("errors serialized as null, want []")
	}
	if bytes.Contains(encoded, []byte(`"new_markets":null`)) {
		t.Error("new_markets serialized as null, want []")
	}
	if bytes.Contains(encoded, []byte(`"flags":null`)) {
		t.Error("flags serialized as null, want []")
	}

	// And the converse: a token that produced nothing reports null metadata
	// rather than zero values.
	if !bytes.Contains(encoded, []byte(`"neg_risk":null`)) {
		t.Error("an unlearned neg_risk did not serialize as null")
	}
	if !bytes.Contains(encoded, []byte(`"tick_size":null`)) {
		t.Error("an unlearned tick_size did not serialize as null")
	}
}

// C6, stated at the byte level: whatever the API said is what comes out. This
// is the strongest form of the guarantee, and it is checked on the encoded
// document rather than on any intermediate value.
func TestValuesSurviveToTheEncodedDocumentByteForByte(t *testing.T) {
	pathological := okSnapshot("111")
	pathological.Bids = []book.Level{
		{Price: decimal.Parse("0.9819999999999999"), Size: decimal.Parse("1500.000")},
	}
	pathological.Asks = []book.Level{
		{Price: decimal.Parse("0.980"), Size: decimal.Parse("0.000000001")},
	}

	in := baseInput()
	in.Snapshots = map[string]tracker.Snapshot{"111": pathological}

	encoded, err := json.Marshal(Build(in))
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	for _, want := range []string{
		`"price":"0.9819999999999999"`,
		`"size":"1500.000"`,
		`"price":"0.980"`,
		`"size":"0.000000001"`,
	} {
		if !bytes.Contains(encoded, []byte(want)) {
			t.Errorf("encoded document does not contain %s", want)
		}
	}
	if bytes.Contains(encoded, []byte("0.98199999999999995")) {
		t.Error("encoded document shows a floating-point artifact")
	}
}

func TestBooksAreOrderedHighestBidAndLowestAskFirst(t *testing.T) {
	snapshot := okSnapshot("111")
	snapshot.Bids = []book.Level{
		{Price: decimal.Parse("0.978"), Size: decimal.Parse("1")},
		{Price: decimal.Parse("0.977"), Size: decimal.Parse("1")},
	}
	snapshot.Asks = []book.Level{
		{Price: decimal.Parse("0.982"), Size: decimal.Parse("1")},
		{Price: decimal.Parse("0.983"), Size: decimal.Parse("1")},
	}

	in := baseInput()
	in.Snapshots = map[string]tracker.Snapshot{"111": snapshot}

	token := Build(in).Books["111"]
	if token.Bids[0].Price.Raw() != "0.978" {
		t.Errorf("bids[0] = %q, want the highest", token.Bids[0].Price.Raw())
	}
	if token.Asks[0].Price.Raw() != "0.982" {
		t.Errorf("asks[0] = %q, want the lowest", token.Asks[0].Price.Raw())
	}
}

// H6: the shape must not depend on what happened during the run. Two runs with
// wildly different outcomes produce documents with the same keys and types.
func TestTheShapeIsIdenticalWhateverHappened(t *testing.T) {
	good := baseInput()

	bad := baseInput()
	bad.Snapshots = map[string]tracker.Snapshot{}
	bad.Errors = []string{"everything went wrong"}
	bad.Connection = Connection{WSConnections: 2, Reconnects: 7}

	if got, want := shapeOf(t, Build(good)), shapeOf(t, Build(bad)); got != want {
		t.Errorf("the document shape changed with the outcome:\n %s\nversus\n %s", got, want)
	}
}

// shapeOf renders a document's object keys, ignoring values entirely, so two
// documents can be compared for structure alone.
//
// Leaves collapse to a single token rather than to their type. A nullable field
// legitimately renders as null in one run and a string in another, and calling
// that a change of shape would make the check fail on exactly the runs it is
// supposed to compare. Array contents are ignored for the same reason: an empty
// book and a full one are the same contract.
func shapeOf(t *testing.T, doc Document) string {
	t.Helper()

	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	var generic any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}

	var render func(any) string
	render = func(value any) string {
		switch typed := value.(type) {
		case map[string]any:
			var out bytes.Buffer
			out.WriteByte('{')
			for _, key := range sortedKeys(typed) {
				out.WriteString(key)
				out.WriteByte(':')
				out.WriteString(render(typed[key]))
				out.WriteByte(',')
			}
			out.WriteByte('}')
			return out.String()
		case []any:
			return "array"
		default:
			return "value"
		}
	}

	// Replace the token-id keys with a fixed one before comparing.
	if root, ok := generic.(map[string]any); ok {
		if books, ok := root["books"].(map[string]any); ok {
			normalized := map[string]any{}
			for _, key := range sortedKeys(books) {
				normalized["<token>"] = books[key]
				break
			}
			root["books"] = normalized
		}
	}

	return render(generic)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}

	return keys
}
