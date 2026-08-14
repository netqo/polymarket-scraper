package report

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/netqo/polymarket-scraper/internal/tracker"
)

// schemaDocPath is the documentation this package is the source of truth for.
const schemaDocPath = "../../SCHEMA.md"

// documentedField matches a field name in one of the schema tables, which are
// written as "| `name` | type | meaning |".
var documentedField = regexp.MustCompile("(?m)^\\| `([a-z0-9_]+)` \\|")

func readSchemaDoc(t *testing.T) string {
	t.Helper()

	contents, err := os.ReadFile(schemaDocPath)
	if err != nil {
		t.Fatalf("reading %s: %v", schemaDocPath, err)
	}

	return string(contents)
}

func documentedFieldNames(t *testing.T) []string {
	t.Helper()

	var names []string
	for _, match := range documentedField.FindAllStringSubmatch(readSchemaDoc(t), -1) {
		names = append(names, match[1])
	}
	slices.Sort(names)

	return slices.Compact(names)
}

// F2: the schema is documented in one place a consumer can read, and if the
// documentation and the code disagree that is a bug in the scraper. Checking
// both directions is what makes that mechanical rather than aspirational.
func TestEveryFieldIsDocumented(t *testing.T) {
	documented := documentedFieldNames(t)

	for name := range schemaFieldNames(t) {
		if !slices.Contains(documented, name) {
			t.Errorf("field %q exists in the document but is not in %s", name, schemaDocPath)
		}
	}
}

func TestNothingIsDocumentedThatDoesNotExist(t *testing.T) {
	fields := schemaFieldNames(t)

	// Flag names and status values are documented in tables of their own; they
	// are values rather than fields, and they are checked separately below.
	values := allFlagNames()
	values = append(values, allStatusNames()...)

	for _, name := range documentedFieldNames(t) {
		if fields[name] || slices.Contains(values, name) {
			continue
		}
		t.Errorf("%s documents %q, which is neither a field, a flag, nor a status", schemaDocPath, name)
	}
}

// The flags are the extension point that keeps the status set closed, so an
// undocumented one is a value a consumer will see and not understand.
func TestEveryFlagIsDocumented(t *testing.T) {
	doc := readSchemaDoc(t)

	for _, flag := range allFlagNames() {
		if !strings.Contains(doc, "`"+flag+"`") {
			t.Errorf("flag %q is not documented in %s", flag, schemaDocPath)
		}
	}
}

func TestEveryStatusIsDocumented(t *testing.T) {
	doc := readSchemaDoc(t)

	for _, status := range allStatusNames() {
		if !strings.Contains(doc, "`"+status+"`") {
			t.Errorf("status %q is not documented in %s", status, schemaDocPath)
		}
	}
}

func TestSchemaVersionIsDocumented(t *testing.T) {
	if !strings.Contains(readSchemaDoc(t), "`"+SchemaVersion+"`") {
		t.Errorf("%s does not state the schema version %q", schemaDocPath, SchemaVersion)
	}
}

// allFlagNames lists every flag the tracker can emit. It is written out rather
// than derived, so adding a flag without documenting it fails here.
func allFlagNames() []string {
	flags := []tracker.Flag{
		tracker.FlagCrossedBook,
		tracker.FlagDeltaGap,
		tracker.FlagDeltaGapResynced,
		tracker.FlagDisconnected,
		tracker.FlagSnapshotOnly,
		tracker.FlagPreSnapshotDeltaDropped,
		tracker.FlagDuplicateDeltaDropped,
		tracker.FlagTimestampRegression,
		tracker.FlagDecodeError,
		tracker.FlagUnknownSide,
		tracker.FlagBestBidAskMismatch,
		tracker.FlagUnparsablePrice,
		tracker.FlagTokenNotFound,
		tracker.FlagTickSizeChanged,
		tracker.FlagMarketResolved,
		tracker.FlagDiscoveredMidWindow,
	}

	names := make([]string, len(flags))
	for i, flag := range flags {
		names[i] = string(flag)
	}

	return names
}

func allStatusNames() []string {
	return []string{
		string(tracker.StatusOK),
		string(tracker.StatusNoData),
		string(tracker.StatusSubscribeFailed),
		string(tracker.StatusResyncFailed),
	}
}
