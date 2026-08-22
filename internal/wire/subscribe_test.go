// Test data: The expected messages are what this build sends, checked byte for byte. The
// custom feature flag is not invented: without it the connection never delivers
// three of the events the output document is required to report.

package wire

import (
	"encoding/json"
	"testing"
)

// B1: the subscription message is checked byte for byte, because the custom
// feature flag is what unlocks the best quote, new market and market resolved
// events, and losing it would quietly remove three things the output document
// is required to report.
func TestNewSubscriptionShape(t *testing.T) {
	got, err := json.Marshal(NewSubscription([]string{"111", "222"}))
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	const want = `{"assets_ids":["111","222"],"type":"market","custom_feature_enabled":true}`
	if string(got) != want {
		t.Errorf("subscription =\n %s\nwant\n %s", got, want)
	}
}

// The update message is a different shape from the opening subscription rather
// than a repeat of it: it carries an operation and no type.
func TestSubscriptionUpdateShape(t *testing.T) {
	tests := []struct {
		name   string
		update SubscriptionUpdate
		want   string
	}{
		{"subscribe", Subscribe([]string{"333"}), `{"operation":"subscribe","assets_ids":["333"]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.update)
			if err != nil {
				t.Fatalf("Marshal returned error: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("update =\n %s\nwant\n %s", got, tt.want)
			}
		})
	}
}
