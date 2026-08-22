// Test data: the payloads below are captured from the live market channel and
// kept verbatim, field order included. The discriminator is not the first field
// in real messages, so a tidied-up fixture would pass a decoder that assumed it
// was. PROTOCOL.md explains what each one is.

package wire

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/netqo/polymarket-scraper/internal/book"
)

// Payloads captured from the live market channel. They are kept verbatim,
// including the field order, because the discriminator is not the first field
// and any decoder that assumed otherwise would pass a tidied-up fixture.
const (
	bookFrame = `{"market":"0x5dacb3919e3eb6372770cba85c42e0f980ddf18b1a767751de700ee272269233",` +
		`"asset_id":"63691357964355397861776339416833450653158434687925250159757815624217137833471",` +
		`"timestamp":"1786728432337","hash":"924ec9bc1c6dd89f5db9b7b2b68cdab458cc2dc6",` +
		`"bids":[{"price":"0.001","size":"2200.8"},{"price":"0.004","size":"100"}],` +
		`"asks":[{"price":"0.999","size":"50"},{"price":"0.009","size":"25"}],` +
		`"tick_size":"0.001","last_trade_price":"0.004","event_type":"book"}`

	priceChangeFrame = `{"market":"0x3bb59c17b8ec156f8ef55d6de522c53c8bcc8fc708bfba00e1c7820c208ee28b",` +
		`"price_changes":[` +
		`{"asset_id":"40600137944982070120020561148201046074034528738262880058756488608210871246278",` +
		`"price":"0.06","size":"374.34","side":"BUY","hash":"f3285de052f887d0ce2847581636a69c1046178d",` +
		`"best_bid":"0.07","best_ask":"0.08"},` +
		`{"asset_id":"79597156840177168343562124064866301094619106642263695929345529947536386203124",` +
		`"price":"0.94","size":"0","side":"SELL","hash":"bc67a217bd1f1f8625f2d099308ee758f95a772c",` +
		`"best_bid":"0.92","best_ask":"0.93"}],` +
		`"timestamp":"1786728438403","event_type":"price_change"}`

	lastTradeFrame = `{"market":"0x12dc2b61723b2a54fc1947a307389b5f32038e7a29a0e936ad1fe410b969d06a",` +
		`"asset_id":"72710166409980712002774407052535905861151156175854323762835102340552522606004",` +
		`"price":"0.585","size":"2.46","fee_rate_bps":"0","side":"SELL","timestamp":"1786728467101",` +
		`"event_type":"last_trade_price",` +
		`"transaction_hash":"0x6c83cdd2cd59f46a116824aecc9d9b05fc58af1a8f9cfdf09033f5cdb77e0dcf"}`

	bestBidAskFrame = `{"market":"0x12dc2b61723b2a54fc1947a307389b5f32038e7a29a0e936ad1fe410b969d06a",` +
		`"asset_id":"72710166409980712002774407052535905861151156175854323762835102340552522606004",` +
		`"best_bid":"0.585","best_ask":"0.587","spread":"0.002","timestamp":"1786728467006",` +
		`"event_type":"best_bid_ask"}`

	tickSizeChangeFrame = `{"event_type":"tick_size_change","asset_id":"658186",` +
		`"market":"0xbd31","old_tick_size":"0.01","new_tick_size":"0.001","timestamp":"1757908892351"}`

	marketResolvedFrame = `{"event_type":"market_resolved","id":"1031769","market":"0x311d0c",` +
		`"assets_ids":["7604307","3169093"],"winning_asset_id":"7604307",` +
		`"winning_outcome":"Yes","timestamp":"1766790415550","tags":["stocks"]}`
)

