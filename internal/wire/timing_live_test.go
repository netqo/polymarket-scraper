//go:build live

// Test data: real. This measures the feed's own delivery behaviour, which is
// the one thing no fixture can tell you.
//
// Two numbers matter and neither has ever been measured. How far out of order do
// timestamps actually arrive, which is what ReorderTolerance is set against; and
// how far behind the exchange's clock is the moment a message reaches us, which
// is what a consumer reads when it compares exchange_timestamp against
// received_at.

package wire

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// timingWindow is long enough for a busy market to show its worst case rather
// than its median.
const timingWindow = 90 * time.Second

// dialLive opens a subscribed connection, for the tests that need raw frames.
func dialLive(t *testing.T, ids []string, budget time.Duration) *websocket.Conn {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), budget)
	t.Cleanup(cancel)

	conn, _, err := websocket.Dial(ctx, liveWSURL, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		t.Fatalf("dialing %s: %v", liveWSURL, err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	conn.SetReadLimit(32 << 20)

	subscription, err := json.Marshal(NewSubscription(ids))
	if err != nil {
		t.Fatalf("encoding the subscription: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, subscription); err != nil {
		t.Fatalf("sending the subscription: %v", err)
	}

	return conn
}

// percentile returns the value at a fraction through a sorted sample.
func percentile(sorted []int64, fraction float64) int64 {
	if len(sorted) == 0 {
		return 0
	}

	index := int(float64(len(sorted)-1) * fraction)

	return sorted[index]
}

// Item 36: is ReorderTolerance calibrated against anything real?
//
// The default is 5 seconds. If real regressions never approach that, a genuine
// gap is being applied as though it were a clock artifact, which is the failure
// the whole trust machinery exists to prevent. If they routinely exceed it, the
// setting causes a resync storm instead.
func TestLiveDeliveryTiming(t *testing.T) {
	ids := liveTokens(t)
	if len(ids) > 50 {
		ids = ids[:50]
	}

	conn := dialLive(t, ids, timingWindow+30*time.Second)
	ctx := t.Context()

	// Per token, the last timestamp seen, so a regression can be measured the
	// way the tracker measures it: against delivery order on one connection.
	last := make(map[string]int64)

	var lags, regressions []int64
	messages := 0
	deadline := time.Now().Add(timingWindow)

	for time.Now().Before(deadline) {
		readCtx, stop := context.WithDeadline(ctx, deadline)
		_, payload, err := conn.Read(readCtx)
		stop()

		if err != nil {
			break
		}
		arrived := time.Now()

		if IsKeepalive(payload) {
			continue
		}

		events, _ := DecodeFrame(payload)
		for _, event := range events {
			change, ok := event.(PriceChange)
			if !ok {
				continue
			}

			millis, err := strconv.ParseInt(change.Timestamp, 10, 64)
			if err != nil {
				continue
			}
			messages++

			// How far behind the exchange's clock we are when it lands.
			lags = append(lags, arrived.UnixMilli()-millis)

			for _, entry := range change.Changes {
				if previous, seen := last[entry.AssetID]; seen && millis < previous {
					regressions = append(regressions, previous-millis)
				}
				last[entry.AssetID] = millis
			}
		}
	}

	if messages == 0 {
		t.Skip("no price changes arrived, so nothing can be measured")
	}

	slices.Sort(lags)
	t.Logf("%d price changes over %v", messages, timingWindow)
	t.Logf("exchange-to-arrival lag: p50 %dms, p95 %dms, p99 %dms, max %dms",
		percentile(lags, 0.50), percentile(lags, 0.95), percentile(lags, 0.99), lags[len(lags)-1])

	if len(regressions) == 0 {
		t.Logf("timestamp regressions: none in %d messages across %d tokens", messages, len(last))
		t.Logf("ReorderTolerance is %v; nothing observed came close to it", 5*time.Second)

		return
	}

	slices.Sort(regressions)
	worst := regressions[len(regressions)-1]
	t.Logf("timestamp regressions: %d of them, p50 %dms, p95 %dms, max %dms",
		len(regressions), percentile(regressions, 0.50), percentile(regressions, 0.95), worst)

	// A tolerance far above anything the feed produces means a real gap is
	// forgiven as a clock artifact. One far below means ordinary jitter is
	// treated as a gap. Both are reported rather than failed, because one
	// window is not a distribution.
	const tolerance = 5 * time.Second
	switch {
	case worst > tolerance.Milliseconds():
		t.Logf("a regression exceeded the %v tolerance, so real gaps and clock jitter overlap", tolerance)
	case worst*20 < tolerance.Milliseconds():
		t.Logf("the worst regression was %dms against a %v tolerance, more than an order of magnitude of slack",
			worst, tolerance)
	}
}

// keepaliveWindow is how long to wait for the server to object to a client that
// never speaks. Longer than any plausible server timeout, so that "it never
// closed" is a finding rather than an artefact of giving up early.
const keepaliveWindow = 3 * time.Minute

// Item 43: what does the server actually require of a client's keepalives?
//
// The scraper sends PING every 10s and treats 30s of silence as death. Neither
// number has ever been checked against the server. Two things are worth knowing
// and only one experiment answers both: whether the server closes a connection
// that never pings, and whether it sends anything of its own to keep alive.
//
// This one deliberately sends nothing after subscribing.
func TestLiveServerToleranceOfASilentClient(t *testing.T) {
	ids := liveTokens(t)
	if len(ids) > 20 {
		ids = ids[:20]
	}

	conn := dialLive(t, ids, keepaliveWindow+30*time.Second)
	ctx := t.Context()

	started := time.Now()
	deadline := started.Add(keepaliveWindow)

	frames, serverKeepalives := 0, 0
	var lastFrame time.Time
	var closeErr error

	for time.Now().Before(deadline) {
		readCtx, stop := context.WithDeadline(ctx, deadline)
		_, payload, err := conn.Read(readCtx)
		stop()

		if err != nil {
			// A deadline of our own is the window ending, not the server acting.
			if ctx.Err() == nil && time.Now().Before(deadline.Add(-time.Second)) {
				closeErr = err
			}

			break
		}

		frames++
		lastFrame = time.Now()
		if IsKeepalive(payload) {
			serverKeepalives++
		}
	}

	survived := time.Since(started)

	t.Logf("sent nothing for %v after subscribing", survived.Round(time.Second))
	t.Logf("read %d frames, %d of them keepalives from the server", frames, serverKeepalives)

	switch {
	case closeErr != nil:
		t.Logf("the server closed the connection after %v: %v", survived.Round(time.Second), closeErr)
		t.Logf("so client keepalives are load-bearing, and the %v ping interval must stay below this",
			10*time.Second)

	case frames == 0:
		t.Errorf("the connection delivered nothing at all in %v, so this measures nothing", keepaliveWindow)

	default:
		t.Logf("the server kept the connection open and delivered data for the whole %v without being pinged",
			keepaliveWindow)
		if serverKeepalives == 0 {
			t.Logf("it also never sent a keepalive of its own, so silence from it is not evidence of health")
		}
		if !lastFrame.IsZero() {
			t.Logf("last frame arrived %v before the window closed",
				deadline.Sub(lastFrame).Round(time.Second))
		}
	}
}

// Item 43, the half that matters more.
//
// The server sends no keepalives of its own and does not require the client's,
// so what is PING actually for? The answer decides whether the idle timeout is
// safe: the client treats silence as death, and on a market with no activity the
// only thing that can break that silence is a reply to something we sent.
//
// If the server answers PING with PONG, a quiet token is fine. If it does not,
// then a genuinely quiet market looks exactly like a dead connection, and every
// token on it is distrusted and re-seeded for no reason at all.
func TestLiveServerAnswersPing(t *testing.T) {
	ids := liveTokens(t)
	if len(ids) > 5 {
		ids = ids[:5]
	}

	const (
		window   = 70 * time.Second
		interval = 10 * time.Second
	)

	conn := dialLive(t, ids, window+30*time.Second)
	ctx := t.Context()

	pings, pongs, other := 0, 0, 0
	deadline := time.Now().Add(window)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	go func() {
		for {
			select {
			case <-ticker.C:
				if err := conn.Write(ctx, websocket.MessageText, []byte(Ping)); err != nil {
					return
				}
				pings++
			case <-ctx.Done():
				return
			}
		}
	}()

	var longestSilence time.Duration
	lastHeard := time.Now()

	for time.Now().Before(deadline) {
		readCtx, stop := context.WithDeadline(ctx, deadline)
		_, payload, err := conn.Read(readCtx)
		stop()

		if err != nil {
			break
		}

		if silence := time.Since(lastHeard); silence > longestSilence {
			longestSilence = silence
		}
		lastHeard = time.Now()

		switch {
		case IsKeepalive(payload):
			pongs++
		default:
			other++
		}
	}

	t.Logf("sent %d pings over %v, heard %d keepalives back and %d other frames",
		pings, window, pongs, other)
	t.Logf("longest silence between frames: %v", longestSilence.Round(time.Millisecond))

	switch {
	case pongs > 0:
		t.Logf("the server answers PING, so a keepalive is what keeps the idle timeout fed " +
			"on a market with no activity of its own")

	case other > 0:
		t.Errorf("the server never answered any of %d pings. On a quiet market nothing would "+
			"break the silence, so the %v idle timeout would fire on a healthy connection and "+
			"distrust every token on it", pings, 30*time.Second)

	default:
		t.Skip("no frames at all arrived, so nothing can be concluded")
	}
}
