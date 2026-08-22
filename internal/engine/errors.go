package engine

import (
	"fmt"
	"sync"

	"github.com/netqo/polymarket-scraper/internal/config"
)

// errorSink collects the human-readable descriptions that go into the output
// document.
//
// The strings are meant to be quoted verbatim by whatever reads the document,
// so they name what happened and where rather than merely reporting that
// something went wrong.
//
// Two bounds apply, and both are settings rather than constants: see
// limits.max_errors and limits.max_error_length.
//
// The cap on distinct messages exists because a connection that drops and
// redials in a tight loop produces one message per attempt, and an uncapped
// list would grow without bound in exactly the run where memory is already
// under pressure. It counts distinct messages rather than occurrences, because
// repeats are collapsed; before that, a loop filled every slot with the same
// sentence and pushed out every later message worth reading, which is the
// opposite of what a cap is for.
//
// The bound on one message exists because a decode failure quotes the frame it
// could not read, and a frame can be kilobytes. Anything cut here is in the log
// file in full, which is where someone who wants it should go.
type errorSink struct {
	mu sync.Mutex

	// maxMessages and maxLength are zero on a zero-valued sink, which then
	// falls back to the defaults. That keeps the type usable without a
	// constructor, which its tests rely on.
	maxMessages int
	maxLength   int

	// messages holds each distinct message once, in first-occurrence order.
	messages []string

	// counts is how many times each message was recorded, which is what turns
	// a thousand identical failures into one line and a number.
	counts map[string]int

	suppressed int
}

// newErrorSink builds a sink with the run's configured bounds.
func newErrorSink(cfg config.Config) *errorSink {
	return &errorSink{maxMessages: cfg.MaxErrors, maxLength: cfg.MaxErrorLength}
}

// Addf records a message.
//
// A message identical to one already recorded costs no additional slot; it
// increments that message's count instead. Recording the first occurrence in
// place rather than the last keeps the list in the order things went wrong.
func (s *errorSink) Addf(format string, args ...any) {
	message := s.shorten(fmt.Sprintf(format, args...))

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.counts[message] > 0 {
		s.counts[message]++
		return
	}

	if len(s.messages) >= s.messageCap() {
		s.suppressed++
		return
	}

	if s.counts == nil {
		s.counts = make(map[string]int)
	}
	s.counts[message] = 1
	s.messages = append(s.messages, message)
}

// Messages returns the collected messages, each annotated with how many times
// it happened, and a final entry accounting for anything the cap dropped so the
// list is never quietly incomplete.
func (s *errorSink) Messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	messages := make([]string, 0, len(s.messages)+1)
	for _, message := range s.messages {
		if count := s.counts[message]; count > 1 {
			message = fmt.Sprintf("%s (x%d)", message, count)
		}
		messages = append(messages, message)
	}

	if s.suppressed > 0 {
		messages = append(messages,
			fmt.Sprintf("%d further distinct messages were suppressed", s.suppressed))
	}

	return messages
}

// messageCap is how many distinct messages this sink keeps.
func (s *errorSink) messageCap() int {
	if s.maxMessages > 0 {
		return s.maxMessages
	}

	return config.DefaultMaxErrors
}

// lengthCap is how long one message may be.
func (s *errorSink) lengthCap() int {
	if s.maxLength > 0 {
		return s.maxLength
	}

	return config.DefaultMaxErrorLength
}

// shorten bounds one message, saying how much was left out rather than trailing
// off, so a reader can tell a truncated message from a complete one.
func (s *errorSink) shorten(message string) string {
	limit := s.lengthCap()
	if len(message) <= limit {
		return message
	}

	return message[:limit] + fmt.Sprintf("... (%d bytes, in full in the log)", len(message))
}
