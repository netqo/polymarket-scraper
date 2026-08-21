package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// Keepalive frames.
//
// These are raw uppercase text, not JSON and not websocket protocol pings, so
// they have to be recognised before anything tries to parse them.
const (
	Ping = "PING"
	Pong = "PONG"
)

// ErrEmptyFrame reports a frame with no content at all.
var ErrEmptyFrame = errors.New("empty frame")

// IsKeepalive reports whether a frame is a keepalive rather than a message.
func IsKeepalive(frame []byte) bool {
	switch string(bytes.TrimSpace(frame)) {
	case Ping, Pong:
		return true
	default:
		return false
	}
}

// DecodeFrame decodes one websocket frame into the events it carries.
//
// A frame is either a bare object or an array of them; the initial book
// snapshot uses the array form and everything else uses the object form, so
// both have to work.
//
// Events and an error can both be returned. An array whose elements are not all
// decodable still yields the ones that were, because discarding a whole frame
// over one bad element would throw away updates for tokens that are perfectly
// fine.
//
// What the caller does with the pair is its own decision, and the engine's is
// deliberately blunt: a message it could not read is a message whose token it
// does not know, so it distrusts every token on the connection before applying
// what did decode. Snapshots in the same frame then restore trust immediately,
// which is the common case, since array framing is what the initial snapshot
// burst uses.
func DecodeFrame(frame []byte) ([]Event, error) {
	trimmed := bytes.TrimSpace(frame)
	if len(trimmed) == 0 {
		return nil, ErrEmptyFrame
	}

	switch trimmed[0] {
	case '[':
		return decodeArray(trimmed)

	case '{':
		event, err := decodeEvent(trimmed)
		if err != nil {
			return nil, err
		}
		return []Event{event}, nil

	default:
		return nil, fmt.Errorf("frame is neither an object nor an array: %s", preview(trimmed))
	}
}

// decodeArray decodes the array framing, gathering per-element failures rather
// than abandoning the whole frame at the first one.
func decodeArray(frame []byte) ([]Event, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(frame, &items); err != nil {
		return nil, fmt.Errorf("decoding frame array: %w", err)
	}

	events := make([]Event, 0, len(items))
	var failures []error

	for i, item := range items {
		event, err := decodeEvent(item)
		if err != nil {
			failures = append(failures, fmt.Errorf("element %d: %w", i, err))
			continue
		}
		events = append(events, event)
	}

	return events, errors.Join(failures...)
}

// envelope is the minimum needed to route a message to its concrete type.
//
// The discriminator is not the first field in real payloads, so it has to be
// read by parsing rather than by looking at the start of the bytes.
type envelope struct {
	EventType EventType `json:"event_type"`
}

// decodeEvent decodes a single message object.
func decodeEvent(raw []byte) (Event, error) {
	var head envelope
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, fmt.Errorf("reading event_type: %w", err)
	}
	if head.EventType == "" {
		return nil, fmt.Errorf("message has no event_type: %s", preview(raw))
	}

	switch head.EventType {
	case EventBook:
		return decodeInto[Book](raw)
	case EventPriceChange:
		return decodeInto[PriceChange](raw)
	case EventLastTradePrice:
		return decodeInto[LastTrade](raw)
	case EventTickSizeChange:
		return decodeInto[TickSizeChange](raw)
	case EventBestBidAsk:
		return decodeInto[BestBidAsk](raw)
	case EventNewMarket:
		return decodeInto[NewMarket](raw)
	case EventMarketResolved:
		return decodeInto[MarketResolved](raw)
	default:
		// Unrecognised types are reported, not rejected: a new event type is a
		// protocol addition to notice, not a reason to distrust a token.
		return Unknown{EventType: head.EventType, Raw: append(json.RawMessage(nil), raw...)}, nil
	}
}

// decodeInto decodes raw into T. Unknown fields are ignored, so the feed can
// grow fields without breaking this build.
func decodeInto[T Event](raw []byte) (Event, error) {
	var event T
	if err := json.Unmarshal(raw, &event); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", event.Type(), err)
	}

	return event, nil
}

// maxPreviewBytes bounds how much of a payload an error quotes.
//
// It is generous rather than tight, because the payload is the only evidence of
// what went wrong and 120 characters routinely stopped short of the part that
// mattered. It is still bounded: a frame may be as large as wsclient's read
// limit allows, and an error carrying megabytes would reach both the run's
// error list and the output document.
//
// Deciding how much of this to actually show is not this package's business.
// Each destination trims it further: the terminal to a couple of lines, the
// document to a sentence, and the log file not at all.
const maxPreviewBytes = 8 << 10

// preview quotes a payload for an error message, bounded so that one enormous
// frame cannot dominate everything downstream of it.
func preview(raw []byte) string {
	if len(raw) <= maxPreviewBytes {
		return string(raw)
	}

	return string(raw[:maxPreviewBytes]) + "... (" + strconv.Itoa(len(raw)) + " bytes)"
}
