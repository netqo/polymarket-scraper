// Test data: Invented prices, with one exception worth knowing about: the ordering tests feed
// the order the live API actually sends, which is the opposite of what its
// documentation claims. See PROTOCOL.md. Everything else here is a property of the
// data structure and holds for any prices at all.

package book

import (
	"slices"
	"testing"

	"github.com/netqo/polymarket-scraper/internal/decimal"
)

// lvl builds a level from decimal text.
func lvl(price, size string) Level {
	return Level{Price: decimal.Parse(price), Size: decimal.Parse(size)}
}

// prices lists a side's prices in stored order, which is output order.
func prices(t *testing.T, b *Book, s Side) []string {
	t.Helper()

	var out []string
	for _, level := range b.Levels(s, 0) {
		out = append(out, level.Price.Raw())
	}

	return out
}

// The live API returns bids ascending and asks descending, the opposite of what
// its own documentation claims. Sorting on ingest rather than trusting either
// is what keeps the best price at the top, and this test feeds the order the
// real API actually sends.
func TestReplaceSortsIntoOutputOrder(t *testing.T) {
	tests := []struct {
		name  string
		side  Side
		input []Level
		want  []string
	}{
		{
			name:  "bids arriving ascending are stored descending",
			side:  Bids,
			input: []Level{lvl("0.001", "1"), lvl("0.002", "1"), lvl("0.004", "1")},
			want:  []string{"0.004", "0.002", "0.001"},
		},
		{
			name:  "asks arriving descending are stored ascending",
			side:  Asks,
			input: []Level{lvl("0.999", "1"), lvl("0.95", "1"), lvl("0.009", "1")},
			want:  []string{"0.009", "0.95", "0.999"},
		},
		{
			name:  "already ordered input is left alone",
			side:  Bids,
			input: []Level{lvl("0.9", "1"), lvl("0.5", "1"), lvl("0.1", "1")},
			want:  []string{"0.9", "0.5", "0.1"},
		},
		{
			name:  "string ordering would get this wrong",
			side:  Asks,
			input: []Level{lvl("0.9", "1"), lvl("0.10", "1"), lvl("0.2", "1")},
			want:  []string{"0.10", "0.2", "0.9"},
		},
		{
			name:  "empty side",
			side:  Bids,
			input: nil,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b Book
			b.Replace(tt.side, tt.input)

			if got := prices(t, &b, tt.side); !slices.Equal(got, tt.want) {
				t.Errorf("%s = %v, want %v", tt.side, got, tt.want)
			}
		})
	}
}

// D1: a level the scraper cannot interpret is still reported. It goes last so
// it cannot be mistaken for the best price.
func TestReplaceKeepsUninterpretableLevelsAtTheEnd(t *testing.T) {
	var b Book
	b.Replace(Bids, []Level{lvl("0.1", "1"), lvl("nonsense", "1"), lvl("0.9", "1")})

	want := []string{"0.9", "0.1", "nonsense"}
	if got := prices(t, &b, Bids); !slices.Equal(got, want) {
		t.Errorf("bids = %v, want %v", got, want)
	}

	if _, ok := b.Best(Bids); !ok {
		t.Error("Best(Bids) reported nothing despite a usable level")
	}
}

// A snapshot that spells one price two ways describes one level. Keeping both
// leaves an entry that no delta can reach, because a deletion removes only the
// one the search finds, and what is left is liquidity that does not exist.
func TestReplaceCollapsesLevelsAtTheSamePrice(t *testing.T) {
	var b Book
	b.Replace(Bids, []Level{lvl("0.98", "100"), lvl("0.980", "250"), lvl("0.97", "10")})

	if got := prices(t, &b, Bids); !slices.Equal(got, []string{"0.98", "0.97"}) {
		t.Fatalf("bids = %v, want the duplicate price collapsed", got)
	}

	b.Apply(Bids, lvl("0.980", "0"))

	if got := prices(t, &b, Bids); !slices.Equal(got, []string{"0.97"}) {
		t.Errorf("bids = %v after deleting 0.980, want the level gone entirely", got)
	}
}

// Levels with no usable numeric form are identified by their text, so two
// different unparseable prices are two different levels and must both survive.
func TestReplaceKeepsDistinctUninterpretableLevels(t *testing.T) {
	var b Book
	b.Replace(Bids, []Level{lvl("nonsense", "1"), lvl("other", "1")})

	if got := b.Len(Bids); got != 2 {
		t.Errorf("bids hold %d levels, want both unparseable prices kept: %v", got, prices(t, &b, Bids))
	}
}

func TestReplaceDiscardsThePreviousSnapshot(t *testing.T) {
	var b Book
	b.Replace(Bids, []Level{lvl("0.5", "1"), lvl("0.4", "1")})
	b.Replace(Bids, []Level{lvl("0.6", "1")})

	if got := prices(t, &b, Bids); !slices.Equal(got, []string{"0.6"}) {
		t.Errorf("bids = %v, want [0.6]", got)
	}
}

