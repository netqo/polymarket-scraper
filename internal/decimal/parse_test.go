// Test data: Invented values, but the valid cases list the shapes the Polymarket feed actually
// emits, and the overflow cases are sizes it can genuinely produce. Whether it ever
// emits the invalid ones is an open question recorded in the working notes.

package decimal

import (
	"math"
	"strconv"
	"testing"
)

func TestParseValidValues(t *testing.T) {
	tests := []struct {
		in       string
		wantNano int64
	}{
		// Shapes the Polymarket feed actually emits.
		{"0", 0},
		{"1", 1_000_000_000},
		{"0.982", 982_000_000},
		{"0.001", 1_000_000},
		{"1500", 1_500_000_000_000},
		{"2200.8", 2_200_800_000_000},
		{"0.9819999999999999", 981_999_999},

		// Trailing and leading zeros must not change the numeric value, even
		// though they must survive verbatim in the raw text.
		{"0.980", 980_000_000},
		{"0.98", 980_000_000},
		{"1.0", 1_000_000_000},
		{"0000.5", 500_000_000},
		{"0.000", 0},

		// Negatives are not expected from the feed but must not be silently
		// mangled if they ever appear.
		{"-0.5", -500_000_000},
		{"-1", -1_000_000_000},
		{"-0", 0},

		// More than nine fractional digits truncates toward zero. Nano is a
		// million times finer than the smallest tick, so this cannot affect any
		// ordering decision the scraper makes.
		{"0.1234567891", 123_456_789},
		{"-0.1234567891", -123_456_789},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := Parse(tt.in)

			if !got.Valid() {
				t.Fatalf("Parse(%q).Valid() = false, want true", tt.in)
			}
			if got.Raw() != tt.in {
				t.Errorf("Parse(%q).Raw() = %q, want the input verbatim", tt.in, got.Raw())
			}

			nano, ok := got.Nano()
			if !ok {
				t.Fatalf("Parse(%q).Nano() reported not ok", tt.in)
			}
			if nano != tt.wantNano {
				t.Errorf("Parse(%q) nano = %d, want %d", tt.in, nano, tt.wantNano)
			}
		})
	}
}

func TestParseInvalidValuesKeepTheirRawText(t *testing.T) {
	// D1: the scraper never drops data it cannot interpret. An unparseable
	// value is still reported verbatim, it is just excluded from comparison.
	tests := []string{
		"abc",
		"1e-3",
		"1E3",
		"+1",
		" 1",
		"1 ",
		"1.",
		".5",
		"1.2.3",
		"--1",
		"-",
		"0x10",
		"NaN",
		"Infinity",
		"1,000",
	}

	for _, in := range tests {
		t.Run(in, func(t *testing.T) {
			got := Parse(in)

			if got.Valid() {
				t.Errorf("Parse(%q).Valid() = true, want false", in)
			}
			if got.Raw() != in {
				t.Errorf("Parse(%q).Raw() = %q, want the input verbatim", in, got.Raw())
			}
			if _, ok := got.Nano(); ok {
				t.Errorf("Parse(%q).Nano() reported ok for an invalid value", in)
			}
		})
	}
}

func TestParseEmptyStringIsAbsent(t *testing.T) {
	got := Parse("")

	if !got.Absent() {
		t.Error("Parse(\"\").Absent() = false, want true")
	}
	if got.Valid() {
		t.Error("Parse(\"\").Valid() = true, want false")
	}
	if got != (Dec{}) {
		t.Error("Parse(\"\") did not produce the zero Dec")
	}
}

// Sizes can legitimately exceed what fits in int64 nano. That is harmless
// because only prices are ever compared or sorted, but it must degrade to an
// invalid Dec rather than wrapping around into a plausible wrong number.
func TestParseOverflowIsInvalidNotWrapped(t *testing.T) {
	tests := []string{
		"9223372037",           // just past MaxInt64 / 1e9
		"99999999999999999999", // far past
		"9223372036.854775808", // MaxInt64 nano plus one
		strconv.FormatInt(math.MaxInt64, 10),
	}

	for _, in := range tests {
		t.Run(in, func(t *testing.T) {
			got := Parse(in)

			if got.Valid() {
				nano, _ := got.Nano()
				t.Errorf("Parse(%q).Valid() = true (nano %d), want false", in, nano)
			}
			if got.Raw() != in {
				t.Errorf("Parse(%q).Raw() = %q, want the input verbatim", in, got.Raw())
			}
		})
	}
}

func TestParseLargestRepresentableValue(t *testing.T) {
	// MaxInt64 nano is 9223372036.854775807 exactly.
	const in = "9223372036.854775807"

	got := Parse(in)
	if !got.Valid() {
		t.Fatalf("Parse(%q).Valid() = false, want true", in)
	}

	nano, _ := got.Nano()
	if nano != math.MaxInt64 {
		t.Errorf("Parse(%q) nano = %d, want %d", in, nano, int64(math.MaxInt64))
	}
}

func TestIsZero(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"0", true},
		{"0.0", true},
		{"0.000", true},
		{"-0", true},
		{"0.001", false},
		{"1", false},
		{"", false},    // absent is not a numeric zero
		{"abc", false}, // unparseable is not a numeric zero
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := Parse(tt.in).IsZero(); got != tt.want {
				t.Errorf("Parse(%q).IsZero() = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// The raw text is the only thing that ever reaches the output document, so no
// input may alter it. This is requirement C6 in its strongest form.
func FuzzParsePreservesRawText(f *testing.F) {
	for _, seed := range []string{"0.982", "1500", "", "abc", "-0.0001", "1e9", "0.9819999999999999"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		got := Parse(in)

		if got.Raw() != in {
			t.Fatalf("Parse(%q).Raw() = %q, want the input verbatim", in, got.Raw())
		}

		// Validity may be false for a well-formed decimal that overflows int64
		// nano, so only the one-way implication is a real invariant: anything
		// declared valid must genuinely be a plain decimal literal.
		if got.Valid() && !isPlainDecimalLiteral(in) {
			t.Fatalf("Parse(%q).Valid() = true for a non-decimal input", in)
		}
	})
}

// isPlainDecimalLiteral reports whether s is a plain decimal literal. It is
// written independently of the production scanner so the fuzz test is a real
// cross-check rather than a restatement of the implementation.
func isPlainDecimalLiteral(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '-' {
		s = s[1:]
	}

	intDigits, fracDigits, seenDot := 0, 0, false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '.' && !seenDot:
			seenDot = true
		case s[i] >= '0' && s[i] <= '9':
			if seenDot {
				fracDigits++
			} else {
				intDigits++
			}
		default:
			return false
		}
	}

	return intDigits > 0 && (!seenDot || fracDigits > 0)
}
