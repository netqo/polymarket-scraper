// Test data: Frames hand-written, modelled on the payloads captured in frame_test.go. The
// behaviours under test are ones the real server will not perform on request: an
// abrupt drop, going silent while the socket stays open, and accepting a
// subscription without ever sending its snapshot.

package wsclient

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/netqo/polymarket-scraper/internal/testsupport"
	"github.com/netqo/polymarket-scraper/internal/wire"
)

// Timings are compressed so the suite runs in about a second. The behaviour
// under test is a sequence, not a duration.
const (
	testPingInterval = 20 * time.Millisecond
	testIdleTimeout  = 120 * time.Millisecond
)

const snapshotFrame = `{"market":"0xmarket","asset_id":"111","timestamp":"1000",` +
	`"hash":"h","bids":[{"price":"0.97","size":"100"}],"asks":[{"price":"0.99","size":"50"}],` +
	`"tick_size":"0.001","event_type":"book"}`

const priceChangeFrame = `{"market":"0xmarket","price_changes":[` +
	`{"asset_id":"111","price":"0.98","size":"25","side":"BUY","hash":"h2",` +
	`"best_bid":"0.98","best_ask":"0.99"}],"timestamp":"1001","event_type":"price_change"}`

// newShard builds a shard pointed at a fake, with compressed timings.
func newShard(t *testing.T, fake *testsupport.FakeWS, assetIDs ...string) *Shard {
	t.Helper()

	return New(Options{
		ID:           1,
		URL:          fake.URL(),
		AssetIDs:     assetIDs,
		PingInterval: testPingInterval,
		IdleTimeout:  testIdleTimeout,
	})
}

// runShard runs a shard until it stops, returning the frames it delivered and
// the reason it ended.
func runShard(t *testing.T, shard *Shard, timeout time.Duration) ([]Frame, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	frames := make(chan Frame, 64)
	done := make(chan error, 1)

	go func() { done <- shard.Run(ctx, frames) }()

	var collected []Frame
	for {
		select {
		case frame := <-frames:
			collected = append(collected, frame)
		case err := <-done:
			// Drain anything already queued before reporting.
			for {
				select {
				case frame := <-frames:
					collected = append(collected, frame)
				default:
					return collected, err
				}
			}
		}
	}
}

// B1: the subscription is checked byte for byte, because the custom feature
// flag is what unlocks three of the events the output document reports, and
// losing it fails silently: the connection works, those events just never come.
func TestSubscriptionIsSentExactly(t *testing.T) {
	fake := testsupport.NewFakeWS(t,
		testsupport.WSStep{Send: snapshotFrame},
		testsupport.WSStep{After: 30 * time.Millisecond, Action: testsupport.WSDropConnection},
	)

	if _, err := runShard(t, newShard(t, fake, "111", "222"), time.Second); err == nil {
		t.Fatal("Run returned no error after the connection was dropped")
	}

	subscriptions := fake.ReceivedMatching("assets_ids")
	if len(subscriptions) != 1 {
		t.Fatalf("the server received %d subscriptions, want 1: %v", len(subscriptions), fake.Received())
	}

	const want = `{"assets_ids":["111","222"],"type":"market","custom_feature_enabled":true}`
	if subscriptions[0] != want {
		t.Errorf("subscription =\n %s\nwant\n %s", subscriptions[0], want)
	}
}

func TestFramesAreDecodedAndTimestamped(t *testing.T) {
	fake := testsupport.NewFakeWS(t,
		testsupport.WSStep{Send: snapshotFrame},
		testsupport.WSStep{After: 10 * time.Millisecond, Send: priceChangeFrame},
		testsupport.WSStep{After: 10 * time.Millisecond, Action: testsupport.WSDropConnection},
	)

	frames, _ := runShard(t, newShard(t, fake, "111"), time.Second)

	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2", len(frames))
	}
	if _, isBook := frames[0].Events[0].(wire.Book); !isBook {
		t.Errorf("first event has type %T, want a book snapshot", frames[0].Events[0])
	}
	if _, isChange := frames[1].Events[0].(wire.PriceChange); !isChange {
		t.Errorf("second event has type %T, want a price change", frames[1].Events[0])
	}
	for i, frame := range frames {
		if frame.ReceivedAt.IsZero() {
			t.Errorf("frame %d has no arrival time", i)
		}
		if frame.ShardID != 1 {
			t.Errorf("frame %d has shard id %d, want 1", i, frame.ShardID)
		}
	}
}

