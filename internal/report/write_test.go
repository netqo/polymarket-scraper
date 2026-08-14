package report

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAtomicProducesAReadableDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "books.json")

	if err := WriteAtomic(path, Build(baseInput())); err != nil {
		t.Fatalf("WriteAtomic returned error: %v", err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the output: %v", err)
	}

	var round Document
	if err := json.Unmarshal(contents, &round); err != nil {
		t.Fatalf("the written document does not parse: %v", err)
	}
	if round.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %q, want %q", round.SchemaVersion, SchemaVersion)
	}
	if len(round.Books) != 2 {
		t.Errorf("got %d books after the round trip, want 2", len(round.Books))
	}
	if !strings.HasSuffix(string(contents), "\n") {
		t.Error("the document does not end with a newline")
	}
}

// A consumer may read the path the instant the process exits, so a partial
// document there would look like data. Nothing may be left behind either: a
// retry after a failure has to need no cleanup.
func TestWriteAtomicLeavesNoStagingFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "books.json")

	if err := WriteAtomic(path, Build(baseInput())); err != nil {
		t.Fatalf("WriteAtomic returned error: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "books.json" {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("directory contains %v, want only books.json", names)
	}
}

func TestWriteAtomicReplacesAPreviousDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "books.json")

	first := baseInput()
	first.Requested = []string{"111"}
	if err := WriteAtomic(path, Build(first)); err != nil {
		t.Fatalf("first write returned error: %v", err)
	}

	second := baseInput()
	second.Requested = []string{"111", "222", "333"}
	if err := WriteAtomic(path, Build(second)); err != nil {
		t.Fatalf("second write returned error: %v", err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the output: %v", err)
	}

	var round Document
	if err := json.Unmarshal(contents, &round); err != nil {
		t.Fatalf("the written document does not parse: %v", err)
	}
	if len(round.Books) != 3 {
		t.Errorf("got %d books, want the second run's 3", len(round.Books))
	}
}

// When the destination cannot be written, the previous document must be left
// exactly as it was rather than truncated or half-replaced.
func TestWriteAtomicLeavesThePreviousDocumentIntactOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "books.json")

	if err := WriteAtomic(path, Build(baseInput())); err != nil {
		t.Fatalf("setup write returned error: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the output: %v", err)
	}

	// A read-only directory makes staging impossible, which is the closest
	// stand-in for a full disk that a test can arrange portably.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skipf("cannot make the directory read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := WriteAtomic(path, Build(baseInput())); err == nil {
		t.Fatal("WriteAtomic succeeded despite an unwritable directory")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the previous document became unreadable: %v", err)
	}
	if string(after) != string(before) {
		t.Error("the previous document was modified by a failed write")
	}
}

func TestWriteAtomicReportsAnUnwritableDestination(t *testing.T) {
	err := WriteAtomic(filepath.Join(t.TempDir(), "no-such-directory", "books.json"), Build(baseInput()))
	if err == nil {
		t.Fatal("WriteAtomic succeeded with a nonexistent directory")
	}
	if !strings.Contains(err.Error(), "staging") {
		t.Errorf("error %q does not say what went wrong", err)
	}
}

// The verification step re-reads from disk rather than trusting the buffer, so
// it also rejects a document that would not decode back into its own schema.
func TestVerifyRejectsSomethingThatIsNotTheSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-document.json")
	if err := os.WriteFile(path, []byte(`{"unexpected_field": 1}`), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	if err := verify(path); !errors.Is(err, ErrInvalidJSON) {
		t.Fatalf("verify returned %v, want ErrInvalidJSON", err)
	}
}

func TestVerifyAcceptsARealDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "books.json")
	if err := WriteAtomic(path, Build(baseInput())); err != nil {
		t.Fatalf("WriteAtomic returned error: %v", err)
	}

	if err := verify(path); err != nil {
		t.Errorf("verify rejected a document this package wrote: %v", err)
	}
}
