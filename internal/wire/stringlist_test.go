package wire

import (
	"encoding/json"
	"slices"
	"testing"
)

// Polymarket sends list-valued fields both as real arrays and as strings
// holding an encoded array. Which one arrives is not something the rest of the
// scraper should have to care about.
func TestStringListAcceptsBothEncodings(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"array", `["Up","Down"]`, []string{"Up", "Down"}},
		{"quoted array", `"[\"Up\",\"Down\"]"`, []string{"Up", "Down"}},
		{"empty array", `[]`, []string{}},
		{"quoted empty array", `"[]"`, []string{}},
		{"null", `null`, nil},
		{"single entry", `["Yes"]`, []string{"Yes"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got StringList
			if err := json.Unmarshal([]byte(tt.in), &got); err != nil {
				t.Fatalf("Unmarshal(%s) returned error: %v", tt.in, err)
			}
			if !slices.Equal([]string(got), tt.want) {
				t.Errorf("Unmarshal(%s) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestStringListRejectsOtherShapes(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"number", `42`},
		{"object", `{"a":1}`},
		{"array of numbers", `[1,2]`},
		{"quoted non-array", `"Up"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got StringList
			if err := json.Unmarshal([]byte(tt.in), &got); err == nil {
				t.Fatalf("Unmarshal(%s) succeeded, want an error", tt.in)
			}
		})
	}
}

// Whichever encoding arrives, the scraper's own output uses one form.
func TestStringListAlwaysMarshalsAsAnArray(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"from an array", `["Up","Down"]`, `["Up","Down"]`},
		{"from a quoted array", `"[\"Up\",\"Down\"]"`, `["Up","Down"]`},
		{"from null", `null`, `null`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var list StringList
			if err := json.Unmarshal([]byte(tt.in), &list); err != nil {
				t.Fatalf("Unmarshal returned error: %v", err)
			}

			got, err := json.Marshal(list)
			if err != nil {
				t.Fatalf("Marshal returned error: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("Marshal = %s, want %s", got, tt.want)
			}
		})
	}
}
