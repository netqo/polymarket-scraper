package testsupport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/netqo/polymarket-scraper/internal/wire"
)

// WSAction is something the fake server does instead of, or after, sending a
// frame.
type WSAction int

// The actions.
const (
	// WSSend just sends the step's frame.
	WSSend WSAction = iota

	// WSDropConnection closes the socket abruptly, without a close handshake.
	// This is the case the whole trust machinery exists for, and it is one the
	// real exchange will not perform on request.
	WSDropConnection

	// WSGoSilent stops responding entirely while leaving the socket open. This
	// is the failure a keepalive detects and a closed-connection check misses.
	WSGoSilent

	// WSIgnoreSubscription accepts the subscription and never sends a snapshot,
	// reproducing the server's behaviour past its silent subscribe ceiling.
	WSIgnoreSubscription
)

// WSStep is one entry in a fake server's script.
type WSStep struct {
	// After delays the step relative to the previous one.
	After time.Duration

	// Send is the raw frame text. Empty means send nothing.
	Send string

	// Action is what to do instead of, or as well as, sending.
	Action WSAction
}

// FakeWS is an in-process stand-in for the market channel.
//
// It plays a script rather than simulating an exchange, because the things
// worth testing are sequences: subscribe, snapshot, a few updates, a
// disconnect, a reconnect. Recording every frame the client sends is what makes
// the protocol assertions possible, and those matter more than usual here,
// since a wrong subscription or a wrong keepalive fails silently against the
// real server.
type FakeWS struct {
	server *httptest.Server

	mu          sync.Mutex
	script      []WSStep
	received    [][]byte
	connections int
	silent      bool
}

// NewFakeWS starts a fake market channel. It is shut down when the test ends.
func NewFakeWS(t interface{ Cleanup(func()) }, script ...WSStep) *FakeWS {
	fake := &FakeWS{script: script}

	fake.server = httptest.NewServer(http.HandlerFunc(fake.handle))
	t.Cleanup(fake.server.Close)

	return fake
}

// URL is the endpoint to point a shard at.
func (f *FakeWS) URL() string {
	return "ws" + strings.TrimPrefix(f.server.URL, "http")
}

// SetScript replaces the script used by the next connection, so a test can make
// a reconnect behave differently from the first attempt.
func (f *FakeWS) SetScript(script ...WSStep) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.script = script
	f.silent = false
}

// Connections reports how many times a client has connected, which is how a
// reconnect is observed.
func (f *FakeWS) Connections() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.connections
}

// Received returns every frame the client sent.
func (f *FakeWS) Received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	frames := make([]string, len(f.received))
	for i, frame := range f.received {
		frames[i] = string(frame)
	}

	return frames
}

// ReceivedMatching returns the frames the client sent that contain substring,
// for asserting on subscriptions and keepalives separately.
func (f *FakeWS) ReceivedMatching(substring string) []string {
	var matching []string
	for _, frame := range f.Received() {
		if strings.Contains(frame, substring) {
			matching = append(matching, frame)
		}
	}

	return matching
}

// Pings counts the keepalive frames the client has sent.
func (f *FakeWS) Pings() int { return len(f.ReceivedMatching(wire.Ping)) }

func (f *FakeWS) handle(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()

	conn.SetReadLimit(32 << 20)

	f.mu.Lock()
	f.connections++
	script := append([]WSStep(nil), f.script...)
	f.mu.Unlock()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Reading in the background both records what the client sends and answers
	// its keepalives, which the client needs in order not to declare the
	// connection dead.
	go f.readFromClient(ctx, conn)

	f.play(ctx, conn, script)

	// Hold the connection open once the script is done, so a test that expects
	// the client to keep running is not tripped by the server hanging up.
	<-ctx.Done()
}

// readFromClient records incoming frames and answers keepalives.
func (f *FakeWS) readFromClient(ctx context.Context, conn *websocket.Conn) {
	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			return
		}

		f.mu.Lock()
		f.received = append(f.received, append([]byte(nil), payload...))
		silent := f.silent
		f.mu.Unlock()

		if silent {
			continue
		}
		if string(payload) == wire.Ping {
			if err := conn.Write(ctx, websocket.MessageText, []byte(wire.Pong)); err != nil {
				return
			}
		}
	}
}

// play runs the script against a connection.
func (f *FakeWS) play(ctx context.Context, conn *websocket.Conn, script []WSStep) {
	for _, step := range script {
		if !sleep(ctx, step.After) {
			return
		}

		switch step.Action {
		case WSDropConnection:
			_ = conn.CloseNow()
			return

		case WSGoSilent:
			f.mu.Lock()
			f.silent = true
			f.mu.Unlock()
			continue

		case WSIgnoreSubscription:
			continue

		case WSSend:
		}

		if step.Send == "" {
			continue
		}
		if err := conn.Write(ctx, websocket.MessageText, []byte(step.Send)); err != nil {
			return
		}
	}
}

// sleep waits, reporting false if the connection ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
