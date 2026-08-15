package engine

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/netqo/polymarket-scraper/internal/tracker"
	"github.com/netqo/polymarket-scraper/internal/wire"
	"github.com/netqo/polymarket-scraper/internal/wsclient"
)

// Channel capacities.
//
// The inbox is generous because the initial snapshot for a full shard arrives
// as one very large frame followed by a burst of updates, and an apply loop
// that is briefly behind should not push back on the reader.
const (
	frameBuffer   = 1024
	controlBuffer = 4096
)

// controlKind is the sort of control message an apply loop receives.
type controlKind int

const (
	// ctrlDisconnected reports that the connection carrying these tokens ended.
	ctrlDisconnected controlKind = iota

	// ctrlSubscribeFailed reports that a subscription was never established.
	ctrlSubscribeFailed

	// ctrlRESTBook delivers a book fetched over REST.
	ctrlRESTBook

	// ctrlRESTFailed reports that a fetch did not succeed.
	ctrlRESTFailed

	// ctrlTokenNotFound reports that the exchange does not know a token.
	ctrlTokenNotFound

	// ctrlSweep asks for a last-chance fetch of anything still unseeded.
	ctrlSweep
)

// control is a message to an apply loop from anything that is not the
// websocket.
type control struct {
	kind    controlKind
	at      time.Time
	tokenID string
	book    wire.RESTBook
}

// shardState is one connection's worth of tokens.
//
// The trackers map is owned exclusively by this shard's apply goroutine and is
// touched by nothing else. That single-writer discipline is not a stylistic
// preference: the run has to be able to produce its document without joining
// any goroutine that performs I/O, because a goroutine blocked in a syscall
// will never observe a cancelled context. Putting the state behind a mutex
// would let an I/O goroutine hold the lock at exactly the moment the run needs
// to read it, which is the hang the deadline exists to prevent.
type shardState struct {
	id int

	// assetIDs are the requested tokens assigned to this shard. It is the
	// completeness baseline and never changes.
	assetIDs []string

	frames  chan wsclient.Frame
	control chan control

	// trackers is read and written only by the apply goroutine.
	trackers map[string]*tracker.Tracker

	// closedToDiscovery records that the shard has stopped accepting announced
	// tokens. It belongs to the apply goroutine alone.
	closedToDiscovery bool

	// subscription is assetIDs plus every token discovery has taken on, and it
	// is what a redial subscribes to. It is replaced rather than mutated,
	// because the apply goroutine extends it while the shard's own goroutine
	// reads it to reconnect.
	subscription atomic.Pointer[[]string]

	// outstanding counts this shard's REST work in flight, so its drain lasts
	// only as long as it has something of its own to wait for. Per shard rather
	// than per run: one shard's slow re-seed is no reason for another to sit
	// through its whole drain allowance.
	outstanding atomic.Int64

	// conn is the live connection, replaced on every reconnect, so a
	// subscription change can find the current one.
	conn atomic.Pointer[wsclient.Shard]
}

// newShardState builds a shard's state and its trackers.
func newShardState(id int, assetIDs []string, opts tracker.Options) *shardState {
	trackers := make(map[string]*tracker.Tracker, len(assetIDs))
	for _, assetID := range assetIDs {
		trackers[assetID] = tracker.New(assetID, opts)
	}

	shard := &shardState{
		id:       id,
		assetIDs: assetIDs,
		frames:   make(chan wsclient.Frame, frameBuffer),
		control:  make(chan control, controlBuffer),
		trackers: trackers,
	}
	shard.subscription.Store(&assetIDs)

	return shard
}

// subscribed reports the tokens a connection for this shard must subscribe to.
func (s *shardState) subscribed() []string {
	if current := s.subscription.Load(); current != nil {
		return *current
	}

	return s.assetIDs
}

// extendSubscription records tokens taken on mid-window, so a reconnect
// resubscribes them rather than silently narrowing the feed back to the
// original shortlist while their books carry on being reported as current.
//
// The slice is copied rather than appended to in place: the shard's own
// goroutine may be reading the previous one to redial.
func (s *shardState) extendSubscription(assetIDs []string) {
	current := s.subscribed()

	extended := make([]string, 0, len(current)+len(assetIDs))
	extended = append(extended, current...)
	extended = append(extended, assetIDs...)

	s.subscription.Store(&extended)
}

// send delivers a control message, giving up rather than blocking if the run is
// already ending.
func (s *shardState) send(ctx context.Context, msg control) {
	select {
	case s.control <- msg:
	case <-ctx.Done():
	}
}

// shardResult is a shard's final answer.
type shardResult struct {
	shardID   int
	snapshots map[string]tracker.Snapshot
}
