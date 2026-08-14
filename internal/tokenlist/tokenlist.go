// Package tokenlist loads the set of outcome tokens a run is asked about.
//
// It is the one place that defines what "requested" means, which matters more
// than the file parsing: requirement C4 says every requested token must appear
// in the output exactly once, and the report builder iterates this list as its
// authority rather than trusting the collection pipeline to have produced an
// entry. Duplicates are therefore collapsed here, once, at the source.
//
// Two input formats are accepted (requirement A3): one token id per line, or a
// JSON array of token id strings. Token ids are long decimal strings and are
// always handled as strings; a 77-digit id cannot survive being read as a JSON
// number, so an array of numbers is rejected outright rather than silently
// truncated into plausible-looking wrong ids.
package tokenlist

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrEmpty reports that the input contained no token ids at all. A run with
// nothing to collect is a configuration mistake, not an empty result.
var ErrEmpty = errors.New("no token ids found")

// byteOrderMark is stripped from the front of the file so an editor-added BOM
// cannot corrupt the first token id.
var byteOrderMark = []byte("\xef\xbb\xbf")

// List is the outcome of loading a token file.
type List struct {
	// IDs are the requested token ids, deduplicated, in first-occurrence order.
	IDs []string

	// Duplicates counts the entries dropped as repeats of an earlier id.
	Duplicates int

	// Suspicious lists ids that do not look like Polymarket token ids, which
	// are long decimal strings. They are still present in IDs: dropping them
	// would hide them from the output entirely, when what the consuming agent
	// needs is to see them fail explicitly.
	Suspicious []string
}

// Load reads and parses a token file.
func Load(path string) (List, error) {
	// #nosec G304 -- the path is an operator-supplied command line argument;
	// reading an arbitrary file is the entire purpose of the flag.
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return List{}, fmt.Errorf("reading token file: %w", err)
	}

	list, err := Parse(data)
	if err != nil {
		return List{}, fmt.Errorf("token file %s: %w", path, err)
	}

	return list, nil
}

// Parse reads a token list from memory, detecting the format from the first
// non-whitespace byte.
func Parse(data []byte) (List, error) {
	data = bytes.TrimPrefix(data, byteOrderMark)

	var (
		entries []string
		err     error
	)
	if leading := bytes.TrimLeft(data, " \t\r\n"); len(leading) > 0 && leading[0] == '[' {
		entries, err = parseJSONArray(leading)
	} else {
		entries, err = parseLines(data)
	}
	if err != nil {
		return List{}, err
	}

	return collect(entries)
}

// parseLines reads the one-id-per-line format, skipping blank lines and lines
// commented out with a leading '#' so a shortlist can be annotated by hand.
func parseLines(data []byte) ([]string, error) {
	var entries []string

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entries = append(entries, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading token lines: %w", err)
	}

	return entries, nil
}

// parseJSONArray reads the JSON array format.
//
// Entries are checked for being JSON strings before decoding, rather than
// relying on the decoder: unmarshalling a JSON null into a string succeeds and
// leaves it empty, which would silently drop an entry.
func parseJSONArray(data []byte) ([]string, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("parsing token file as a JSON array: %w", err)
	}

	entries := make([]string, 0, len(items))
	for i, item := range items {
		if len(item) == 0 || item[0] != '"' {
			return nil, fmt.Errorf(
				"entry %d (%s) is not a string: token ids must be quoted strings, "+
					"because a 77-digit id cannot survive being read as a JSON number",
				i, item)
		}

		var id string
		if err := json.Unmarshal(item, &id); err != nil {
			return nil, fmt.Errorf("entry %d is not a valid string: %w", i, err)
		}

		if id = strings.TrimSpace(id); id != "" {
			entries = append(entries, id)
		}
	}

	return entries, nil
}

// collect deduplicates the entries and classifies them.
func collect(entries []string) (List, error) {
	list := List{IDs: make([]string, 0, len(entries))}
	seen := make(map[string]struct{}, len(entries))

	for _, entry := range entries {
		if _, duplicate := seen[entry]; duplicate {
			list.Duplicates++
			continue
		}
		seen[entry] = struct{}{}

		list.IDs = append(list.IDs, entry)
		if !looksLikeTokenID(entry) {
			list.Suspicious = append(list.Suspicious, entry)
		}
	}

	if len(list.IDs) == 0 {
		return List{}, ErrEmpty
	}

	return list, nil
}

// looksLikeTokenID reports whether id has the shape of a Polymarket token id.
// This is advisory only: the authoritative answer comes from the API refusing
// to serve a book for it.
func looksLikeTokenID(id string) bool {
	if id == "" {
		return false
	}

	for i := 0; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
	}

	return true
}
