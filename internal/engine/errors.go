package engine

import (
	"fmt"
	"sync"
)

// maxErrors caps how many distinct messages the run's error list holds.
//
// The cap exists because the failure mode it guards against is real: a
// connection that drops and redials in a tight loop produces one message per
// attempt, and an uncapped list would grow without bound in exactly the run
// where memory is already under pressure.
//
// It counts distinct messages rather than occurrences, because repeats are
// collapsed. Before that, a loop like the one above filled all five hundred
// slots with the same sentence and pushed out every later message that was
// actually worth reading, which is the opposite of what a cap is for.
const maxErrors = 500

// maxErrorLength bounds one message.
//
// A decode failure quotes the frame it could not read, and a frame can be
// kilobytes. These strings are meant to be read, and quoted verbatim into a
// consuming agent's own report, so one of them running to several pages would
// make the whole list useless. The full payload is in the log file, which is
// where someone who wants it should go.
const maxErrorLength = 500

// errorSink collects the human-readable descriptions that go into the output
// document.
//
// The strings are meant to be quoted verbatim by whatever reads the document,
// so they name what happened and where rather than merely reporting that
// something went wrong.
type errorSink struct {
	mu sync.Mutex

	// messages holds each distinct message once, in first-occurrence order.
	messages []string

	// counts is how many times each message was recorded, which is what turns
	// a thousand identical failures into one line and a number.
	counts map[string]int

	suppressed int
}

// Addf records a message.
//
// A message identical to one already recorded costs no additional slot; it
// increments that message's count instead. Recording the first occurrence in
// place rather than the last keeps the list in the order things went wrong.
func (s *errorSink) Addf(format string, args ...any) {
	message := shorten(fmt.Sprintf(format, args...))

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.counts[message] > 0 {
		s.counts[message]++
		return
	}

	if len(s.messages) >= maxErrors {
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

// shorten bounds one message, saying how much was left out rather than trailing
// off, so a reader can tell a truncated message from a complete one.
func shorten(message string) string {
	if len(message) <= maxErrorLength {
		return message
	}

	return message[:maxErrorLength] + fmt.Sprintf("... (%d bytes, in full in the log)", len(message))
}
