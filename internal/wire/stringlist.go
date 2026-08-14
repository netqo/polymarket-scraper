package wire

import (
	"encoding/json"
	"fmt"
)

// StringList is a list of strings that tolerates being sent as a JSON string
// containing a JSON array.
//
// Polymarket serialises list-valued fields inconsistently: some endpoints send
// outcomes as ["Yes","No"] and others as "[\"Yes\",\"No\"]". Both forms mean the
// same thing, and which one arrives is not something the rest of the scraper
// should have to care about, so the tolerance lives here in the anti-corruption
// layer rather than at every use site.
type StringList []string

// UnmarshalJSON accepts either an array of strings or a string holding one.
func (l *StringList) UnmarshalJSON(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("empty JSON value")
	}

	switch data[0] {
	case 'n': // null
		*l = nil
		return nil

	case '[':
		var items []string
		if err := json.Unmarshal(data, &items); err != nil {
			return fmt.Errorf("decoding string list: %w", err)
		}
		*l = items
		return nil

	case '"':
		var encoded string
		if err := json.Unmarshal(data, &encoded); err != nil {
			return fmt.Errorf("decoding quoted string list: %w", err)
		}

		var items []string
		if err := json.Unmarshal([]byte(encoded), &items); err != nil {
			return fmt.Errorf("decoding string list from the quoted value %q: %w", encoded, err)
		}
		*l = items
		return nil

	default:
		return fmt.Errorf("cannot decode %s as a list of strings", preview(data))
	}
}

// MarshalJSON always writes the plain array form, so the scraper's own output
// is consistent regardless of which form arrived.
func (l StringList) MarshalJSON() ([]byte, error) {
	if l == nil {
		return []byte("null"), nil
	}

	return json.Marshal([]string(l))
}
