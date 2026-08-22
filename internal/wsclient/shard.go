// Package wsclient carries one websocket connection's worth of tokens.
//
// A shard knows nothing about order books or trust. It dials, subscribes,
// keeps the connection alive, decodes what arrives, and hands it upward with
// the time it arrived. Everything about what the messages mean lives elsewhere,
// which is what lets the transport be tested against a scripted server and the
// interpretation be tested with no network at all.
//
// Two details of the protocol are easy to get wrong and expensive to get wrong
// quietly: the keepalive is a text frame rather than a protocol ping, and past
// roughly 750 assets a subscription is accepted but its initial snapshot
// silently never arrives. PROTOCOL.md explains both. SnapshotsSeen exists to
// detect the second, since a client that does not count what it received looks
// perfectly healthy while holding no book state at all.
package wsclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/netqo/polymarket-scraper/internal/logging"
	"github.com/netqo/polymarket-scraper/internal/wire"
)

// outboundBuffer bounds the queue of frames waiting to be written. Keepalives
// and subscription changes are both small and infrequent, so a connection that
// cannot drain this is one that is already dead.
const outboundBuffer = 32

// ErrIdle reports that the connection delivered nothing for longer than the
// configured timeout.
//
// It is a distinct error because it means something specific: the socket is
// still open and the far end is still there, it has simply stopped talking.
// That is the failure a keepalive exists to detect, and it is invisible to
// anything that only watches for a closed connection.
var ErrIdle = errors.New("the connection delivered nothing before the idle timeout")

// ErrDial reports that the connection could never be opened.
//
// It is distinct from every other failure because it means the tokens on this
// connection were never subscribed at all, which is a different thing to report
// than a subscription that worked and then broke.
var ErrDial = errors.New("could not open the connection")

// Frame is one batch of decoded messages, with the time they arrived.
//
// Events and Err can both be set. A frame carrying several messages may have
// some that decode and some that do not, and throwing away the whole frame
// would discard updates for tokens that are perfectly fine.
type Frame struct {
	ShardID    int
	Events     []wire.Event
	ReceivedAt time.Time
	Err        error
}

// Options configure a Shard.
type Options struct {
	// ID identifies the shard in logs and in the frames it produces.
	ID int

	// URL is the market channel endpoint.
	URL string

	// AssetIDs are the tokens this connection subscribes to.
	AssetIDs []string

	// PingInterval is how often the keepalive is sent.
	PingInterval time.Duration

	// IdleTimeout is how long silence is tolerated before the connection is
	// treated as dead.
	IdleTimeout time.Duration

	Logger *slog.Logger

	// ReadLimit bounds a single frame, in bytes. Zero uses fallbackReadLimit.
	//
	// It matters more than a buffer size usually does: the initial snapshot for
	// a full shard arrives as one frame and grows with the number of assets, so
	// a limit set too low drops exactly the message the whole run depends on.
	ReadLimit int64

	// HTTPClient replaces the dialer's transport. Tests point it at an
	// in-process server; production leaves it nil.
	HTTPClient *http.Client
}

// Shard is one websocket connection.
type Shard struct {
	opts   Options
	logger *slog.Logger
	now    func() time.Time

	outbound chan []byte

	snapshots atomic.Int64
	frames    atomic.Int64
}

// New builds a shard. It does not dial; Run does.
func New(opts Options) *Shard {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	return &Shard{
		opts:     opts,
		logger:   logger.With("shard", opts.ID),
		now:      time.Now,
		outbound: make(chan []byte, outboundBuffer),
	}
}

// SnapshotsSeen reports how many book snapshots have arrived.
//
// This is the number that catches the silent subscribe ceiling: if it is short
// of the number of assets subscribed, the connection is delivering updates for
// books we never received, and those tokens have no trustworthy state no matter
// how healthy the socket looks.
func (s *Shard) SnapshotsSeen() int { return int(s.snapshots.Load()) }

// FramesSeen reports how many frames have been read, keepalives included. A
// connection with frames but no snapshots is the symptom above.
func (s *Shard) FramesSeen() int { return int(s.frames.Load()) }

// Run dials, subscribes, and delivers decoded frames until the connection ends
// or the context is done.
//
// It returns the reason the connection ended. A caller that wants the shard to
// come back is responsible for calling Run again; reconnection is not this
// type's business, because a shard that reconnects itself cannot tell its
// caller that the tokens it carries just stopped being trustworthy.
func (s *Shard) Run(ctx context.Context, out chan<- Frame) error {
	conn, err := s.dial(ctx)
	if err != nil {
		return err
	}
	// CloseNow rather than a polite close: teardown must not block on a
	// handshake with a peer that may be the reason we are shutting down.
	defer func() { _ = conn.CloseNow() }()

	if err := s.subscribe(ctx, conn); err != nil {
		return err
	}

	// The writer owns every write after the subscription, so there is never
	// more than one writer on the connection.
	// Deferred calls run last in, first out, so these two are registered in the
	// order they must not run in: the writer's context has to be cancelled
	// before anything joins the writer. Waiting first would block until the
	// keepalive ticker next fired and its write failed, which is a whole ping
	// interval of delay on every reconnect, and a whole ping interval before
	// the tokens on this connection are told they are no longer trustworthy.
	var writer sync.WaitGroup
	defer writer.Wait()

	writerCtx, stopWriter := context.WithCancel(ctx)
	defer stopWriter()

	writer.Add(1)
	go func() {
		defer writer.Done()
		s.writeLoop(writerCtx, conn)
	}()

	return s.readLoop(ctx, conn, out)
}

