package wire

import (
	"encoding/json"
	"testing"
)

// Captured from GET /book. The three fields the websocket never sends are the
// reason this endpoint is part of every run rather than only a fallback.
const restBookBody = `{"market":"0x7d0aaf81bbd3fd73b6a1651cce08a452c0cbf9c0cbb4520ce0f981065b639d88",` +
	`"asset_id":"27146956652877944551877724690365745048289675287536243265951843487691050802191",` +
	`"timestamp":"1786728198766","hash":"4514cbfcdad578db98c9bdac47e6c3b3b4632ced",` +
	`"bids":[{"price":"0.001","size":"28195.33"}],"asks":[{"price":"0.999","size":"231.08"}],` +
	`"min_order_size":"5","tick_size":"0.001","neg_risk":true,"last_trade_price":"0.004"}`

func TestDecodeRESTBook(t *testing.T) {
	var got RESTBook
	if err := json.Unmarshal([]byte(restBookBody), &got); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}

	if got.MinOrderSize.Raw() != "5" {
		t.Errorf("MinOrderSize = %q, want 5", got.MinOrderSize.Raw())
	}
	if got.TickSize.Raw() != "0.001" {
		t.Errorf("TickSize = %q, want 0.001", got.TickSize.Raw())
	}
	if got.NegRisk == nil || !*got.NegRisk {
		t.Errorf("NegRisk = %v, want true", got.NegRisk)
	}
	if len(got.Bids) != 1 || got.Bids[0].Size.Raw() != "28195.33" {
		t.Errorf("bids = %v", got.Bids)
	}
}

// D3: absent and false are different answers. Defaulting a missing neg_risk to
// false would be inventing a value the consuming agent then acts on.
func TestDecodeRESTBookLeavesAbsentNegRiskUnset(t *testing.T) {
	const body = `{"market":"0xabc","asset_id":"111","timestamp":"1","hash":"h","bids":[],"asks":[]}`

	var got RESTBook
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}

	if got.NegRisk != nil {
		t.Errorf("NegRisk = %v, want nil for an absent field", *got.NegRisk)
	}
	if !got.MinOrderSize.Absent() {
		t.Errorf("MinOrderSize = %q, want absent", got.MinOrderSize.Raw())
	}
}

func TestDecodeRESTBookBatch(t *testing.T) {
	var got []RESTBook
	if err := json.Unmarshal([]byte("["+restBookBody+","+restBookBody+"]"), &got); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d books, want 2", len(got))
	}
}

func TestNewBookRequests(t *testing.T) {
	got, err := json.Marshal(NewBookRequests([]string{"111", "222"}))
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	const want = `[{"token_id":"111"},{"token_id":"222"}]`
	if string(got) != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestNewBookRequestsOnAnEmptyListIsAnEmptyArray(t *testing.T) {
	got, err := json.Marshal(NewBookRequests(nil))
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if string(got) != "[]" {
		t.Errorf("body = %s, want []", got)
	}
}
