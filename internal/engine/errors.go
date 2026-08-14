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

	s.messages = append(s.messages, fmt.Sprintf(format, args...))
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