// dial opens the connection.
//
// The handshake response is deliberately discarded. The library documents that
// its body must not be closed by the caller and sets it to nil on success, so
// closing it here would be reaching into a stream the connection has taken
// over.
func (s *Shard) dial(ctx context.Context) (*websocket.Conn, error) {
	//nolint:bodyclose // see above: the library owns the handshake response body
	conn, _, err := websocket.Dial(ctx, s.opts.URL, &websocket.DialOptions{
		HTTPClient: s.opts.HTTPClient,
		// The market channel does not negotiate compression, and asking for it
		// only adds a way for the handshake to fail.
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: dialing %s: %w", ErrDial, s.opts.URL, err)
	}

	// Book snapshots for several hundred assets arrive in a single frame, which
	// is far larger than the library's default limit.
	conn.SetReadLimit(s.readLimit())

	s.logger.Info("connected", logging.Cat(logging.CategoryConnection),
		"assets", len(s.opts.AssetIDs))

	return conn, nil
}

// fallbackReadLimit is used when a caller leaves ReadLimit at zero. The real
// default lives in the config package, which is the one place it can be changed
// without rebuilding.
const fallbackReadLimit = 32 << 20

// readLimit is how large a single frame may be.
func (s *Shard) readLimit() int64 {
	if s.opts.ReadLimit > 0 {
		return s.opts.ReadLimit
	}

	return fallbackReadLimit
}

// subscribe sends the opening subscription.
func (s *Shard) subscribe(ctx context.Context, conn *websocket.Conn) error {
	payload, err := json.Marshal(wire.NewSubscription(s.opts.AssetIDs))
	if err != nil {
		return fmt.Errorf("encoding the subscription: %w", err)
	}

	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		return fmt.Errorf("sending the subscription: %w", err)
	}

	return nil
}

// Subscribe asks for more tokens on a live connection.
//
// It never blocks. A connection whose write queue is full is one that is
// already failing, and blocking here would spread that failure to whatever is
// deciding to subscribe.
func (s *Shard) Subscribe(assetIDs []string) bool {
	if len(assetIDs) == 0 {
		return true
	}

	payload, err := json.Marshal(wire.Subscribe(assetIDs))
	if err != nil {
		return false
	}

	select {
	case s.outbound <- payload:
		return true
	default:
		s.logger.Warn("dropped a subscription change: the write queue is full",
			logging.Cat(logging.CategoryConnection), "assets", len(assetIDs))
		return false
	}
}

// writeLoop sends keepalives and queued messages.
func (s *Shard) writeLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(s.opts.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			// The literal text PING, not a protocol ping: the server answers
			// this and ignores the other.
			if err := conn.Write(ctx, websocket.MessageText, []byte(wire.Ping)); err != nil {
				s.logger.Debug("keepalive failed", logging.Cat(logging.CategoryConnection), "error", err)
				return
			}

		case payload := <-s.outbound:
			if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
				s.logger.Debug("write failed", logging.Cat(logging.CategoryConnection), "error", err)
				return
			}
		}
	}
}

// readLoop reads until the connection ends.
func (s *Shard) readLoop(ctx context.Context, conn *websocket.Conn, out chan<- Frame) error {
	for {
		payload, err := s.read(ctx, conn)
		if err != nil {
			return err
		}

		s.frames.Add(1)

		// Keepalive replies are raw text and would fail JSON parsing, so they
		// are recognised before anything tries.
		if wire.IsKeepalive(payload) {
			continue
		}

		events, decodeErr := wire.DecodeFrame(payload)
		s.countSnapshots(events)

		frame := Frame{
			ShardID:    s.opts.ID,
			Events:     events,
			ReceivedAt: s.now(),
			Err:        decodeErr,
		}

		select {
		case out <- frame:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// read performs one read with an idle deadline of its own.
//
// Deriving the timeout per read is what turns silence into a reportable event
// rather than a hang: there is no shared last-seen timestamp to race on, and no
// separate watchdog to keep in step with the reader.
func (s *Shard) read(ctx context.Context, conn *websocket.Conn) ([]byte, error) {
	readCtx, cancel := context.WithTimeout(ctx, s.opts.IdleTimeout)
	defer cancel()

	_, payload, err := conn.Read(readCtx)
	if err == nil {
		return payload, nil
	}

	// A deadline that expired while the outer context is still alive means the
	// far end went quiet, which is a different thing from the run ending.
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return nil, fmt.Errorf("%w after %v", ErrIdle, s.opts.IdleTimeout)
	}

	return nil, fmt.Errorf("reading from the connection: %w", err)
}

// countSnapshots records book snapshots as they arrive.
func (s *Shard) countSnapshots(events []wire.Event) {
	for _, event := range events {
		if _, isBook := event.(wire.Book); isBook {
			s.snapshots.Add(1)
		}
	}
}
