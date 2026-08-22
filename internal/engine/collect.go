package engine

import (
	"context"
	"errors"
	"time"

	"github.com/netqo/polymarket-scraper/internal/logging"
	"github.com/netqo/polymarket-scraper/internal/report"
	"github.com/netqo/polymarket-scraper/internal/tracker"
	"github.com/netqo/polymarket-scraper/internal/wsclient"
)

// runWebsocket collects a window over the websocket.
//
// The whole shape of this function follows from one fact: a goroutine blocked
// in a syscall will never observe a cancelled context, so the run must be able
// to produce its document without joining anything that performs I/O. Nothing
// here waits on a reader, a writer or a REST worker. It waits only on the apply
// goroutines, which perform no I/O and therefore cannot wedge, and even that
// wait has a deadline, with a watchdog behind it as the only hard guarantee.
func (e *Engine) runWebsocket(ctx context.Context) (report.Document, error) {
	startedAt := e.now()
	budget := e.cfg.Budget(len(e.tokens.IDs))

	stopWatchdog := e.startWatchdog(budget)
	defer stopWatchdog()

	// Collection ends when the window closes. Draining runs on past it, so
	// re-seeds still in flight have somewhere to land.
	collectCtx, stopCollect := context.WithDeadline(ctx, startedAt.Add(budget.Collect))
	defer stopCollect()
	drainCtx, stopDrain := context.WithDeadline(ctx, startedAt.Add(budget.Drain))
	defer stopDrain()

	e.noteTokenListAnomalies()
	e.buildShards()

	// Sized for every connection the run could end up with, discovery included.
	// An apply goroutine blocking on a full results channel is the one failure
	// the whole shutdown design exists to rule out.
	results := make(chan shardResult, e.maxShards())

	// Set before any shard goroutine exists, and only read after, so discovery
	// can start a connection of its own without a race.
	e.running = &collection{collectCtx: collectCtx, drainCtx: drainCtx, results: results}

	for range e.cfg.ResyncWorkers {
		go e.resyncWorker(drainCtx)
	}
	go e.seedMetadata(drainCtx)

	for _, shard := range e.shards.all() {
		e.startShard(shard)
	}

	sweep := time.AfterFunc(budget.Sweep, func() { e.broadcastSweep(collectCtx) })
	defer sweep.Stop()

	e.logger.Info("collecting", logging.Cat(logging.CategoryProgress),
		"tokens", len(e.tokens.IDs),
		"shards", e.shards.count(),
		"window", budget.Collect)

	snapshots := e.gatherResults(startedAt.Add(budget.Finalize), results)

	return e.finalizeDocument(startedAt, e.now(), snapshots), nil
}

// startShard begins a connection and the goroutine that owns its trackers.
func (e *Engine) startShard(s *shardState) {
	go e.runShard(e.running.collectCtx, s)
	go e.applyLoop(e.running.collectCtx, e.running.drainCtx, s, e.running.results)
}

// broadcastSweep gives every token that still has no book one last chance.
func (e *Engine) broadcastSweep(ctx context.Context) {
	e.discoveryClosed.Store(true)

	for _, shard := range e.shards.all() {
		shard.send(ctx, control{kind: ctrlSweep, at: e.now()})
	}
}

// gatherResults collects each shard's final answer, giving up at the deadline.
//
// A shard that has not reported by then has all of its tokens recorded as
// failures. That branch should be unreachable, because an apply goroutine
// performs no I/O and cannot wedge, but the run must not depend on that being
// true: reporting a stale book because a goroutine was late is exactly the
// outcome the design exists to rule out.
func (e *Engine) gatherResults(deadline time.Time, results <-chan shardResult) map[string]tracker.Snapshot {
	snapshots := make(map[string]tracker.Snapshot, len(e.tokens.IDs))
	reported := make(map[int]bool, e.shards.count())

	timeout := time.NewTimer(time.Until(deadline))
	defer timeout.Stop()

	// The count is re-read each time round, because discovery can add a
	// connection while this is waiting. Discovery stops at the sweep and a
	// shard only reports once the window has closed, so by the time anything
	// arrives here the set has stopped changing.
	for len(reported) < e.shards.count() {
		select {
		case result := <-results:
			reported[result.shardID] = true
			for id, snapshot := range result.snapshots {
				snapshots[id] = snapshot
			}

		case <-timeout.C:
			e.recordUnreportedShards(reported, snapshots)
			return snapshots
		}
	}

	return snapshots
}

