package tokenlist

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A real token id, for tests that care about the shape rather than the value.
const realID = "71321045679252212594626385532706912750332728571942532289631379312455583992563"

func TestParseLineFormat(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "one per line",
			in:   "111\n222\n333\n",
			want: []string{"111", "222", "333"},
		},
		{
			name: "no trailing newline",
			in:   "111\n222",
			want: []string{"111", "222"},
		},
		{
			name: "windows line endings",
			in:   "111\r\n222\r\n",
			want: []string{"111", "222"},
		},
		{
			name: "blank and whitespace-only lines are skipped",
			in:   "111\n\n   \n\t\n222\n",
			want: []string{"111", "222"},
		},
		{
			name: "surrounding whitespace is trimmed",
			in:   "  111  \n\t222\t\n",
			want: []string{"111", "222"},
		},
		{
			name: "comments are skipped",
			in:   "# the shortlist\n111\n  # indented comment\n222\n",
			want: []string{"111", "222"},
		},
		{
			name: "utf-8 byte order mark is stripped",
			in:   "\ufeff111\n222\n",
			want: []string{"111", "222"},
		},
		{
			name: "a real token id survives intact",
			in:   realID + "\n",
			want: []string{realID},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.in))
			if err != nil {
				t.Fatalf("Parse returned error: %v", err)
			}
			if !slices.Equal(got.IDs, tt.want) {
				t.Errorf("IDs = %v, want %v", got.IDs, tt.want)
			}
		})
	}
}

func TestParseJSONArrayFormat(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"compact", `["111","222"]`, []string{"111", "222"}},
		{"indented", "[\n  \"111\",\n  \"222\"\n]", []string{"111", "222"}},
		{"leading whitespace", "  \n[\"111\"]", []string{"111"}},
		{"entries are trimmed", `[" 111 ","222"]`, []string{"111", "222"}},
		{"real token id", `["` + realID + `"]`, []string{realID}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.in))
			if err != nil {
				t.Fatalf("Parse returned error: %v", err)
			}
			if !slices.Equal(got.IDs, tt.want) {
				t.Errorf("IDs = %v, want %v", got.IDs, tt.want)
			}
		})
	}
}

// A JSON array of numbers means the caller's token ids already lost precision
// before they reached us: a 77-digit id cannot survive a float64. Failing loudly
// is the only safe response, because the ids would look plausible and be wrong.
func TestParseJSONArrayRejectsNonStrings(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"numbers", `[111, 222]`},
		{"mixed", `["111", 222]`},
		{"nested arrays", `[["111"]]`},
		{"objects", `[{"token_id":"111"}]`},
		{"null entry", `["111", null]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.in))
			if err == nil {
				t.Fatalf("Parse(%s) succeeded, want an error", tt.in)
			}
			if !strings.Contains(err.Error(), "string") {
				t.Errorf("error %q does not explain that entries must be strings", err)
			}
		})
	}
}

// C4 requires every requested token to appear exactly once in the output, so
// duplicates are collapsed here, at the one place that defines "requested".
func TestParseDeduplicatesPreservingFirstOccurrence(t *testing.T) {
	got, err := Parse([]byte("222\n111\n222\n333\n111\n"))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	want := []string{"222", "111", "333"}
	if !slices.Equal(got.IDs, want) {
		t.Errorf("IDs = %v, want %v", got.IDs, want)
	}
	if got.Duplicates != 2 {
		t.Errorf("Duplicates = %d, want 2", got.Duplicates)
	}
}

// H2: a malformed token id must reach the output with a failure status, so it
// must not be rejected at load time. It is reported as suspicious instead.
func TestParseKeepsMalformedIDsAndFlagsThem(t *testing.T) {
	got, err := Parse([]byte("111\nnot-a-token\n0x1234\n222\n"))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	want := []string{"111", "not-a-token", "0x1234", "222"}
	if !slices.Equal(got.IDs, want) {
		t.Errorf("IDs = %v, want %v", got.IDs, want)
	}

	wantSuspicious := []string{"not-a-token", "0x1234"}
	if !slices.Equal(got.Suspicious, wantSuspicious) {
		t.Errorf("Suspicious = %v, want %v", got.Suspicious, wantSuspicious)
	}
}

func TestParseRejectsEmptyInput(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"whitespace only", "  \n\n\t\n"},
		{"comments only", "# nothing here\n# really\n"},
		{"empty json array", `[]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(tt.in)); !errors.Is(err, ErrEmpty) {
				t.Fatalf("Parse(%q) error = %v, want ErrEmpty", tt.in, err)
			}
		})
	}
}

func TestParseRejectsMalformedJSON(t *testing.T) {
	if _, err := Parse([]byte(`["111",`)); err == nil {
		t.Fatal("Parse of truncated JSON succeeded, want an error")
	}
}

// A3: the format must handle at least 500 ids, comfortably above the 400-token
// default the consuming agent uses.
func TestParseHandlesLargeLists(t *testing.T) {
	const count = 500

	var lines strings.Builder
	for i := range count {
		lines.WriteString(strconv.Itoa(i))
		lines.WriteByte('\n')
	}

	got, err := Parse([]byte(lines.String()))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(got.IDs) != count {
		t.Errorf("len(IDs) = %d, want %d", len(got.IDs), count)
	}
}

func TestLoadReadsFromDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.txt")

	if err := os.WriteFile(path, []byte("111\n222\n"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !slices.Equal(got.IDs, []string{"111", "222"}) {
		t.Errorf("IDs = %v, want [111 222]", got.IDs)
	}
}

func TestLoadReportsAMissingFileClearly(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.txt"))
	if err == nil {
		t.Fatal("Load of a missing file succeeded, want an error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error %v does not unwrap to os.ErrNotExist", err)
	}
}
