package report

import (
	"fmt"
	"time"
)

// SummaryLine renders the one line written to stdout on success.
//
// It exists so a consumer can confirm the run's health without parsing the
// document first, and it is deliberately grep-friendly: fixed keys, no
// punctuation that a shell would eat, everything on one line. Because it is
// written only on success, non-empty stdout is by itself a reliable signal that
// the document at the output path is complete and valid.
func SummaryLine(doc Document, outPath string) string {
	return fmt.Sprintf(
		"OK tokens=%d/%d discovered=%d connections=%d reconnects=%d resyncs=%d errors=%d duration=%s out=%s",
		doc.TokensOK,
		doc.TokensRequested,
		doc.TokensDiscovered,
		doc.Connection.WSConnections,
		doc.Connection.Reconnects,
		doc.Connection.RESTResyncs,
		len(doc.Errors),
		elapsed(doc),
		outPath,
	)
}

// elapsed reports the wall clock length of the run, rounded to the second.
// It is the whole run, not the collection window: the difference is how long
// shutdown took, which is the thing worth noticing when it grows.
func elapsed(doc Document) string {
	started, startErr := time.Parse(TimeFormat, doc.StartedAt)
	finished, finishErr := time.Parse(TimeFormat, doc.FinishedAt)
	if startErr != nil || finishErr != nil {
		return "unknown"
	}

	return finished.Sub(started).Round(time.Second).String()
}