// recordUnreportedShards reports every token of a silent shard as a failure.
func (e *Engine) recordUnreportedShards(reported map[int]bool, snapshots map[string]tracker.Snapshot) {
	for _, shard := range e.shards.all() {
		if reported[shard.id] {
			continue
		}

		e.errors.Addf("shard %d did not report in time; its %d tokens are reported as failures",
			shard.id, len(shard.assetIDs))

		for _, assetID := range shard.assetIDs {
			snapshots[assetID] = tracker.Unreported(assetID)
		}
	}
}

// runShard keeps one connection alive for the collection window.
//
// Reconnection lives here rather than in the transport because a connection
// that quietly reconnects cannot tell anyone that the tokens it carries just
// stopped being trustworthy. Every reconnection sends that notice first, and
// only then dials again.
func (e *Engine) runShard(collectCtx context.Context, s *shardState) {
	backoff := e.cfg.ReconnectInitialBackoff
	everConnected := false

	for collectCtx.Err() == nil {
		// The subscription rather than the assigned tokens: anything discovery
		// took on has to be asked for again, or the reconnect would quietly
		// stop delivering it while its book was still reported as current.
		subscribed := s.subscribed()

		conn := wsclient.New(wsclient.Options{
			ID:           s.id,
			URL:          e.cfg.WSURL,
			AssetIDs:     subscribed,
			PingInterval: e.cfg.PingInterval,
			IdleTimeout:  e.cfg.IdleTimeout,
			ReadLimit:    e.cfg.ReadLimit,
			Logger:       e.logger,
			HTTPClient:   e.httpClient,
		})
		s.conn.Store(conn)

		err := conn.Run(collectCtx, s.frames)
		if collectCtx.Err() != nil {
			return
		}

		if !errors.Is(err, wsclient.ErrDial) {
			everConnected = true
			e.checkSubscriptionWasHonoured(s, conn, len(subscribed))
		}

		e.reconnects.Add(1)
		e.errors.Addf("shard %d: connection ended (%v), reconnecting", s.id, err)
		e.logger.Warn("connection ended", logging.Cat(logging.CategoryConnection),
			"shard", s.id, "error", err)

		// The notice goes out before the redial, so no token stays trusted
		// across a gap even briefly.
		s.send(collectCtx, control{kind: ctrlDisconnected, at: e.now()})

		if !sleepCtx(collectCtx, backoff) {
			return
		}
		backoff = min(backoff*2, e.cfg.ReconnectMaxBackoff)
	}

	if !everConnected {
		s.send(context.WithoutCancel(collectCtx), control{kind: ctrlSubscribeFailed, at: e.now()})
	}
}

// checkSubscriptionWasHonoured reports a connection that never sent the
// snapshots it was asked for.
//
// Past roughly 750 assets the server accepts a subscription, keeps delivering
// updates, and silently never sends the initial snapshot. The connection looks
// healthy from every angle except this count.
//
// The comparison is against everything the connection was subscribed to, not
// just the assigned tokens: snapshots for tokens discovery added would
// otherwise make up the shortfall and hide the very failure this catches.
func (e *Engine) checkSubscriptionWasHonoured(s *shardState, conn *wsclient.Shard, subscribed int) {
	if conn.SnapshotsSeen() >= subscribed || conn.FramesSeen() == 0 {
		return
	}

	e.errors.Addf("shard %d received %d snapshots for %d subscribed assets: the subscription was only partly honoured",
		s.id, conn.SnapshotsSeen(), subscribed)
}

// sleepCtx waits, reporting false if the run ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
