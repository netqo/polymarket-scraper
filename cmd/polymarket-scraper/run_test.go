package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tokenFile writes a token list and returns its path.
func tokenFile(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "tokens.txt")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing token file: %v", err)
	}

	return path
}

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

// F1: --help is what a consuming agent reads to learn how to call the tool, so
// it goes to stdout and succeeds rather than being treated as a usage mistake.
func TestRunHelpFlagSucceedsAndDescribesTheTool(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		t.Run(flag, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			if code := run([]string{flag}, &stdout, &stderr); code != exitOK {
				t.Fatalf("run(%q) = %d, want %d", flag, code, exitOK)
			}

			out := stdout.String()
			for _, want := range []string{"--tokens", "--out", "--duration", "EXIT STATUS"} {
				if !strings.Contains(out, want) {
					t.Errorf("help output does not mention %q", want)
				}
			}
		})
	}
}

// A failed run must leave stdout empty, so the consuming agent can use
// "stdout is non-empty" as a success predicate without parsing anything.
func TestRunUsageErrorKeepsStdoutEmpty(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no arguments", nil},
		{"missing output path", []string{"--tokens", "t.txt"}},
		{"unknown flag", []string{"--tokens", "t.txt", "--out", "o.json", "--nope"}},
		{"unreadable token file", []string{"--tokens", "/nonexistent/tokens.txt", "--out", "o.json"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			if code := run(tt.args, &stdout, &stderr); code != exitUsage {
				t.Fatalf("run(%v) = %d, want %d", tt.args, code, exitUsage)
			}
			if stdout.Len() != 0 {
				t.Errorf("failed run wrote to stdout: %q", stdout.String())
			}
			if stderr.Len() == 0 {
				t.Error("failed run explained nothing on stderr")
			}
		})
	}
}

func TestRunReportsTokenListAnomalies(t *testing.T) {
	path := tokenFile(t, "111\n111\nnot-a-token\n")

	var stdout, stderr bytes.Buffer
	run([]string{"--tokens", path, "--out", filepath.Join(t.TempDir(), "books.json")}, &stdout, &stderr)

	logs := stderr.String()
	if !strings.Contains(logs, "duplicates=1") {
		t.Errorf("logs do not report the collapsed duplicate:\n%s", logs)
	}
	if !strings.Contains(logs, "not-a-token") {
		t.Errorf("logs do not report the suspicious id:\n%s", logs)
	}
	if stdout.Len() != 0 {
		t.Errorf("incomplete run wrote to stdout: %q", stdout.String())
	}
}
