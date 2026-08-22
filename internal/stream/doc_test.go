package stream

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// docPath is the documentation this package is the source of truth for.
const docPath = "../../STREAM.md"

func readDoc(t *testing.T) string {
	t.Helper()

	contents, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("reading %s: %v", docPath, err)
	}

	return string(contents)
}

// documentedFields lists the field names the tables describe, written as
// "| `name` | type | meaning |".
func documentedFields(t *testing.T) []string {
	t.Helper()

	var names []string
	for _, line := range strings.Split(readDoc(t), "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		name, _, found := strings.Cut(strings.TrimPrefix(line, "| `"), "`")
		if found {
			names = append(names, name)
		}
	}
	slices.Sort(names)

	return slices.Compact(names)
}

// recordFields lists the JSON names a record can carry.
func recordFields(t *testing.T) []string {
	t.Helper()

	typ := reflect.TypeOf(record{})

	var names []string
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" {
			t.Errorf("record field %s has no json tag", typ.Field(i).Name)
			continue
		}
		names = append(names, strings.Split(tag, ",")[0])
	}
	slices.Sort(names)

	return names
}

// F2 applied to this file as much as to SCHEMA.md: a consumer reads it, and if
// it and the code disagree that is a bug in the scraper. Checking both
// directions is what makes that mechanical rather than aspirational.
func TestEveryFieldIsDocumented(t *testing.T) {
	documented := documentedFields(t)

	for _, name := range recordFields(t) {
		if !slices.Contains(documented, name) {
			t.Errorf("field %q can appear in a record but is not in %s", name, docPath)
		}
	}
}

func TestNothingIsDocumentedThatCannotAppear(t *testing.T) {
	fields := recordFields(t)

	for _, name := range documentedFields(t) {
		if !slices.Contains(fields, name) {
			t.Errorf("%s documents %q, which no record can carry", docPath, name)
		}
	}
}

// The kinds are the first thing a reader switches on, so an undocumented one is
// a line nobody knows how to handle.
func TestEveryKindIsDocumented(t *testing.T) {
	doc := readDoc(t)

	kinds := []string{KindHeader, KindQuote, KindTrade, KindFlag, KindMarket, KindResolved}
	for _, kind := range kinds {
		if !strings.Contains(doc, "`"+kind+"`") {
			t.Errorf("kind %q is not documented in %s", kind, docPath)
		}
	}
}

func TestTheVersionIsDocumented(t *testing.T) {
	if !strings.Contains(readDoc(t), "`"+Version+"`") {
		t.Errorf("%s does not state the version %q", docPath, Version)
	}
}

// The document is the contract and this is not. Anything reading the stream has
// to be told that, or it will treat a gap here as a statement.
func TestTheDocumentIsNamedAsTheContract(t *testing.T) {
	doc := readDoc(t)

	for _, phrase := range []string{"never a replacement", "The document is the contract", "SCHEMA.md"} {
		if !strings.Contains(doc, phrase) {
			t.Errorf("%s no longer says %q", docPath, phrase)
		}
	}
}

// The example is the part a reader is most likely to copy, so a line of it that
// does not parse would be copied too.
func TestTheExampleLinesParse(t *testing.T) {
	doc := readDoc(t)

	_, block, found := strings.Cut(doc, "```jsonl\n")
	if !found {
		t.Fatalf("%s has no example block", docPath)
	}
	block, _, _ = strings.Cut(block, "```")

	seen := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(block), "\n") {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Errorf("example line does not parse: %v\n %s", err, line)
			continue
		}

		kind, _ := decoded["kind"].(string)
		seen[kind] = true

		if decoded["at"] == nil {
			t.Errorf("example line has no timestamp:\n %s", line)
		}
		for name := range decoded {
			if !slices.Contains(recordFields(t), name) {
				t.Errorf("example line carries %q, which no record can:\n %s", name, line)
			}
		}
	}

	if len(seen) < 4 {
		t.Errorf("the example shows %d kinds, want most of them", len(seen))
	}
}
