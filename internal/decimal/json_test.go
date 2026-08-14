package decimal

import (
	"encoding/json"
	"strings"
	"testing"
)

// C6: values are passed through as the API's own decimal strings. Marshalling
// must reproduce the input byte for byte, with no float artifacts and no
// rounding, including for values a float64 could not represent exactly.
func TestMarshalJSONIsByteIdentical(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"0.982", `"0.982"`},
		{"0.980", `"0.980"`},
		{"0.98", `"0.98"`},
		{"1500", `"1500"`},
		{"0.9819999999999999", `"0.9819999999999999"`},
		{"0.000000000000000001", `"0.000000000000000001"`},
		{"-0.5", `"-0.5"`},
		{"99999999999999999999999", `"99999999999999999999999"`},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := json.Marshal(Parse(tt.in))
			if err != nil {
				t.Fatalf("json.Marshal(%q) returned error: %v", tt.in, err)
			}
			if string(got) != tt.want {
				t.Errorf("json.Marshal(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

// D3: a field the feed did not provide is null, never "" and never 0.
func TestMarshalJSONAbsentIsNull(t *testing.T) {
	got, err := json.Marshal(Dec{})
	if err != nil {
		t.Fatalf("json.Marshal(Dec{}) returned error: %v", err)
	}
	if string(got) != "null" {
		t.Errorf("json.Marshal(Dec{}) = %s, want null", got)
	}
}

// An unparseable value still reaches the output, and anything that is not a
// plain decimal literal takes the escaping path so it cannot inject JSON.
func TestMarshalJSONEscapesNonDecimalText(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"quote", `0.98"`},
		{"backslash", `0.98\`},
		{"newline", "0.9\n8"},
		{"brace", `{"injected":1}`},
		{"letters", "abc"},
		{"non ascii", "0.98\u00e9"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(Parse(tt.in))
			if err != nil {
				t.Fatalf("json.Marshal(%q) returned error: %v", tt.in, err)
			}

			var back string
			if err := json.Unmarshal(got, &back); err != nil {
				t.Fatalf("json.Marshal(%q) produced invalid JSON %s: %v", tt.in, got, err)
			}
			if back != tt.in {
				t.Errorf("round trip of %q produced %q", tt.in, back)
			}
		})
	}
}

func TestUnmarshalJSONFromString(t *testing.T) {
	var d Dec
	if err := json.Unmarshal([]byte(`"0.982"`), &d); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if d.Raw() != "0.982" {
		t.Errorf("Raw() = %q, want %q", d.Raw(), "0.982")
	}
	if !d.Valid() {
		t.Error("Valid() = false, want true")
	}
}

// If Polymarket ever switches a field from string to number, the literal source
// text must still survive: the decoder never routes through float64.
func TestUnmarshalJSONFromNumberKeepsSourceText(t *testing.T) {
	tests := []string{"0.9819999999999999", "1500", "0.982", "1e3"}

	for _, in := range tests {
		t.Run(in, func(t *testing.T) {
			var d Dec
			if err := json.Unmarshal([]byte(in), &d); err != nil {
				t.Fatalf("Unmarshal(%s) returned error: %v", in, err)
			}
			if d.Raw() != in {
				t.Errorf("Raw() = %q, want the source literal %q", d.Raw(), in)
			}
		})
	}
}

func TestUnmarshalJSONNullIsAbsent(t *testing.T) {
	d := Parse("0.5")
	if err := json.Unmarshal([]byte("null"), &d); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if !d.Absent() {
		t.Errorf("Absent() = false after unmarshalling null, Raw() = %q", d.Raw())
	}
}

func TestUnmarshalJSONRejectsWrongTypes(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"object", `{"price":"0.98"}`},
		{"array", `["0.98"]`},
		{"bool", `true`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Dec
			if err := json.Unmarshal([]byte(tt.in), &d); err == nil {
				t.Fatalf("Unmarshal(%s) succeeded, want an error", tt.in)
			}
		})
	}
}

// Round-tripping a decoded document must not perturb any value, which is what
// makes the scraper safe to put between the API and the consuming agent.
func TestRoundTripThroughAStruct(t *testing.T) {
	const in = `{"price":"0.9819999999999999","size":"2200.8","tick":null}`

	var level struct {
		Price Dec `json:"price"`
		Size  Dec `json:"size"`
		Tick  Dec `json:"tick"`
	}
	if err := json.Unmarshal([]byte(in), &level); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}

	out, err := json.Marshal(level)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if string(out) != in {
		t.Errorf("round trip changed the document:\n got %s\nwant %s", out, in)
	}
	if strings.Contains(string(out), "0.98199999999999995") {
		t.Error("output shows float64 artifacts")
	}
}
