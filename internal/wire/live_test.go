//go:build live

// Test data: real, and that is the entire point of this file. It answers a
// question the offline suite structurally cannot: whether the types in this
// package still match what Polymarket actually sends.
//
// The offline tests decode captured payloads, so they prove the decoder handles
// what the exchange sent on the day someone captured it. A field renamed since
// then would pass every one of them and silently produce a null, and a field
// added since then would be dropped without anything noticing, because
// decodeInto ignores unknown fields on purpose.
//
// So this compares in both directions: keys on the wire that no struct field
// claims, and struct fields the wire never sends. Neither is automatically a
// bug, which is why this reports rather than fails for the additive case.
//
// Run with: POLYMARKET_LIVE_TOKENS=/path/to/tokens.txt make test-live

package wire

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Live endpoints. Spelled out rather than imported from the config package,
// which this one does not otherwise depend on.
const (
	liveRESTURL = "https://clob.polymarket.com"
	liveWSURL   = "wss://ws-subscriptions-clob.polymarket.com/ws/market"
)

// liveWindow is how long to listen for before drawing conclusions. Long enough
// to see price changes and best quotes on a normal market; not long enough to
// see a tick size change or a resolution, which is why an absent field is
// reported rather than failed.
const liveWindow = 45 * time.Second

// liveTokens reads the operator-supplied token list, skipping if there is none.
func liveTokens(t *testing.T) []string {
	t.Helper()

	path := os.Getenv("POLYMARKET_LIVE_TOKENS")
	if path == "" {
		t.Skip("set POLYMARKET_LIVE_TOKENS to a token file to run the live checks")
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var ids []string
	for _, line := range strings.Split(string(contents), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			ids = append(ids, line)
		}
	}
	if len(ids) == 0 {
		t.Fatalf("%s holds no token ids", path)
	}

	return ids
}

// jsonNames lists the field names a struct claims, by its json tags, including
// those of any struct it embeds by value.
func jsonNames(typ reflect.Type) []string {
	var names []string

	for i := range typ.NumField() {
		field := typ.Field(i)
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		names = append(names, tag)
	}
	slices.Sort(names)

	return names
}

// census is what one message type looked like on the wire.
type census struct {
	seen  int
	keys  map[string]int
	nulls map[string]int

	// sample is one whole message, kept so that a field being dropped can be
	// judged rather than merely counted. A name alone does not say whether the
	// value is worth having.
	sample map[string]json.RawMessage
}

func newCensus() *census {
	return &census{keys: make(map[string]int), nulls: make(map[string]int)}
}

// add records one object's keys.
func (c *census) add(object map[string]json.RawMessage) {
	c.seen++
	if c.sample == nil {
		c.sample = object
	}

	for key, value := range object {
		c.keys[key]++
		if string(value) == "null" {
			c.nulls[key]++
		}
	}
}

// known is what this build has seen the wire carry and decided not to read.
//
// A field being dropped is not automatically a bug, but it must not be a
// surprise. Anything here has been looked at once and judged; anything not here
// fails the test, so a field the exchange adds tomorrow is noticed rather than
// silently discarded.
//
// Captured 2026-08-22 and recorded in the working notes. Several of these are
// worth having and are not read yet, which is a decision rather than an
// oversight:
//
//   - tags was empty on all 25 distinct announcements observed on 2026-08-22,
//     across sports and crypto alike. A field that is always empty would be a
//     column of nulls in the output. Revisit if it is ever seen populated.
//   - fee_schedule and taker_base_fee are the exchange stating its own fee
//     terms. The scraper computes no fees, but reporting what the exchange said
//     is the same free cross-check fee_rate_bps already is. Not read yet.
//   - clob_token_ids duplicates assets_ids.
//   - active, description, event_message, fees_enabled, group_item_title and
//     line are metadata with no use here today.
//
// sports_market_type, game_start_time and order_price_min_tick_size were on this
// list and are now read.
var known = map[EventType][]string{
	EventNewMarket: {
		"active",
		"clob_token_ids",
		"description",
		"event_message",
		"fee_schedule",
		"fees_enabled",
		"group_item_title",
		"line",
		"tags",
		"taker_base_fee",
	},
}