// The initial snapshot arrives as an array holding every subscribed asset in
// one frame, which is the framing a decoder is most likely to get wrong.
func TestTheArrayFramedSnapshotIsHandled(t *testing.T) {
	fake := testsupport.NewFakeWS(t,
		testsupport.WSStep{Send: "[" + snapshotFrame + "," + snapshotFrame + "]"},
		testsupport.WSStep{After: 20 * time.Millisecond, Action: testsupport.WSDropConnection},
	)

	shard := newShard(t, fake, "111", "222")
	frames, _ := runShard(t, shard, time.Second)

	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(frames))
	}
	if len(frames[0].Events) != 2 {
		t.Errorf("the frame carried %d events, want 2", len(frames[0].Events))
	}
	if shard.SnapshotsSeen() != 2 {
		t.Errorf("SnapshotsSeen() = %d, want 2", shard.SnapshotsSeen())
	}
}

// This is the counter that catches the silent subscribe ceiling: past roughly
// 750 assets the server keeps delivering updates and never sends the snapshot,
// so a client that does not count looks healthy while holding no book state.
func TestSnapshotCountRevealsAMissingSnapshot(t *testing.T) {
	fake := testsupport.NewFakeWS(t,
		testsupport.WSStep{Action: testsupport.WSIgnoreSubscription},
		testsupport.WSStep{After: 10 * time.Millisecond, Send: priceChangeFrame},
		testsupport.WSStep{After: 20 * time.Millisecond, Action: testsupport.WSDropConnection},
	)

	shard := newShard(t, fake, "111", "222")
	frames, _ := runShard(t, shard, time.Second)

	if len(frames) == 0 {
		t.Fatal("the shard received nothing at all, so the test proves nothing")
	}
	if shard.SnapshotsSeen() != 0 {
		t.Errorf("SnapshotsSeen() = %d, want 0", shard.SnapshotsSeen())
	}
	// The shard was subscribed to two assets, so a caller comparing the two
	// numbers is what notices that neither snapshot ever arrived.
	if shard.SnapshotsSeen() >= 2 {
		t.Error("a shard with no snapshots looks fully seeded")
	}
}

// B2: the keepalive is an application-level text frame containing the literal
// word PING. Calling a library's Ping method talks to the wrong layer, and the
// connection dies half a minute later for no visible reason.
func TestKeepaliveIsALiteralTextFrame(t *testing.T) {
	fake := testsupport.NewFakeWS(t,
		testsupport.WSStep{Send: snapshotFrame},
		testsupport.WSStep{After: 120 * time.Millisecond, Action: testsupport.WSDropConnection},
	)

	_, _ = runShard(t, newShard(t, fake, "111"), time.Second)

	pings := fake.ReceivedMatching(wire.Ping)
	if len(pings) < 2 {
		t.Fatalf("the server received %d keepalives over ~120ms at a 20ms interval, want at least 2: %v",
			len(pings), fake.Received())
	}
	for _, ping := range pings {
		if ping != wire.Ping {
			t.Errorf("keepalive = %q, want the literal %q", ping, wire.Ping)
		}
	}
}

