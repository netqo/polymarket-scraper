// Test data: Invented data, and deliberately pointed at an unreachable endpoint. What is
// under test is that a run whose every token fails still writes a valid document
// and still exits 0, which is easiest to arrange by guaranteeing the failure.

package main

import (
	"bytes"
	"encoding/json"
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

// offlineArgs point the binary at an unreachable endpoint with a short window,
// so a full run completes quickly and without touching the network. Every token
// fails, which is the point: the run still has to finish and write a document.
func offlineArgs(tokensPath, outPath string) []string {
	return []string{
		"--tokens", tokensPath,
		"--out", outPath,
		"--rest-only",
		"--rest-url", "http://127.0.0.1:1",
		"--duration", "1",
		"--grace", "2",
	}
}

func TestRunReportsTokenListAnomalies(t *testing.T) {
	path := tokenFile(t, "111\n111\nnot-a-token\n")
	out := filepath.Join(t.TempDir(), "books.json")

	var stdout, stderr bytes.Buffer
	if code := run(offlineArgs(path, out), &stdout, &stderr); code != exitOK {
		t.Fatalf("run = %d, want %d; stderr:\n%s", code, exitOK, stderr.String())
	}

	logs := stderr.String()
	if !strings.Contains(logs, "duplicates=1") {
		t.Errorf("logs do not report the collapsed duplicate:\n%s", logs)
	}
	if !strings.Contains(logs, "not-a-token") {
		t.Errorf("logs do not report the suspicious id:\n%s", logs)
	}
}

// A run where every token fails is still a successful run: the document exists,
// it is valid, and each token says what went wrong. Exit 0 means "the output is
// usable", not "everything worked".
func TestRunWritesADocumentEvenWhenEveryTokenFails(t *testing.T) {
	path := tokenFile(t, "111\n222\n")
	out := filepath.Join(t.TempDir(), "books.json")

	var stdout, stderr bytes.Buffer
	if code := run(offlineArgs(path, out), &stdout, &stderr); code != exitOK {
		t.Fatalf("run = %d, want %d; stderr:\n%s", code, exitOK, stderr.String())
	}

	contents, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the output document was not written: %v", err)
	}

	var document struct {
		SchemaVersion string                    `json:"schema_version"`
		Books         map[string]map[string]any `json:"books"`
	}
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatalf("the output document does not parse: %v", err)
	}
	if document.SchemaVersion == "" {
		t.Error("the document has no schema version")
	}
	if len(document.Books) != 2 {
		t.Errorf("the document has %d books, want both requested tokens", len(document.Books))
	}
	for id, token := range document.Books {
		if token["status"] == "ok" {
			t.Errorf("token %s is ok despite the endpoint being unreachable", id)
		}
	}

	// F3: exactly one summary line, on stdout, only on success.
	summary := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(summary, "OK ") {
		t.Errorf("stdout = %q, want the summary line", summary)
	}
	if strings.Count(summary, "\n") != 0 {
		t.Errorf("stdout has more than one line: %q", summary)
	}
}
