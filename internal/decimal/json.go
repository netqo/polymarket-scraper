package decimal

import (
	"encoding/json"
	"fmt"
)

// jsonNull is the encoding of an absent value.
var jsonNull = []byte("null")

// MarshalJSON writes the value as a JSON string containing the original text.
//
// The happy path assembles the output by hand rather than calling json.Marshal,
// which is what makes the byte-for-byte guarantee in requirement C6 mechanical
// rather than a matter of trusting the encoder. Text that is not a plain
// decimal literal takes the standard escaping path instead, so an unexpected
// value can never inject structure into the document.
func (d Dec) MarshalJSON() ([]byte, error) {
	if d.Absent() {
		return jsonNull, nil
	}
	if !isPlainDecimal(d.raw) {
		return json.Marshal(d.raw)
	}

	out := make([]byte, 0, len(d.raw)+2)
	out = append(out, '"')
	out = append(out, d.raw...)
	out = append(out, '"')

	return out, nil
}

// UnmarshalJSON reads a value from either a JSON string or a JSON number.
//
// A number is captured from its literal source bytes rather than decoded into
// a float64, so a field that Polymarket one day changes from string to number
// still round-trips exactly.
func (d *Dec) UnmarshalJSON(b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("decimal: empty JSON value")
	}

	switch b[0] {
	case 'n': // null
		*d = Dec{}
		return nil

	case '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return fmt.Errorf("decimal: %w", err)
		}
		*d = Parse(s)
		return nil

	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		*d = Parse(string(b))
		return nil

	default:
		return fmt.Errorf("decimal: cannot decode %s into a decimal", b)
	}
}

// isPlainDecimal reports whether s consists only of characters that are safe to
// place inside a JSON string without escaping. It is deliberately stricter than
// the JSON escaping rules: anything outside digits, a minus and a dot goes down
// the escaping path.
func isPlainDecimal(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) && s[i] != '-' && s[i] != '.' {
			return false
		}
	}

	return true
}
