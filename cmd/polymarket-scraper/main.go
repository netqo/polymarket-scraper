// Command polymarket-scraper collects live Polymarket order books for a fixed
// list of outcome tokens over a fixed time window and writes a single atomic
// JSON document describing what it saw.
//
// It is a deliberately dumb, honest pipe. It applies no filtering, computes no
// edges, fees, or scores, and never authenticates: all judgement belongs to the
// agent that consumes the output. Its one hard promise is that a book reported
// as current really is current, and everything else is reported as an explicit
// failure rather than silently passed off as fresh data.
//
// See SCHEMA.md for the output contract and docs/DESIGN.md for the design.
package main

import "os"

// main is intentionally trivial: os.Exit skips deferred functions, so every
// defer in the program must live below this frame, in run.
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
