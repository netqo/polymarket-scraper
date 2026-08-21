package engine

import (
	"strconv"
	"strings"
	"testing"
)

func TestErrorSinkKeepsMessagesInOrder(t *testing.T) {
	var sink errorSink

	sink.Addf("first")
	sink.Addf("second %d", 2)

	got := sink.Messages()
	if len(got) != 2 || got[0] != "first" || got[1] != "second 2" {
		t.Errorf("messages = %v, want them in the order they were recorded", got)
	}
}

func TestErrorSinkOnAQuietRunIsEmptyNotNil(t *testing.T) {
	var sink errorSink

	if got := sink.Messages(); got == nil {
		t.Error("Messages() = nil, which would serialize as null rather than []")
	} else if len(got) != 0 {
		t.Errorf("Messages() = %v, want empty", got)
	}
}

// The cap exists because a connection redialling in a tight loop would
// otherwise grow this list without bound, but a truncated list that does not
// say it was truncated reads as a complete one.
func TestErrorSinkCapsTheListAndSaysSo(t *testing.T) {
	var sink errorSink

	for i := range maxErrors + 25 {
		sink.Addf("failure %d", i)
	}

	got := sink.Messages()
	if len(got) != maxErrors+1 {
		t.Fatalf("got %d messages, want the cap of %d plus the accounting line", len(got), maxErrors)
	}
	if !strings.Contains(got[len(got)-1], "25 further messages were suppressed") {
		t.Errorf("last message = %q, want it to account for what was dropped", got[len(got)-1])
	}
}

// A decode failure quotes the frame it could not read, and a frame runs to
// kilobytes. These strings are meant to be read and quoted verbatim, so one of
// them running to several pages would make the whole list useless.
func TestErrorSinkShortensAnEnormousMessage(t *testing.T) {
	var sink errorSink

	const size = maxErrorLength * 20
	sink.Addf("a frame could not be decoded: %s", strings.Repeat("x", size))

	got := sink.Messages()[0]
	if len(got) > maxErrorLength+64 {
		t.Errorf("message is %d bytes, want it bounded near %d", len(got), maxErrorLength)
	}
	if !strings.Contains(got, "in full in the log") {
		t.Errorf("message = %q..., want it to point at where the rest is", got[:80])
	}
	// Trailing off silently would leave a reader unable to tell a truncated
	// message from a complete one.
	if !strings.Contains(got, strconv.Itoa(size+len("a frame could not be decoded: "))) {
		t.Errorf("message = %q..., want it to report the true length", got[:80])
	}
}

func TestErrorSinkLeavesOrdinaryMessagesAlone(t *testing.T) {
	var sink errorSink

	const message = "shard 0: connection ended (no frame for 30s), reconnecting"
	sink.Addf("%s", message)

	if got := sink.Messages()[0]; got != message {
		t.Errorf("message = %q, want it untouched", got)
	}
}
