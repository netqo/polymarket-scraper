// Test data: Invented messages. What is under test is collapsing, capping and truncation,
// which hold whatever the text says.

package engine

import (
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/netqo/polymarket-scraper/internal/config"
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

	for i := range config.DefaultMaxErrors + 25 {
		sink.Addf("failure %d", i)
	}

	got := sink.Messages()
	if len(got) != config.DefaultMaxErrors+1 {
		t.Fatalf("got %d messages, want the cap of %d plus the accounting line", len(got), config.DefaultMaxErrors)
	}
	if !strings.Contains(got[len(got)-1], "25 further distinct messages were suppressed") {
		t.Errorf("last message = %q, want it to account for what was dropped", got[len(got)-1])
	}
}

// This is the failure the whole change exists for: a reconnect loop used to
// fill every slot with one sentence and push out everything worth reading.
func TestErrorSinkCollapsesRepeatsIntoOneEntry(t *testing.T) {
	var sink errorSink

	for range 1200 {
		sink.Addf("shard 0: connection ended (idle), reconnecting")
	}
	sink.Addf("token 83191 was left out of a re-seed response")

	got := sink.Messages()
	if len(got) != 2 {
		t.Fatalf("got %d messages, want the repeat collapsed into one: %v", len(got), got)
	}
	if !strings.Contains(got[0], "(x1200)") {
		t.Errorf("first message = %q, want the occurrence count", got[0])
	}
	// The point of collapsing: the later, more interesting message survives a
	// flood that used to bury it well past the cap.
	if !strings.Contains(got[1], "83191") {
		t.Errorf("second message = %q, want the message that followed the flood", got[1])
	}
}

// Recording the first occurrence in place rather than the last keeps the list
// in the order things actually went wrong.
func TestErrorSinkKeepsARepeatInItsOriginalPosition(t *testing.T) {
	var sink errorSink

	sink.Addf("first")
	sink.Addf("second")
	sink.Addf("first")

	got := sink.Messages()
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2: %v", len(got), got)
	}
	if !strings.HasPrefix(got[0], "first") || !strings.Contains(got[0], "(x2)") {
		t.Errorf("messages = %v, want the repeat counted in its first position", got)
	}
}

// A message that happened once says so by saying nothing; "(x1)" would be noise
// on the overwhelming majority of entries.
func TestErrorSinkDoesNotAnnotateASingleOccurrence(t *testing.T) {
	var sink errorSink

	sink.Addf("something happened once")

	if got := sink.Messages()[0]; strings.Contains(got, "(x") {
		t.Errorf("message = %q, want no count for a single occurrence", got)
	}
}

// The engine records from a goroutine per shard and per connection.
func TestErrorSinkIsSafeUnderConcurrentUse(t *testing.T) {
	var sink errorSink
	var wg sync.WaitGroup

	for shard := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				sink.Addf("shard %d: connection ended, reconnecting", shard)
			}
		}()
	}
	wg.Wait()

	got := sink.Messages()
	if len(got) != 8 {
		t.Fatalf("got %d messages, want one per shard: %v", len(got), got)
	}
	for _, message := range got {
		if !strings.Contains(message, "(x100)") {
			t.Errorf("message = %q, want all 100 occurrences counted", message)
		}
	}
}

// A decode failure quotes the frame it could not read, and a frame runs to
// kilobytes. These strings are meant to be read and quoted verbatim, so one of
// them running to several pages would make the whole list useless.
func TestErrorSinkShortensAnEnormousMessage(t *testing.T) {
	var sink errorSink

	const size = config.DefaultMaxErrorLength * 20
	sink.Addf("a frame could not be decoded: %s", strings.Repeat("x", size))

	got := sink.Messages()[0]
	if len(got) > config.DefaultMaxErrorLength+64 {
		t.Errorf("message is %d bytes, want it bounded near %d", len(got), config.DefaultMaxErrorLength)
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