// report compares what arrived against what the type claims.
func (c *census) report(t *testing.T, name string, typ reflect.Type) {
	t.Helper()

	if c.seen == 0 {
		t.Logf("%s: none arrived in %v, so nothing can be concluded about it", name, liveWindow)
		return
	}

	claimed := jsonNames(typ)

	// Fields on the wire that no struct field claims. These are being silently
	// dropped, since decodeInto ignores unknown fields.
	//
	// event_type is not one of them: it is read by the envelope to route the
	// message and deliberately absent from every concrete type, so counting it
	// here would report the router working as a leak.
	ignored := known[EventType(name)]

	var unclaimed []string
	for key := range c.keys {
		if key == "event_type" || slices.Contains(claimed, key) || slices.Contains(ignored, key) {
			continue
		}
		unclaimed = append(unclaimed, key)
	}
	slices.Sort(unclaimed)

	// Fields the struct claims that never arrived. Either dead weight, or the
	// window was too short, or the name has changed and the field is silently
	// null on every message.
	var absent []string
	for _, name := range claimed {
		if c.keys[name] == 0 {
			absent = append(absent, name)
		}
	}

	t.Logf("%s: %d messages", name, c.seen)
	if len(ignored) > 0 {
		t.Logf("%s: %d fields read, %d seen and deliberately not read", name, len(claimed), len(ignored))
	}
	if len(unclaimed) > 0 {
		t.Errorf("%s: the wire carries %v, which this build neither reads nor knows about. "+
			"Decide what it is for, then either read it or add it to the known list with a reason.",
			name, unclaimed)
		for _, key := range unclaimed {
			t.Logf("    %s.%s = %s", name, key, preview(c.sample[key]))
		}
	}
	if len(absent) > 0 {
		t.Logf("%s: never sent %v (dead weight, a renamed field, or too short a window)", name, absent)
	}
	for key, count := range c.nulls {
		if count == c.seen {
			t.Logf("%s: %s was null on every message", name, key)
		}
	}
}

// Item 35: do the REST structs still match what the endpoint returns?
func TestLiveRESTBookMatchesItsStruct(t *testing.T) {
	ids := liveTokens(t)

	body, err := json.Marshal(NewBookRequests(ids[:min(len(ids), 20)]))
	if err != nil {
		t.Fatalf("encoding the request: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, liveRESTURL+"/books", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST /books: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("POST /books returned %d: %s", response.StatusCode, preview(raw))
	}

	var books []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &books); err != nil {
		t.Fatalf("the response is not an array of objects: %v", err)
	}

	counted := newCensus()
	for _, object := range books {
		counted.add(object)
	}
	counted.report(t, "RESTBook", reflect.TypeOf(RESTBook{}))

	// The three fields that are the whole reason REST is part of every run.
	for _, required := range []string{"min_order_size", "neg_risk", "tick_size"} {
		if counted.keys[required] == 0 {
			t.Errorf("no book carried %s, which the output document reports and the websocket never sends", required)
		}
	}
}

