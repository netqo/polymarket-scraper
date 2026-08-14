// Package decimal preserves API-provided decimal values byte for byte.
//
// Requirement C6 forbids re-parsing the API's decimal strings into floats and
// serializing them back: "0.982" must leave the scraper as "0.982", never as
// 0.9819999999999999. But sorting an order book, matching a delta to the level
// it updates, detecting a crossed book and computing window statistics all need
// numeric comparison. Those two demands pull in opposite directions.
//
// A Dec resolves the tension by carrying two independent representations. The
// raw text is the only thing ever serialized. The fixed-point integer, in units
// of 1e-9, is derived once at parse time and used only for comparison and
// derived statistics; it has no path to the output document. A caller that only
// ever writes Dec values into the report therefore cannot introduce a float
// artifact, no matter what it does with the numeric form.
//
// Fixed-point int64 is exact for every value Polymarket can produce and is a
// million times finer than the smallest tick size (0.001). Sizes large enough
// to overflow it degrade to an invalid Dec with the text intact, which is
// harmless: only prices are ever compared or sorted, and prices are bounded in
// the interval [0, 1].
package decimal

import (
	"strconv"
	"strings"
)

// Scale is the number of fractional digits in the fixed-point representation.
// A tick size of 0.001 is six orders of magnitude coarser, so truncating at
// this scale cannot change any ordering decision the scraper makes.
const Scale = 9

// Dec is an API decimal value.
//
// The zero Dec is "absent": it carries no text and serializes to JSON null,
// which is how a field the feed never provided is reported (requirement D3).
// Dec is comparable, so it can be used as a map key and compared with ==,
// but == compares the text, not the value: use Cmp for numeric ordering.
type Dec struct {
	raw   string // exactly the bytes the API sent; the only thing serialized
	nano  int64  // value scaled by 1e9; comparison and statistics only
	valid bool   // nano is usable: the text parsed and did not overflow
}

// Raw returns the original text, exactly as the API sent it.
func (d Dec) Raw() string { return d.raw }

// Absent reports whether the value was never provided. An absent Dec
// serializes to null.
func (d Dec) Absent() bool { return d.raw == "" }

// Valid reports whether the value has a usable numeric form. It is false for
// an absent value, for text that is not a plain decimal literal, and for a
// value too large to represent in fixed point.
func (d Dec) Valid() bool { return d.valid }

// Nano returns the value scaled by 1e9, and whether that form is usable.
// Callers must check the second result: an invalid Dec returns zero, which is
// otherwise indistinguishable from a genuine zero.
func (d Dec) Nano() (int64, bool) { return d.nano, d.valid }

// IsZero reports whether the value is numerically zero.
//
// This is the test behind the single most important order book semantic in the
// Polymarket protocol: a price change with size zero removes the level rather
// than setting it to nothing. An absent or unparseable value is not zero.
func (d Dec) IsZero() bool { return d.valid && d.nano == 0 }

// Cmp orders two decimals, returning -1, 0 or 1.
//
// It is a total order, which matters because the book is kept sorted and a
// partial order would make sorting undefined. Values with a usable numeric form
// are ordered by value, so "0.98" and "0.980" compare equal. Values without one
// sort after all of them, ordered by their text so the result stays
// deterministic across runs.
func Cmp(a, b Dec) int {
	switch {
	case a.valid && b.valid:
		switch {
		case a.nano < b.nano:
			return -1
		case a.nano > b.nano:
			return 1
		default:
			return 0
		}
	case a.valid:
		return -1
	case b.valid:
		return 1
	default:
		return strings.Compare(a.raw, b.raw)
	}
}

// FromScaled builds a Dec from a fixed-point integer with the given number of
// fractional digits, formatting it exactly.
//
// This is how derived statistics enter the output document. Formatting goes
// through integer arithmetic and string assembly rather than any floating-point
// conversion, so a computed value carries the same no-artifacts guarantee as a
// value that came straight off the wire. Trailing fractional zeros are trimmed,
// so 982000000 at scale 9 formats as "0.982" rather than "0.982000000".
func FromScaled(value int64, decimals int) Dec {
	if decimals <= 0 {
		return Parse(strconv.FormatInt(value, 10))
	}

	// Take the magnitude through its decimal text rather than by negating, so
	// that math.MinInt64, which has no positive counterpart, needs no special
	// case and no unsigned arithmetic.
	digits := strconv.FormatInt(value, 10)
	negative := strings.HasPrefix(digits, "-")
	digits = strings.TrimPrefix(digits, "-")

	if len(digits) <= decimals {
		digits = strings.Repeat("0", decimals-len(digits)+1) + digits
	}

	split := len(digits) - decimals
	integer := digits[:split]
	fraction := strings.TrimRight(digits[split:], "0")

	var text strings.Builder
	if negative && (integer != "0" || fraction != "") {
		text.WriteByte('-')
	}
	text.WriteString(integer)
	if fraction != "" {
		text.WriteByte('.')
		text.WriteString(fraction)
	}

	return Parse(text.String())
}
