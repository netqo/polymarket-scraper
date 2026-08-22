package wire

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// protocolDocPath is the documentation this package is the source of truth for.
const protocolDocPath = "../../PROTOCOL.md"

func readProtocolDoc(t *testing.T) string {
	t.Helper()

	contents, err := os.ReadFile(protocolDocPath)
	if err != nil {
		t.Fatalf("reading %s: %v", protocolDocPath, err)
	}

	return string(contents)
}

// mentions reports whether the document says something, ignoring where its
// lines happen to break.
//
// The document is hard wrapped, so any phrase long enough to be worth checking
// will eventually straddle a newline. Collapsing whitespace here rather than
// rewording the prose keeps the test from dictating the paragraph shape.
func mentions(doc, phrase string) bool {
	return strings.Contains(strings.Join(strings.Fields(doc), " "), phrase)
}

// allEventTypes lists every event this build understands. Written out rather
// than derived, so adding one without documenting it fails here.
func allEventTypes() []EventType {
	return []EventType{
		EventBook,
		EventPriceChange,
		EventLastTradePrice,
		EventTickSizeChange,
		EventBestBidAsk,
		EventNewMarket,
		EventMarketResolved,
	}
}

// The protocol is the half of the contract that Polymarket controls, and most
// of what this package knows about it was learned by being surprised. An
// undocumented event type is a surprise waiting to happen again.
func TestEveryEventTypeIsDocumented(t *testing.T) {
	doc := readProtocolDoc(t)

	for _, eventType := range allEventTypes() {
		if !strings.Contains(doc, "`"+string(eventType)+"`") {
			t.Errorf("event type %q is not documented in %s", eventType, protocolDocPath)
		}
	}
}

// The subscription message is quoted in the documentation byte for byte,
// because the custom feature flag is what unlocks three of the events the output
// document reports and losing it fails silently. A quotation that has drifted
// from the code is worse than none: someone would copy it.
func TestTheDocumentedSubscriptionMatchesTheCode(t *testing.T) {
	encoded, err := json.Marshal(NewSubscription([]string{"111", "222"}))
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	if doc := readProtocolDoc(t); !mentions(doc, string(encoded)) {
		t.Errorf("%s does not quote the subscription this build sends:\n %s",
			protocolDocPath, encoded)
	}
}

func TestTheDocumentedSubscriptionUpdateMatchesTheCode(t *testing.T) {
	encoded, err := json.Marshal(Subscribe([]string{"333"}))
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	if doc := readProtocolDoc(t); !mentions(doc, string(encoded)) {
		t.Errorf("%s does not quote the subscription update this build sends:\n %s",
			protocolDocPath, encoded)
	}
}

// The keepalive is the single easiest thing here to get wrong, and getting it
// wrong kills the connection half a minute later for no visible reason.
func TestTheKeepaliveWordsAreDocumented(t *testing.T) {
	doc := readProtocolDoc(t)

	for _, word := range []string{Ping, Pong} {
		if !strings.Contains(doc, "`"+word+"`") {
			t.Errorf("%s does not name the keepalive frame %q", protocolDocPath, word)
		}
	}
}

// Each of these fails silently against the live server, which is the whole
// reason the document exists rather than a comment somewhere.
func TestTheSilentFailuresAreCalledOut(t *testing.T) {
	doc := readProtocolDoc(t)

	surprises := []string{
		// Bids descend and asks ascend, says the documentation. They do not.
		"the exact opposite",
		// The initial snapshot is an array; everything else is an object.
		"array",
		// Past roughly 750 assets the snapshot silently never arrives.
		"750",
		// PING is a text frame, not a protocol ping.
		"not a websocket protocol ping",
		// size zero deletes the level rather than zeroing it.
		"removes the level",
		// min_order_size and neg_risk are REST-only.
		"min_order_size",
	}

	for _, surprise := range surprises {
		if !mentions(doc, surprise) {
			t.Errorf("%s no longer mentions %q, which is one of the failures that "+
				"cost real time to discover", protocolDocPath, surprise)
		}
	}
}
