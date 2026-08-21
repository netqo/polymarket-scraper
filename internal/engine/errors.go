package engine

import (
	"fmt"
	"sync"
)

// maxErrors caps the run's error list.
//
// The cap exists because the failure mode it guards against is real: a
// connection that drops and redials in a tight loop produces one message per
// attempt, and an uncapped list would grow without bound in exactly the run
// where memory is already under pressure. Losing the ten-thousandth copy of a
// message costs nothing; the count of what was dropped is kept instead.
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
	mu         sync.Mutex
	messages   []string
	suppressed int
}

// Addf records a message.
func (s *errorSink) Addf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.messages) >= maxErrors {
		s.suppressed++
		return
	}

	s.messages = append(s.messages, shorten(fmt.Sprintf(format, args...)))
}

// shorten bounds one message, saying how much was left out rather than trailing
// off, so a reader can tell a truncated message from a complete one.
func shorten(message string) string {
	if len(message) <= maxErrorLength {
		return message
	}

	return message[:maxErrorLength] + fmt.Sprintf("... (%d bytes, in full in the log)", len(message))
}

// Messages returns the collected messages, with a final entry accounting for
// anything the cap dropped so the list is never quietly incomplete.
func (s *errorSink) Messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	messages := make([]string, len(s.messages), len(s.messages)+1)
	copy(messages, s.messages)

	if s.suppressed > 0 {
		messages = append(messages, fmt.Sprintf("%d further messages were suppressed", s.suppressed))
	}

	return messages
}
