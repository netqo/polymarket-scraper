package engine

import "testing"

func TestChunk(t *testing.T) {
	tests := []struct {
		name string
		ids  []string
		size int
		want int
	}{
		{"exact multiple", []string{"a", "b", "c", "d"}, 2, 2},
		{"with a remainder", []string{"a", "b", "c"}, 2, 2},
		{"one batch", []string{"a", "b"}, 10, 1},
		{"empty", nil, 2, 0},
		{"invalid size", []string{"a"}, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(chunk(tt.ids, tt.size)); got != tt.want {
				t.Errorf("chunk produced %d batches, want %d", got, tt.want)
			}
		})
	}
}
