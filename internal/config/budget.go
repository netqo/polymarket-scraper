package config

import (
	"math"
	"time"
)

// Timeline shape, expressed as fractions of the grace period.
//
// The grace period is split rather than consumed by one stage, because the
// three things that happen after the window closes have very different failure
// modes: draining in-flight REST work can be abandoned cheaply, waiting for
// shard results should not be abandoned unless something is genuinely wedged,
// and writing the file must never be the step that runs out of time.
const (
	drainShare    = 4 // grace/4 to finish in-flight REST work
	finalizeShare = 2 // grace/2 to collect shard results, leaving half to write

	// maxSweepLead caps how early the last-chance sweep runs. On a long window
	// there is no value in sweeping earlier than this; on a short one the lead
	// is proportional so the sweep cannot land before the run has begun.
	maxSweepLead     = 15 * time.Second
	sweepLeadDivisor = 6
)

// Budget is the deterministic timeline of a run: a set of offsets from the
// start, each naming one stage of collection and shutdown.
//
// It exists as a value computed by a pure function, rather than as timers
// scattered through the engine, so that requirement A4 can be verified by a
// table test instead of by watching a real process fail to exit.
type Budget struct {
	// Sweep is when every token that still has no book gets a last-chance REST
	// fetch, and dynamic subscription stops accepting new tokens.
	Sweep time.Duration

	// Collect is when the collection window ends and the websocket connections
	// are closed.
	Collect time.Duration

	// Drain is when in-flight REST work is abandoned. Anything still being
	// resynced at this point is reported as a resync failure, never as a book.
	Drain time.Duration

	// Finalize is when the engine stops waiting for shard results. A shard that
	// has not reported by now has all of its tokens reported as failures.
	Finalize time.Duration

	// HardStop is when the watchdog terminates the process unconditionally.
	// Go goroutines are not killable, so this is the only hard guarantee that
	// A4 is met; every other stage is cooperative.
	HardStop time.Duration
}

// NewBudget derives the timeline from the length of the collection window and
// the grace period allowed for shutdown.
func NewBudget(collect, grace time.Duration) Budget {
	return Budget{
		Sweep:    collect - sweepLead(collect),
		Collect:  collect,
		Drain:    collect + grace/drainShare,
		Finalize: collect + grace/finalizeShare,
		HardStop: collect + grace,
	}
}

// sweepLead returns how long before the end of the window the last-chance
// sweep runs.
func sweepLead(collect time.Duration) time.Duration {
	lead := collect / sweepLeadDivisor
	if lead > maxSweepLead {
		lead = maxSweepLead
	}

	return lead
}

// RESTOnlyFloor estimates how long it takes to fetch every token's book over
// REST at the configured pace, which is the shortest collection window that can
// possibly succeed.
//
// Returns zero when the inputs make no sense, so a misconfigured rate cannot
// silently extend a run: validation rejects those values before this is called.
func RESTOnlyFloor(tokens, batchSize int, rate float64) time.Duration {
	if tokens <= 0 || batchSize <= 0 || rate <= 0 {
		return 0
	}

	requests := math.Ceil(float64(tokens) / float64(batchSize))
	seconds := requests / rate

	return time.Duration(seconds * float64(time.Second))
}

// CollectWindow returns the effective length of the collection window for a run
// over the given number of tokens.
//
// In websocket mode this is simply the configured duration. In rest-only mode
// it is floored by the time the requests themselves take (interpretation I8):
// otherwise a short duration with many tokens would be unsatisfiable by
// construction, and the run would report failures that are purely arithmetic.
func (c Config) CollectWindow(tokens int) time.Duration {
	if !c.RESTOnly {
		return c.Duration
	}

	if floor := RESTOnlyFloor(tokens, c.RESTBatchSize, c.RESTRate); floor > c.Duration {
		return floor
	}

	return c.Duration
}

// Budget returns the timeline for a run over the given number of tokens.
func (c Config) Budget(tokens int) Budget {
	return NewBudget(c.CollectWindow(tokens), c.Grace)
}
