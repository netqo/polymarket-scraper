package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersionFlagWritesToStdoutOnly(t *testing.T) {
	for _, flag := range []string{"--version", "-version"} {
		t.Run(flag, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			if code := run([]string{flag}, &stdout, &stderr); code != exitOK {
				t.Fatalf("run(%q) = %d, want %d", flag, code, exitOK)
			}
			if got := strings.TrimSpace(stdout.String()); got == "" {
				t.Fatal("--version printed nothing to stdout")
			}
			if stderr.Len() != 0 {
				t.Fatalf("--version wrote to stderr: %q", stderr.String())
			}
		})
	}
}

// A failed run must leave stdout empty, so the consuming agent can use
// "stdout is non-empty" as a success predicate without parsing anything.
func TestRunFailureKeepsStdoutEmpty(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if code := run(nil, &stdout, &stderr); code != exitUsage {
		t.Fatalf("run(nil) = %d, want %d", code, exitUsage)
	}
	if stdout.Len() != 0 {
		t.Fatalf("failed run wrote to stdout: %q", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("failed run logged nothing to stderr")
	}
}