// Item 35 over the websocket, and item 8 alongside it: every decimal-valued
// field is checked for whether it actually parses.
func TestLiveEventsMatchTheirStructs(t *testing.T) {
	ids := liveTokens(t)
	if len(ids) > 50 {
		ids = ids[:50]
	}

	ctx, cancel := context.WithTimeout(t.Context(), liveWindow+30*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, liveWSURL, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		t.Fatalf("dialing %s: %v", liveWSURL, err)
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(32 << 20)

	subscription, err := json.Marshal(NewSubscription(ids))
	if err != nil {
		t.Fatalf("encoding the subscription: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, subscription); err != nil {
		t.Fatalf("sending the subscription: %v", err)
	}

	types := map[EventType]reflect.Type{
		EventBook:           reflect.TypeOf(Book{}),
		EventPriceChange:    reflect.TypeOf(PriceChange{}),
		EventLastTradePrice: reflect.TypeOf(LastTrade{}),
		EventTickSizeChange: reflect.TypeOf(TickSizeChange{}),
		EventBestBidAsk:     reflect.TypeOf(BestBidAsk{}),
		EventNewMarket:      reflect.TypeOf(NewMarket{}),
		EventMarketResolved: reflect.TypeOf(MarketResolved{}),
	}

	counted := make(map[EventType]*census, len(types))
	for eventType := range types {
		counted[eventType] = newCensus()
	}
	unknown := make(map[string]int)

	frames, keepalives := 0, 0
	deadline := time.Now().Add(liveWindow)

	for time.Now().Before(deadline) {
		readCtx, stop := context.WithDeadline(ctx, deadline)
		_, payload, err := conn.Read(readCtx)
		stop()

		if err != nil {
			break
		}
		frames++

		if IsKeepalive(payload) {
			keepalives++
			continue
		}

		for _, object := range flatten(t, payload) {
			var head struct {
				EventType EventType `json:"event_type"`
			}
			raw, _ := json.Marshal(object)
			_ = json.Unmarshal(raw, &head)

			if _, known := types[head.EventType]; !known {
				unknown[string(head.EventType)]++
				continue
			}
			counted[head.EventType].add(object)
		}
	}

	t.Logf("read %d frames in %v, %d of them keepalives", frames, liveWindow, keepalives)
	if frames == 0 {
		t.Fatal("the connection delivered nothing at all; the websocket may be blocked on this network")
	}

	for eventType, typ := range types {
		counted[eventType].report(t, string(eventType), typ)
	}
	for eventType, count := range unknown {
		t.Errorf("the feed sent %d messages of unknown type %q, which this build ignores", count, eventType)
	}
}

// flatten turns a frame into the objects it carries, whichever framing it used.
func flatten(t *testing.T, payload []byte) []map[string]json.RawMessage {
	t.Helper()

	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" {
		return nil
	}

	if trimmed[0] == '[' {
		var objects []map[string]json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &objects); err != nil {
			t.Errorf("an array frame did not decode: %v", err)
		}

		return objects
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil {
		t.Errorf("an object frame did not decode: %v", err)

		return nil
	}

	return []map[string]json.RawMessage{object}
}

// Item 8: does the feed ever send a price or size that is not a plain decimal?
//
// internal/decimal tolerates text that is not a decimal literal, keeping the raw
// bytes and marking the value unusable for comparison. That tolerance is either
// earned or it is defending against something that never happens, and only the
// live feed can say which.
func TestLiveDecimalFieldsAreDecimals(t *testing.T) {
	ids := liveTokens(t)
	if len(ids) > 50 {
		ids = ids[:50]
	}

	ctx, cancel := context.WithTimeout(t.Context(), liveWindow+30*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, liveWSURL, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		t.Fatalf("dialing %s: %v", liveWSURL, err)
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(32 << 20)

	subscription, err := json.Marshal(NewSubscription(ids))
	if err != nil {
		t.Fatalf("encoding the subscription: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, subscription); err != nil {
		t.Fatalf("sending the subscription: %v", err)
	}

	checked, odd := 0, 0
	deadline := time.Now().Add(liveWindow)

	for time.Now().Before(deadline) {
		readCtx, stop := context.WithDeadline(ctx, deadline)
		_, payload, err := conn.Read(readCtx)
		stop()

		if err != nil {
			break
		}
		if IsKeepalive(payload) {
			continue
		}

		events, _ := DecodeFrame(payload)
		for _, event := range events {
			for _, value := range decimalsOf(event) {
				checked++
				if value.Absent() || value.Valid() {
					continue
				}
				odd++
				t.Errorf("the feed sent %q, which is not a plain decimal", value.Raw())
			}
		}
	}

	if checked == 0 {
		t.Skip("no decimal values arrived, so nothing can be concluded")
	}
	t.Logf("checked %d decimal values, %d of them unparseable", checked, odd)
}

// decimalsOf pulls every decimal-valued field out of an event.
func decimalsOf(event Event) []decimalValue {
	switch typed := event.(type) {
	case Book:
		values := []decimalValue{typed.TickSize, typed.LastTradePrice}
		for _, level := range typed.Bids {
			values = append(values, level.Price, level.Size)
		}
		for _, level := range typed.Asks {
			values = append(values, level.Price, level.Size)
		}

		return values

	case PriceChange:
		var values []decimalValue
		for _, entry := range typed.Changes {
			values = append(values, entry.Price, entry.Size, entry.BestBid, entry.BestAsk)
		}

		return values

	case LastTrade:
		return []decimalValue{typed.Price, typed.Size, typed.FeeRateBPS}

	case BestBidAsk:
		return []decimalValue{typed.BestBid, typed.BestAsk, typed.Spread}

	case TickSizeChange:
		return []decimalValue{typed.OldTickSize, typed.NewTickSize}

	default:
		return nil
	}
}

// decimalValue is the part of decimal.Dec this file needs, named so the switch
// above reads as a list of values rather than of types.
type decimalValue interface {
	Absent() bool
	Valid() bool
	Raw() string
}
