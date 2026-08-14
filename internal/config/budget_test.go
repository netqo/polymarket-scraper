package config

import (
	"testing"
	"time"
)

// A4: the process must terminate within the window plus the grace period, even
// on hung connections. The timeline is derived arithmetic, so it is checked
// here with no clock, no sleeps and no goroutines.
func TestNewBudgetSplitsTheGracePeriod(t *testing.T) {
	got := NewBudget(90*time.Second, 30*time.Second)

	want := Budget{
		Sweep:    75 * time.Second,
		Collect:  90 * time.Second,
		Drain:    97*time.Second + 500*time.Millisecond,
		Finalize: 105 * time.Second,
		HardStop: 120 * time.Second,
	}
	if got != want {
		t.Errorf("NewBudget(90s, 30s) =\n %+v\nwant\n %+v", got, want)
	}
}

func TestBudgetStagesAreOrdered(t *testing.T) {
	tests := []struct {
		name    string
		collect time.Duration
		grace   time.Duration
	}{
		{"defaults", 90 * time.Second, 30 * time.Second},
		{"short window", 2 * time.Second, 30 * time.Second},
		{"very short window", 200 * time.Millisecond, time.Second},
		{"tiny grace", 90 * time.Second, time.Second},
		{"long window", time.Hour, 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBudget(tt.collect, tt.grace)

			if b.Sweep < 0 {
				t.Errorf("Sweep = %v, want a non-negative offset", b.Sweep)
			}
			if b.Sweep > b.Collect {
				t.Errorf("Sweep %v is after Collect %v", b.Sweep, b.Collect)
			}
			if b.Collect != tt.collect {
				t.Errorf("Collect = %v, want %v", b.Collect, tt.collect)
			}
			if b.Collect >= b.Drain || b.Drain >= b.Finalize || b.Finalize >= b.HardStop {
				t.Errorf("stages out of order: collect %v, drain %v, finalize %v, hard stop %v",
					b.Collect, b.Drain, b.Finalize, b.HardStop)
			}
			if b.HardStop != tt.collect+tt.grace {
				t.Errorf("HardStop = %v, want %v", b.HardStop, tt.collect+tt.grace)
			}
		})
	}
}

// The sweep gives still-empty tokens a last chance before the window shuts, but
// on a very short window it must not land before the run has begun.
func TestBudgetSweepLeadIsCappedAndFloored(t *testing.T) {
	tests := []struct {
		collect time.Duration
		want    time.Duration
	}{
		{90 * time.Second, 75 * time.Second}, // capped at the maximum lead
		{time.Hour, 3585 * time.Second},      // still capped, not proportional
		{30 * time.Second, 25 * time.Second}, // proportional below the cap
		{6 * time.Second, 5 * time.Second},
		{time.Second, time.Second - time.Second/6},
	}

	for _, tt := range tests {
		t.Run(tt.collect.String(), func(t *testing.T) {
			if got := NewBudget(tt.collect, 30*time.Second).Sweep; got != tt.want {
				t.Errorf("NewBudget(%v, 30s).Sweep = %v, want %v", tt.collect, got, tt.want)
			}
		})
	}
}

// I8: in rest-only mode a short duration with many tokens would otherwise be
// unsatisfiable, so the collection window is floored by the time the requests
// themselves take.
func TestRESTOnlyFloor(t *testing.T) {
	tests := []struct {
		name      string
		tokens    int
		batchSize int
		rate      float64
		want      time.Duration
	}{
		{"single batch at ten per second", 400, 400, 10, 100 * time.Millisecond},
		{"two batches", 500, 400, 10, 200 * time.Millisecond},
		{"unbatched", 40, 1, 10, 4 * time.Second},
		{"no tokens", 0, 400, 10, 0},
		{"invalid rate is ignored", 400, 400, 0, 0},
		{"invalid batch size is ignored", 400, 0, 10, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RESTOnlyFloor(tt.tokens, tt.batchSize, tt.rate)
			if got != tt.want {
				t.Errorf("RESTOnlyFloor(%d, %d, %v) = %v, want %v",
					tt.tokens, tt.batchSize, tt.rate, got, tt.want)
			}
		})
	}
}

func TestCollectWindowUsesTheFloorOnlyInRESTOnlyMode(t *testing.T) {
	cfg := Config{Duration: 5 * time.Second, RESTOnly: false, RESTRate: 10, RESTBatchSize: 1}

	if got := cfg.CollectWindow(400); got != 5*time.Second {
		t.Errorf("CollectWindow in websocket mode = %v, want the duration unchanged", got)
	}

	cfg.RESTOnly = true
	if got := cfg.CollectWindow(400); got != 40*time.Second {
		t.Errorf("CollectWindow in rest-only mode = %v, want the 40s request floor", got)
	}

	if got := cfg.CollectWindow(1); got != 5*time.Second {
		t.Errorf("CollectWindow with few tokens = %v, want the duration unchanged", got)
	}
}
