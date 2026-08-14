package decimal

import (
	"slices"
	"testing"
)

func TestCmpOrdersByValueNotByText(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		// Trailing zeros are the case that breaks naive string comparison, and
		// it is exactly what delta matching depends on: "0.98" and "0.980" are
		// the same price level.
		{"0.98", "0.980", 0},
		{"0.98", "0.98", 0},
		{"1", "1.000000000", 0},
		{"0", "-0", 0},

		{"0.978", "0.982", -1},
		{"0.982", "0.978", 1},
		{"0.9", "0.10", 1}, // string comparison would get this backwards
		{"2", "10", -1},    // and this
		{"-1", "1", -1},
	}

	for _, tt := range tests {
		t.Run(tt.a+" vs "+tt.b, func(t *testing.T) {
			if got := Cmp(Parse(tt.a), Parse(tt.b)); got != tt.want {
				t.Errorf("Cmp(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
			if got := Cmp(Parse(tt.b), Parse(tt.a)); got != -tt.want {
				t.Errorf("Cmp(%q, %q) = %d, want %d (antisymmetry)", tt.b, tt.a, got, -tt.want)
			}
		})
	}
}

// Comparison must be a total order even in the presence of values that could
// not be parsed, or sorting the book would be undefined behaviour.
func TestCmpIsATotalOrderWithInvalidValues(t *testing.T) {
	if got := Cmp(Parse("abc"), Parse("0.98")); got != 1 {
		t.Errorf("Cmp(invalid, valid) = %d, want 1 (invalid sorts last)", got)
	}
	if got := Cmp(Parse("0.98"), Parse("abc")); got != -1 {
		t.Errorf("Cmp(valid, invalid) = %d, want -1", got)
	}
	if got := Cmp(Parse("abc"), Parse("abc")); got != 0 {
		t.Errorf("Cmp of identical invalid values = %d, want 0", got)
	}
	if got := Cmp(Parse("abc"), Parse("abd")); got != -1 {
		t.Errorf("Cmp of distinct invalid values = %d, want -1 (ordered by text)", got)
	}
	if got := Cmp(Dec{}, Parse("abc")); got != -1 {
		t.Errorf("Cmp(absent, invalid) = %d, want -1", got)
	}
}

func TestSortingUsesNumericOrder(t *testing.T) {
	prices := []Dec{
		Parse("0.999"), Parse("0.01"), Parse("abc"), Parse("0.1"), Parse("0.020"),
	}
	slices.SortFunc(prices, Cmp)

	var got []string
	for _, p := range prices {
		got = append(got, p.Raw())
	}

	want := []string{"0.01", "0.020", "0.1", "0.999", "abc"}
	if !slices.Equal(got, want) {
		t.Errorf("sorted order = %v, want %v", got, want)
	}
}

func TestFromScaledFormatsExactly(t *testing.T) {
	tests := []struct {
		name     string
		value    int64
		decimals int
		want     string
	}{
		{"whole number", 5, 0, "5"},
		{"nano one", 1_000_000_000, 9, "1"},
		{"nano tick", 1_000_000, 9, "0.001"},
		{"nano trims trailing zeros", 982_000_000, 9, "0.982"},
		{"nano keeps precision", 123_456_789, 9, "0.123456789"},
		{"zero", 0, 9, "0"},
		{"negative", -500_000_000, 9, "-0.5"},
		{"below one", 5, 10, "0.0000000005"},
		{"mid price in half nano", 4_910_000_000, 10, "0.491"},
		{"integer plus fraction", 2_200_800_000_000, 9, "2200.8"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FromScaled(tt.value, tt.decimals)

			if got.Raw() != tt.want {
				t.Errorf("FromScaled(%d, %d).Raw() = %q, want %q", tt.value, tt.decimals, got.Raw(), tt.want)
			}
			if !got.Valid() {
				t.Errorf("FromScaled(%d, %d) is not valid", tt.value, tt.decimals)
			}
		})
	}
}

// A derived statistic must survive its own round trip, or the numbers the
// consuming agent reads are not the numbers we computed.
func TestFromScaledRoundTripsThroughParse(t *testing.T) {
	values := []int64{0, 1, -1, 982_000_000, 123_456_789, 9_223_372_036_854_775_807}

	for _, v := range values {
		d := FromScaled(v, 9)

		nano, ok := Parse(d.Raw()).Nano()
		if !ok {
			t.Fatalf("FromScaled(%d, 9) produced unparseable text %q", v, d.Raw())
		}
		if nano != v {
			t.Errorf("FromScaled(%d, 9) = %q, which parses back as %d", v, d.Raw(), nano)
		}
	}
}
