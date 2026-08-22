// Test data: Invented data. The summary line is this program's own format.

package report

import (
	"strings"
	"testing"
)

// F3: the line lets a consumer confirm the run's health without parsing the
// document, so it is fixed keys on one line with nothing a shell would eat.
func TestSummaryLine(t *testing.T) {
	in := baseInput()
	in.Connection = Connection{WSConnections: 2, Reconnects: 1, RESTResyncs: 3, RESTRequests: 12}
	in.Errors = []string{"connection 2: no frame for 30s, reconnecting"}

	got := SummaryLine(Build(in), "books.json")

	const want = "OK tokens=1/2 discovered=0 connections=2 reconnects=1 resyncs=3 errors=1 duration=1m32s out=books.json"
	if got != want {
		t.Errorf("SummaryLine =\n %s\nwant\n %s", got, want)
	}
}

func TestSummaryLineIsASingleLine(t *testing.T) {
	got := SummaryLine(Build(baseInput()), "/tmp/books.json")

	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("SummaryLine contains a line break: %q", got)
	}
}

// The duration is the whole run rather than the collection window, because the
// difference between them is shutdown time, which is the thing worth noticing
// when it starts growing.
func TestSummaryLineReportsTheWholeRunNotTheWindow(t *testing.T) {
	doc := Build(baseInput())
	if doc.WindowSeconds != 90 {
		t.Fatalf("setup: WindowSeconds = %d", doc.WindowSeconds)
	}

	if !strings.Contains(SummaryLine(doc, "out.json"), "duration=1m32s") {
		t.Errorf("SummaryLine = %q, want the 92s wall clock rather than the 90s window",
			SummaryLine(doc, "out.json"))
	}
}

func TestSummaryLineSurvivesUnparseableTimestamps(t *testing.T) {
	doc := Build(baseInput())
	doc.StartedAt = "not a timestamp"

	if !strings.Contains(SummaryLine(doc, "out.json"), "duration=unknown") {
		t.Errorf("SummaryLine = %q, want duration=unknown", SummaryLine(doc, "out.json"))
	}
}
