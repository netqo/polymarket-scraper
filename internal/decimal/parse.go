package decimal

import "math"

// Parse converts an API decimal string into a Dec.
//
// It never fails. Text that is not a plain decimal literal, and text that is
// one but overflows the fixed-point form, are both returned with the raw bytes
// intact and no usable numeric form. That is deliberate: requirement D1 says
// the scraper never drops data, so an odd-looking value is still reported to
// the consuming agent, it is simply excluded from comparison and flagged.
//
// The accepted grammar is -?[0-9]+(\.[0-9]+)? and nothing else. Scientific
// notation, a leading plus, surrounding whitespace and thousands separators are
// all rejected rather than guessed at, because a wrong guess about a price is
// far worse than an explicitly unparseable one.
func Parse(s string) Dec {
	nano, valid := parseNano(s)
	return Dec{raw: s, nano: nano, valid: valid}
}

// parseNano scans s and returns its value scaled by 1e9, or ok=false if s does
// not match the accepted grammar or does not fit in an int64.
func parseNano(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}

	i := 0
	negative := s[0] == '-'
	if negative {
		i++
	}

	integerStart := i
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	if i == integerStart {
		return 0, false // no integer digits: "-", ".5", "abc"
	}
	integer := s[integerStart:i]

	var fraction string
	if i < len(s) {
		if s[i] != '.' {
			return 0, false // trailing junk: "1e-3", "1 ", "1,000"
		}
		i++

		fractionStart := i
		for i < len(s) && isDigit(s[i]) {
			i++
		}
		if i == fractionStart || i != len(s) {
			return 0, false // bare dot ("1.") or junk after the fraction
		}
		fraction = s[fractionStart:i]
	}

	value, ok := scaleDigits(integer, fraction)
	if !ok {
		return 0, false
	}
	if negative {
		value = -value
	}

	return value, true
}

// scaleDigits combines the integer and fractional digit runs into a fixed-point
// magnitude, reporting ok=false on overflow. The fraction is truncated toward
// zero past Scale digits.
//
// The loops over digit runs are indexed rather than ranged deliberately. These
// walk bytes, and "for i := range integer" over a string walks runes instead,
// which for a decimal literal happens to give the same answer right up until it
// does not. Spelling the bound out is what keeps a later simplification from
// quietly changing the unit.
func scaleDigits(integer, fraction string) (int64, bool) {
	const unit = int64(1_000_000_000) // 10^Scale

	var value int64
	for i := 0; i < len(integer); i++ {
		digit := int64(integer[i] - '0')
		if value > math.MaxInt64/10 {
			return 0, false
		}
		value *= 10
		if value > math.MaxInt64-digit {
			return 0, false
		}
		value += digit
	}

	if value > math.MaxInt64/unit {
		return 0, false
	}
	value *= unit

	var scaled int64
	for i := range Scale {
		scaled *= 10
		if i < len(fraction) {
			scaled += int64(fraction[i] - '0')
		}
	}

	if value > math.MaxInt64-scaled {
		return 0, false
	}

	return value + scaled, true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