// The reply is raw text and would fail JSON parsing, so it has to be recognised
// before anything tries. If it were not, every keepalive would surface as a
// decode error and take its tokens out of trust.
func TestKeepaliveRepliesAreNotTreatedAsMessages(t *testing.T) {
	fake := testsupport.NewFakeWS(t,
		testsupport.WSStep{Send: snapshotFrame},
		testsupport.WSStep{After: 100 * time.Millisecond, Action: testsupport.WSDropConnection},
	)

	shard := newShard(t, fake, "111")
	frames, _ := runShard(t, shard, time.Second)

	for i, frame := range frames {
		if frame.Err != nil {
			t.Errorf("frame %d reported a decode error: %v", i, frame.Err)
		}
	}
	if len(frames) != 1 {
		t.Errorf("got %d frames, want only the snapshot: keepalive replies leaked through", len(frames))
	}
	// The replies did arrive, so the test is exercising what it claims to.
	if shard.FramesSeen() <= len(frames) {
		t.Errorf("FramesSeen() = %d with %d delivered: no keepalive replies were read",
			shard.FramesSeen(), len(frames))
	}
}

// A connection that stops talking while staying open is the failure a keepalive
// exists to detect, and it is invisible to anything that only watches for a
// closed socket.
func TestSilenceIsReportedAsIdleRatherThanHanging(t *testing.T) {
	fake := testsupport.NewFakeWS(t,
		testsupport.WSStep{Send: snapshotFrame},
		testsupport.WSStep{After: 10 * time.Millisecond, Action: testsupport.WSGoSilent},
	)

	started := time.Now()
	_, err := runShard(t, newShard(t, fake, "111"), 2*time.Second)

	if !errors.Is(err, ErrIdle) {
		t.Fatalf("Run returned %v, want ErrIdle", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("the shard took %v to notice silence, want about the %v idle timeout",
			elapsed, testIdleTimeout)
	}
}

// Teardown must not wait for the keepalive ticker. The rest of this file runs
// with a ping interval far shorter than the idle timeout, which hides a join
// that happens before the writer is cancelled; production runs the other way
// round, at a 10s keepalive against a 30s idle timeout, where the same mistake
// costs a full ping interval on every reconnect and delays the notice that the
// tokens on this connection are no longer trustworthy.
func TestTeardownDoesNotWaitForTheKeepaliveTicker(t *testing.T) {
	fake := testsupport.NewFakeWS(t,
		testsupport.WSStep{Send: snapshotFrame},
		testsupport.WSStep{After: 10 * time.Millisecond, Action: testsupport.WSGoSilent},
	)

	const longPingInterval = 3 * time.Second

	shard := New(Options{
		ID:           1,
		URL:          fake.URL(),
		AssetIDs:     []string{"111"},
		PingInterval: longPingInterval,
		IdleTimeout:  100 * time.Millisecond,
	})

	started := time.Now()
	_, err := runShard(t, shard, 10*time.Second)

	if !errors.Is(err, ErrIdle) {
		t.Fatalf("Run returned %v, want ErrIdle", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("Run took %v to return after a 100ms idle timeout, want no wait on the %v keepalive",
			elapsed, longPingInterval)
	}
}

// An abrupt drop must be reported, not absorbed. The caller is what decides
// that the tokens on this connection just stopped being trustworthy, and it
// cannot decide that if the shard quietly reconnects on its own.
func TestAnAbruptDropEndsTheRun(t *testing.T) {
	fake := testsupport.NewFakeWS(t,
		testsupport.WSStep{Send: snapshotFrame},
		testsupport.WSStep{After: 10 * time.Millisecond, Action: testsupport.WSDropConnection},
	)

	_, err := runShard(t, newShard(t, fake, "111"), time.Second)

	if err == nil {
		t.Fatal("Run returned no error after the connection was dropped")
	}
	if errors.Is(err, ErrIdle) {
		t.Errorf("a dropped connection was reported as idle: %v", err)
	}
	if fake.Connections() != 1 {
		t.Errorf("the server saw %d connections, want 1: the shard reconnected on its own", fake.Connections())
	}
}

func TestCancellationEndsTheRun(t *testing.T) {
	fake := testsupport.NewFakeWS(t, testsupport.WSStep{Send: snapshotFrame})

	ctx, cancel := context.WithCancel(context.Background())
	frames := make(chan Frame, 8)
	done := make(chan error, 1)

	shard := newShard(t, fake, "111")
	go func() { done <- shard.Run(ctx, frames) }()

	<-frames
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("Run returned no error after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return within a second of cancellation")
	}
}

func TestDialFailureIsReported(t *testing.T) {
	shard := New(Options{
		ID:           1,
		URL:          "ws://127.0.0.1:1/nothing-here",
		AssetIDs:     []string{"111"},
		PingInterval: testPingInterval,
		IdleTimeout:  testIdleTimeout,
	})

	_, err := runShard(t, shard, 2*time.Second)
	if err == nil {
		t.Fatal("Run succeeded against a closed port")
	}
	if !strings.Contains(err.Error(), "dialing") {
		t.Errorf("error %q does not say that dialing failed", err)
	}
}

// A frame that does not decode must not take the whole connection down: the
// caller distrusts the affected tokens and the connection keeps working.
func TestAnUndecodableFrameIsReportedWithoutEndingTheRun(t *testing.T) {
	fake := testsupport.NewFakeWS(t,
		testsupport.WSStep{Send: `{"no_discriminator":true}`},
		testsupport.WSStep{After: 10 * time.Millisecond, Send: snapshotFrame},
		testsupport.WSStep{After: 10 * time.Millisecond, Action: testsupport.WSDropConnection},
	)

	frames, _ := runShard(t, newShard(t, fake, "111"), time.Second)

	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2: the bad frame ended the run", len(frames))
	}
	if frames[0].Err == nil {
		t.Error("the undecodable frame was reported as fine")
	}
	if frames[1].Err != nil {
		t.Errorf("the frame after the bad one also failed: %v", frames[1].Err)
	}
}