// The single most important semantic in the protocol: size zero removes the
// level rather than setting it to zero. Leaving a zero-size level behind is
// phantom liquidity.
func TestApplyZeroSizeDeletesTheLevel(t *testing.T) {
	var b Book
	b.Replace(Asks, []Level{lvl("0.10", "5"), lvl("0.20", "5"), lvl("0.30", "5")})

	b.Apply(Asks, lvl("0.20", "0"))

	want := []string{"0.10", "0.30"}
	if got := prices(t, &b, Asks); !slices.Equal(got, want) {
		t.Errorf("asks = %v, want %v", got, want)
	}
}

func TestApplyZeroSizeOnAnAbsentLevelDoesNothing(t *testing.T) {
	var b Book
	b.Replace(Asks, []Level{lvl("0.10", "5")})

	b.Apply(Asks, lvl("0.20", "0"))

	if got := prices(t, &b, Asks); !slices.Equal(got, []string{"0.10"}) {
		t.Errorf("asks = %v, want [0.10]", got)
	}
}

func TestApplyInsertsInOrder(t *testing.T) {
	tests := []struct {
		name  string
		side  Side
		seed  []Level
		apply Level
		want  []string
	}{
		{"ask at the head", Asks, []Level{lvl("0.5", "1")}, lvl("0.1", "1"), []string{"0.1", "0.5"}},
		{"ask at the tail", Asks, []Level{lvl("0.5", "1")}, lvl("0.9", "1"), []string{"0.5", "0.9"}},
		{"ask in the middle", Asks, []Level{lvl("0.1", "1"), lvl("0.9", "1")}, lvl("0.5", "1"), []string{"0.1", "0.5", "0.9"}},
		{"bid at the head", Bids, []Level{lvl("0.5", "1")}, lvl("0.9", "1"), []string{"0.9", "0.5"}},
		{"bid at the tail", Bids, []Level{lvl("0.5", "1")}, lvl("0.1", "1"), []string{"0.5", "0.1"}},
		{"bid in the middle", Bids, []Level{lvl("0.9", "1"), lvl("0.1", "1")}, lvl("0.5", "1"), []string{"0.9", "0.5", "0.1"}},
		{"into an empty side", Asks, nil, lvl("0.5", "1"), []string{"0.5"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b Book
			b.Replace(tt.side, tt.seed)
			b.Apply(tt.side, tt.apply)

			if got := prices(t, &b, tt.side); !slices.Equal(got, tt.want) {
				t.Errorf("%s = %v, want %v", tt.side, got, tt.want)
			}
		})
	}
}

// Levels are the same level if they are the same number, so a delta quoted as
// "0.980" updates the level seeded as "0.98" rather than creating a second one.
// The stored text comes from whichever message touched it most recently, which
// is what the output has to show.
func TestApplyMatchesLevelsByValueNotByText(t *testing.T) {
	var b Book
	b.Replace(Asks, []Level{lvl("0.98", "100")})

	b.Apply(Asks, lvl("0.980", "250"))

	levels := b.Levels(Asks, 0)
	if len(levels) != 1 {
		t.Fatalf("got %d levels, want 1: %v", len(levels), prices(t, &b, Asks))
	}
	if levels[0].Price.Raw() != "0.980" {
		t.Errorf("price text = %q, want the most recent spelling %q", levels[0].Price.Raw(), "0.980")
	}
	if levels[0].Size.Raw() != "250" {
		t.Errorf("size = %q, want 250", levels[0].Size.Raw())
	}
}

func TestApplyDeletesByValueNotByText(t *testing.T) {
	var b Book
	b.Replace(Asks, []Level{lvl("0.98", "100")})

	b.Apply(Asks, lvl("0.980", "0"))

	if got := b.Len(Asks); got != 0 {
		t.Errorf("level survived deletion under a different spelling: %v", prices(t, &b, Asks))
	}
}

func TestBest(t *testing.T) {
	tests := []struct {
		name    string
		side    Side
		levels  []Level
		want    string
		wantAny bool
	}{
		{"best bid is the highest", Bids, []Level{lvl("0.1", "1"), lvl("0.9", "1")}, "0.9", true},
		{"best ask is the lowest", Asks, []Level{lvl("0.9", "1"), lvl("0.1", "1")}, "0.1", true},
		{"empty side has none", Bids, nil, "", false},
		{"uninterpretable prices only", Asks, []Level{lvl("nonsense", "1")}, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b Book
			b.Replace(tt.side, tt.levels)

			got, ok := b.Best(tt.side)
			if ok != tt.wantAny {
				t.Fatalf("Best(%v) ok = %v, want %v", tt.side, ok, tt.wantAny)
			}
			if ok && got.Price.Raw() != tt.want {
				t.Errorf("Best(%v) = %q, want %q", tt.side, got.Price.Raw(), tt.want)
			}
		})
	}
}