// decodeOne decodes a single-event frame and fails the test if it does not
// hold exactly one event of the expected concrete type.
func decodeOne[T Event](t *testing.T, frame string) T {
	t.Helper()

	events, err := DecodeFrame([]byte(frame))
	if err != nil {
		t.Fatalf("DecodeFrame returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("DecodeFrame returned %d events, want 1", len(events))
	}

	typed, ok := events[0].(T)
	if !ok {
		t.Fatalf("event has type %T, want %T", events[0], *new(T))
	}

	return typed
}

// The initial snapshot arrives as an array holding every subscribed asset in
// one frame, while everything else is a bare object. A decoder that handles
// only one of the two silently loses either all of its state or all of its
// updates, so both framings are exercised against the same payload.
func TestDecodeFrameAcceptsBothFramings(t *testing.T) {
	object, err := DecodeFrame([]byte(bookFrame))
	if err != nil {
		t.Fatalf("object framing returned error: %v", err)
	}
	if len(object) != 1 {
		t.Fatalf("object framing produced %d events, want 1", len(object))
	}

	array, err := DecodeFrame([]byte("[" + bookFrame + "," + bookFrame + "," + bookFrame + "]"))
	if err != nil {
		t.Fatalf("array framing returned error: %v", err)
	}
	if len(array) != 3 {
		t.Fatalf("array framing produced %d events, want 3", len(array))
	}

	for i, event := range array {
		if _, ok := event.(Book); !ok {
			t.Errorf("array element %d has type %T, want Book", i, event)
		}
	}
}

func TestDecodeBookSnapshot(t *testing.T) {
	got := decodeOne[Book](t, bookFrame)

	if got.AssetID != "63691357964355397861776339416833450653158434687925250159757815624217137833471" {
		t.Errorf("AssetID = %q", got.AssetID)
	}
	if got.Timestamp != "1786728432337" {
		t.Errorf("Timestamp = %q, want the feed's epoch milliseconds verbatim", got.Timestamp)
	}
	if got.Hash != "924ec9bc1c6dd89f5db9b7b2b68cdab458cc2dc6" {
		t.Errorf("Hash = %q", got.Hash)
	}
	if got.TickSize.Raw() != "0.001" {
		t.Errorf("TickSize = %q, want 0.001", got.TickSize.Raw())
	}
	if got.LastTradePrice.Raw() != "0.004" {
		t.Errorf("LastTradePrice = %q, want 0.004", got.LastTradePrice.Raw())
	}

	if len(got.Bids) != 2 || len(got.Asks) != 2 {
		t.Fatalf("got %d bids and %d asks, want 2 and 2", len(got.Bids), len(got.Asks))
	}
	// Levels are decoded in wire order and left alone: the book package sorts.
	if got.Bids[0].Price.Raw() != "0.001" {
		t.Errorf("bids[0] = %q, want the wire order preserved", got.Bids[0].Price.Raw())
	}
	if got.Bids[0].Size.Raw() != "2200.8" {
		t.Errorf("bids[0] size = %q, want 2200.8", got.Bids[0].Size.Raw())
	}
}

// A book refresh sent after a trade omits tick size and last trade price. That
// must decode cleanly and report them as absent rather than as zero.
func TestDecodeBookRefreshWithoutTickSize(t *testing.T) {
	const refresh = `{"market":"0xabc","asset_id":"123","timestamp":"1786728432337",` +
		`"hash":"deadbeef","bids":[],"asks":[],"event_type":"book"}`

	got := decodeOne[Book](t, refresh)

	if !got.TickSize.Absent() {
		t.Errorf("TickSize = %q, want absent", got.TickSize.Raw())
	}
	if !got.LastTradePrice.Absent() {
		t.Errorf("LastTradePrice = %q, want absent", got.LastTradePrice.Raw())
	}
}

// The envelope carries no asset id at all: each element names its own token,
// and one message routinely covers both legs of a binary market.
func TestDecodePriceChangeBatch(t *testing.T) {
	got := decodeOne[PriceChange](t, priceChangeFrame)

	if got.Timestamp != "1786728438403" {
		t.Errorf("Timestamp = %q", got.Timestamp)
	}
	if len(got.Changes) != 2 {
		t.Fatalf("got %d changes, want 2", len(got.Changes))
	}

	first := got.Changes[0]
	if first.AssetID == "" {
		t.Error("change entry has no asset id")
	}
	if first.Price.Raw() != "0.06" || first.Size.Raw() != "374.34" {
		t.Errorf("first change = %q x %q, want 0.06 x 374.34", first.Price.Raw(), first.Size.Raw())
	}
	if first.BestBid.Raw() != "0.07" || first.BestAsk.Raw() != "0.08" {
		t.Errorf("best quotes = %q / %q, want 0.07 / 0.08", first.BestBid.Raw(), first.BestAsk.Raw())
	}

	if got.Changes[0].AssetID == got.Changes[1].AssetID {
		t.Error("both entries name the same asset; the batch is meant to span tokens")
	}
	if !got.Changes[1].Size.IsZero() {
		t.Error("second entry should carry the level-removal size of zero")
	}
}

// Putting liquidity on the wrong half of the book is worse than not applying
// the update, so an unrecognised side is reported rather than guessed at.
func TestPriceChangeEntryBookSide(t *testing.T) {
	tests := []struct {
		side    string
		want    book.Side
		wantOK  bool
		comment string
	}{
		{SideBuy, book.Bids, true, "a buy rests on the bid side"},
		{SideSell, book.Asks, true, "a sell rests on the ask side"},
		{"buy", 0, false, "the feed spells it uppercase"},
		{"", 0, false, "missing"},
		{"SIDEWAYS", 0, false, "unrecognised"},
	}

	for _, tt := range tests {
		t.Run(tt.side+" "+tt.comment, func(t *testing.T) {
			got, ok := PriceChangeEntry{Side: tt.side}.BookSide()
			if ok != tt.wantOK {
				t.Fatalf("BookSide() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("BookSide() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecodeLastTrade(t *testing.T) {
	got := decodeOne[LastTrade](t, lastTradeFrame)

	if got.Price.Raw() != "0.585" || got.Size.Raw() != "2.46" {
		t.Errorf("trade = %q x %q", got.Price.Raw(), got.Size.Raw())
	}
	if got.Side != SideSell {
		t.Errorf("Side = %q, want %q", got.Side, SideSell)
	}
	// The fee rate is the whole reason this event is collected.
	if got.FeeRateBPS.Raw() != "0" {
		t.Errorf("FeeRateBPS = %q, want 0", got.FeeRateBPS.Raw())
	}
	if got.TransactionHash == "" {
		t.Error("TransactionHash is empty")
	}
}

func TestDecodeBestBidAsk(t *testing.T) {
	got := decodeOne[BestBidAsk](t, bestBidAskFrame)

	if got.BestBid.Raw() != "0.585" || got.BestAsk.Raw() != "0.587" {
		t.Errorf("quotes = %q / %q", got.BestBid.Raw(), got.BestAsk.Raw())
	}
	if got.Spread.Raw() != "0.002" {
		t.Errorf("Spread = %q, want 0.002", got.Spread.Raw())
	}
}

func TestDecodeTickSizeChange(t *testing.T) {
	got := decodeOne[TickSizeChange](t, tickSizeChangeFrame)

	if got.OldTickSize.Raw() != "0.01" || got.NewTickSize.Raw() != "0.001" {
		t.Errorf("tick sizes = %q -> %q", got.OldTickSize.Raw(), got.NewTickSize.Raw())
	}
}

func TestDecodeMarketResolved(t *testing.T) {
	got := decodeOne[MarketResolved](t, marketResolvedFrame)

	if got.WinningOutcome != "Yes" {
		t.Errorf("WinningOutcome = %q, want Yes", got.WinningOutcome)
	}
	if got.WinningAssetID != "7604307" {
		t.Errorf("WinningAssetID = %q", got.WinningAssetID)
	}
	if len(got.AssetIDs) != 2 {
		t.Errorf("AssetIDs = %v, want two entries", got.AssetIDs)
	}
}

func TestDecodeNewMarket(t *testing.T) {
	const frame = `{"id":"5551","market":"0xabc","condition_id":"0xdef",` +
		`"question":"Bitcoin Up or Down - Aug 14, 3:15PM ET","slug":"btc-up-or-down",` +
		`"assets_ids":["111","222"],"outcomes":["Up","Down"],` +
		`"timestamp":"1786728400000","event_type":"new_market"}`

	got := decodeOne[NewMarket](t, frame)

	if got.Question != "Bitcoin Up or Down - Aug 14, 3:15PM ET" {
		t.Errorf("Question = %q", got.Question)
	}
	if len(got.AssetIDs) != 2 || got.AssetIDs[0] != "111" {
		t.Errorf("AssetIDs = %v, want [111 222]", got.AssetIDs)
	}
	if len(got.Outcomes) != 2 || got.Outcomes[0] != "Up" {
		t.Errorf("Outcomes = %v, want [Up Down]", got.Outcomes)
	}
}

// A protocol addition should show up in the run's error list, not vanish. It is
// not a reason to distrust a token either, so it decodes successfully.
func TestDecodeUnknownEventTypeIsReportedNotRejected(t *testing.T) {
	const frame = `{"event_type":"something_new","asset_id":"111","payload":{"a":1}}`

	got := decodeOne[Unknown](t, frame)

	if got.Type() != "something_new" {
		t.Errorf("Type() = %q, want something_new", got.Type())
	}
	if !json.Valid(got.Raw) {
		t.Error("Unknown did not keep the original payload")
	}
	if !strings.Contains(string(got.Raw), "something_new") {
		t.Errorf("Raw = %s, want the original message", got.Raw)
	}
}

// Fields the feed grows must not break this build.
func TestDecodeIgnoresUnknownFields(t *testing.T) {
	const frame = `{"event_type":"best_bid_ask","asset_id":"111","best_bid":"0.5",` +
		`"best_ask":"0.6","spread":"0.1","timestamp":"1","brand_new_field":{"nested":true}}`

	got := decodeOne[BestBidAsk](t, frame)
	if got.BestBid.Raw() != "0.5" {
		t.Errorf("BestBid = %q, want 0.5", got.BestBid.Raw())
	}
}

func TestDecodeFrameRejectsUnusableInput(t *testing.T) {
	tests := []struct {
		name  string
		frame string
	}{
		{"empty", ""},
		{"whitespace", "   \n"},
		{"not json", "PONG"},
		{"bare string", `"hello"`},
		{"bare number", `42`},
		{"truncated object", `{"event_type":"book"`},
		{"no discriminator", `{"asset_id":"111"}`},
		{"empty discriminator", `{"event_type":"","asset_id":"111"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, err := DecodeFrame([]byte(tt.frame))
			if err == nil {
				t.Fatalf("DecodeFrame(%q) succeeded, want an error", tt.frame)
			}
			if len(events) != 0 {
				t.Errorf("DecodeFrame(%q) returned %d events alongside the error", tt.frame, len(events))
			}
		})
	}
}

// Discarding a whole frame over one bad element would throw away updates for
// tokens that are perfectly fine, so the good elements come back alongside an
// error naming the bad one.
func TestDecodeFrameReturnsGoodElementsAlongsideElementErrors(t *testing.T) {
	frame := "[" + bookFrame + `,{"no_discriminator":true},` + bestBidAskFrame + "]"

	events, err := DecodeFrame([]byte(frame))
	if err == nil {
		t.Fatal("DecodeFrame succeeded despite an undecodable element")
	}
	if len(events) != 2 {
		t.Fatalf("DecodeFrame returned %d events, want the 2 that were decodable", len(events))
	}
	if !strings.Contains(err.Error(), "element 1") {
		t.Errorf("error %q does not identify which element failed", err)
	}
}

func TestDecodeFrameAcceptsAnEmptyArray(t *testing.T) {
	events, err := DecodeFrame([]byte("[]"))
	if err != nil {
		t.Fatalf("DecodeFrame returned error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("DecodeFrame returned %d events for an empty array", len(events))
	}
}

// The payload is the only evidence of what the exchange actually sent, so an
// error quotes as much of it as is reasonable. It is still bounded, because a
// frame may be as large as the read limit allows and an error carrying
// megabytes would reach the output document.
func TestPreviewIsGenerousButBounded(t *testing.T) {
	tests := []struct {
		name string
		size int
	}{
		{"short payloads are quoted whole", 200},
		{"payloads up to the limit are quoted whole", maxPreviewBytes},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(strings.Repeat("x", tt.size))

			if got := preview(raw); got != string(raw) {
				t.Errorf("preview shortened a %d byte payload to %d bytes", tt.size, len(got))
			}
		})
	}
}

func TestPreviewReportsWhatItLeftOut(t *testing.T) {
	const size = maxPreviewBytes * 4

	got := preview([]byte(strings.Repeat("x", size)))

	if len(got) > maxPreviewBytes+64 {
		t.Errorf("preview returned %d bytes for a %d byte payload, want it bounded", len(got), size)
	}
	// Trailing off silently would leave a reader unable to tell a truncated
	// payload from a complete one.
	if !strings.Contains(got, strconv.Itoa(size)) {
		t.Errorf("preview = %q..., want it to report the true size %d", got[:64], size)
	}
}

// Keepalives are raw text, so they have to be recognised before anything tries
// to parse them as JSON.
func TestIsKeepalive(t *testing.T) {
	tests := []struct {
		frame string
		want  bool
	}{
		{"PING", true},
		{"PONG", true},
		{"PONG\n", true},
		{" PING ", true},
		{"pong", false},
		{`{"event_type":"book"}`, false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.frame, func(t *testing.T) {
			if got := IsKeepalive([]byte(tt.frame)); got != tt.want {
				t.Errorf("IsKeepalive(%q) = %v, want %v", tt.frame, got, tt.want)
			}
		})
	}
}