// Dynamic subscription is how tokens announced mid-window get picked up. The
// update message is a different shape from the opening subscription.
func TestSubscribeSendsAnUpdateOnALiveConnection(t *testing.T) {
	fake := testsupport.NewFakeWS(t,
		testsupport.WSStep{Send: snapshotFrame},
		testsupport.WSStep{After: 150 * time.Millisecond, Action: testsupport.WSDropConnection},
	)

	shard := newShard(t, fake, "111")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	frames := make(chan Frame, 8)
	done := make(chan error, 1)
	go func() { done <- shard.Run(ctx, frames) }()

	<-frames
	if !shard.Subscribe([]string{"333"}) {
		t.Fatal("Subscribe reported that it could not queue the update")
	}
	<-done

	updates := fake.ReceivedMatching(`"operation"`)
	if len(updates) != 1 {
		t.Fatalf("the server received %d subscription updates, want 1: %v", len(updates), fake.Received())
	}

	const want = `{"operation":"subscribe","assets_ids":["333"]}`
	if updates[0] != want {
		t.Errorf("update =\n %s\nwant\n %s", updates[0], want)
	}
}

func TestSubscribeWithNothingToAddDoesNothing(t *testing.T) {
	fake := testsupport.NewFakeWS(t)
	shard := newShard(t, fake, "111")

	if !shard.Subscribe(nil) {
		t.Error("Subscribe reported a failure for an empty list")
	}
	if len(fake.ReceivedMatching(`"operation"`)) != 0 {
		t.Error("an empty subscription change was sent anyway")
	}
}

// Blocking here would spread a failing connection's problem to whatever is
// deciding to subscribe, so a full queue is reported rather than waited on.
func TestSubscribeDoesNotBlockOnAFullQueue(t *testing.T) {
	fake := testsupport.NewFakeWS(t)
	shard := newShard(t, fake, "111")

	// Nothing is draining the queue, because Run was never called.
	for range outboundBuffer {
		shard.Subscribe([]string{"333"})
	}

	done := make(chan bool, 1)
	go func() { done <- shard.Subscribe([]string{"444"}) }()

	select {
	case queued := <-done:
		if queued {
			t.Error("Subscribe claimed to queue an update onto a full queue")
		}
	case <-time.After(time.Second):
		t.Fatal("Subscribe blocked on a full queue")
	}
}
