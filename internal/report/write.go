package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrInvalidJSON reports that the document did not survive its own round trip,
// which means the file would have been written but not readable.
var ErrInvalidJSON = errors.New("the document did not decode back into its own schema")

// WriteAtomic writes the document so the destination is never observed
// half-written.
//
// A consumer may read the path the instant the process exits, and a truncated
// document there is worse than no document at all: it looks like data. So the
// document is staged, verified, and only then moved into place with a rename,
// which the filesystem performs as a single operation.
//
// The staging file is created in the destination's own directory, because
// rename is only atomic within one filesystem and a temporary directory
// elsewhere would silently degrade it into a copy. It has a randomised name, so
// two runs writing at once cannot corrupt each other, and it is removed on
// every failure path, so a retry needs no cleanup.
func WriteAtomic(path string, doc Document) error {
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the document: %w", err)
	}
	encoded = append(encoded, '\n')

	directory := filepath.Dir(path)

	staged, err := os.CreateTemp(directory, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating a staging file in %s: %w", directory, err)
	}
	stagedPath := staged.Name()
	defer func() { _ = os.Remove(stagedPath) }()

	if err := writeAndSync(staged, encoded); err != nil {
		return err
	}
	if err := verify(stagedPath); err != nil {
		return err
	}

	if err := os.Rename(stagedPath, path); err != nil {
		return fmt.Errorf("moving the document into place: %w", err)
	}

	return syncDirectory(directory)
}

// writeAndSync writes the payload and flushes it to disk, closing the file
// whatever happens.
func writeAndSync(file *os.File, payload []byte) error {
	defer func() { _ = file.Close() }()

	if _, err := file.Write(payload); err != nil {
		return fmt.Errorf("writing the document: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("flushing the document: %w", err)
	}

	return nil
}

// verify reads the staged file back from disk and decodes it into the schema.
//
// Re-reading rather than checking the buffer we just built is the point: it is
// what catches a short write, a full disk, or anything else that made the bytes
// on disk differ from the bytes we meant to write.
func verify(path string) error {
	// #nosec G304 -- the path was created by this function, in a directory the
	// operator named on the command line.
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("re-opening the staged document: %w", err)
	}
	defer func() { _ = file.Close() }()

	var round Document
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&round); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidJSON, err)
	}

	return nil
}

// syncDirectory flushes the directory entry, so the rename survives a crash
// rather than only the file contents.
func syncDirectory(path string) error {
	// #nosec G304 -- the directory holds the operator-supplied output path.
	directory, err := os.Open(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("opening %s to flush it: %w", path, err)
	}
	defer func() { _ = directory.Close() }()

	if err := directory.Sync(); err != nil {
		return fmt.Errorf("flushing %s: %w", path, err)
	}

	return nil
}