// C8: a crossed book is flagged, never repaired. Equality counts as crossed,
// because a bid at the ask is not a state a live book should be in either.
func TestCrossed(t *testing.T) {
	tests := []struct {
		name string
		bids []Level
		asks []Level
		want bool
	}{
		{"normal book", []Level{lvl("0.97", "1")}, []Level{lvl("0.98", "1")}, false},
		{"one tick apart", []Level{lvl("0.979", "1")}, []Level{lvl("0.980", "1")}, false},
		{"locked at the same price", []Level{lvl("0.98", "1")}, []Level{lvl("0.98", "1")}, true},
		{"locked despite different spellings", []Level{lvl("0.98", "1")}, []Level{lvl("0.980", "1")}, true},
		{"genuinely crossed", []Level{lvl("0.99", "1")}, []Level{lvl("0.98", "1")}, true},
		{"one sided book is not crossed", []Level{lvl("0.99", "1")}, nil, false},
		{"empty book is not crossed", nil, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b Book
			b.Replace(Bids, tt.bids)
			b.Replace(Asks, tt.asks)

			if got := b.Crossed(); got != tt.want {
				t.Errorf("Crossed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSpread(t *testing.T) {
	var b Book
	b.Replace(Bids, []Level{lvl("0.978", "1")})
	b.Replace(Asks, []Level{lvl("0.982", "1")})

	got, ok := b.Spread()
	if !ok {
		t.Fatal("Spread reported nothing for a two-sided book")
	}
	if want := int64(4_000_000); got != want {
		t.Errorf("Spread() = %d, want %d", got, want)
	}

	var oneSided Book
	oneSided.Replace(Bids, []Level{lvl("0.978", "1")})
	if _, ok := oneSided.Spread(); ok {
		t.Error("Spread reported a value for a one-sided book")
	}
}

func TestLevelsTruncatesAndCopies(t *testing.T) {
	var b Book
	b.Replace(Bids, []Level{lvl("0.9", "1"), lvl("0.5", "1"), lvl("0.1", "1")})

	top := b.Levels(Bids, 2)
	if len(top) != 2 {
		t.Fatalf("Levels(Bids, 2) returned %d levels, want 2", len(top))
	}

	// Mutating the result must not reach into the book.
	top[0] = lvl("9.9", "9")
	if best, _ := b.Best(Bids); best.Price.Raw() != "0.9" {
		t.Errorf("the book was modified through a returned slice: best bid is now %q", best.Price.Raw())
	}

	if all := b.Levels(Bids, 0); len(all) != 3 {
		t.Errorf("Levels(Bids, 0) returned %d levels, want all 3", len(all))
	}
	if more := b.Levels(Bids, 99); len(more) != 3 {
		t.Errorf("Levels(Bids, 99) returned %d levels, want all 3", len(more))
	}
}

// Whatever sequence of operations is applied, both sides must still be in
// output order afterwards: everything downstream reads index zero as the best.
func TestOrderingSurvivesAnUpdateSequence(t *testing.T) {
	var b Book
	b.Replace(Bids, []Level{lvl("0.40", "1"), lvl("0.60", "1"), lvl("0.50", "1")})
	b.Replace(Asks, []Level{lvl("0.80", "1"), lvl("0.70", "1")})

	b.Apply(Bids, lvl("0.55", "2"))
	b.Apply(Bids, lvl("0.60", "0"))
	b.Apply(Bids, lvl("0.30", "3"))
	b.Apply(Asks, lvl("0.75", "1"))
	b.Apply(Asks, lvl("0.70", "0"))
	b.Apply(Asks, lvl("0.65", "1"))

	wantBids := []string{"0.55", "0.50", "0.40", "0.30"}
	if got := prices(t, &b, Bids); !slices.Equal(got, wantBids) {
		t.Errorf("bids = %v, want %v", got, wantBids)
	}

	wantAsks := []string{"0.65", "0.75", "0.80"}
	if got := prices(t, &b, Asks); !slices.Equal(got, wantAsks) {
		t.Errorf("asks = %v, want %v", got, wantAsks)
	}

	assertSorted(t, &b, Bids)
	assertSorted(t, &b, Asks)
}

func assertSorted(t *testing.T, b *Book, s Side) {
	t.Helper()

	levels := b.Levels(s, 0)
	for i := 1; i < len(levels); i++ {
		if s.compare(levels[i-1], levels[i]) > 0 {
			t.Errorf("%s are out of order at index %d: %q then %q",
				s, i, levels[i-1].Price.Raw(), levels[i].Price.Raw())
		}
	}
}

func TestSideString(t *testing.T) {
	if Bids.String() != "bids" || Asks.String() != "asks" {
		t.Errorf("side names are %q and %q", Bids.String(), Asks.String())
	}
}
