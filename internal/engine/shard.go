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
	id       int
	assetIDs []string

	frames  chan wsclient.Frame
	control chan control

	// trackers is read and written only by the apply goroutine.
	trackers map[string]*tracker.Tracker

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

	return &shardState{
		id:       id,
		assetIDs: assetIDs,
		frames:   make(chan wsclient.Frame, frameBuffer),
		control:  make(chan control, controlBuffer),
		trackers: trackers,
	}
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
