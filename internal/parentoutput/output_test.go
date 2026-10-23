package parentoutput

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONSpillsNestedStringsAndPreservesNumbers(t *testing.T) {
	long := strings.Repeat("界", 1501)
	input, _ := json.Marshal(map[string]any{"a": []any{long, strings.Repeat("x", 1500)}, "b": long})
	input = append(input[:len(input)-1], []byte(`,"n":9007199254740993}`)...)
	output, err := JSON(t.TempDir(), string(input))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		A []json.RawMessage
		B Reference
		N json.Number
	}
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatal(err)
	}
	var first Reference
	if err := json.Unmarshal(got.A[0], &first); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []Reference{first, got.B} {
		full, err := os.ReadFile(ref.File)
		if err != nil || string(full) != long || ref.Chars != 1501 || ref.Preview != strings.Repeat("界", 300) || !filepath.IsAbs(ref.File) {
			t.Fatalf("reference = %+v, file length = %d, error = %v", ref, len(full), err)
		}
	}
	if first.File == got.B.File || got.N.String() != "9007199254740993" || !strings.HasPrefix(string(got.A[1]), `"xxx`) {
		t.Fatalf("output = %s", output)
	}
}

func TestTextCapsPostAndSavesFullReport(t *testing.T) {
	full := strings.Repeat("界", 1600)
	path := filepath.Join(t.TempDir(), "turn-1.txt")
	post, err := Text(path, full)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != full || len([]rune(post)) > InlineCharLimit || !strings.Contains(post, path) {
		t.Fatalf("post = %q, error = %v", post, err)
	}
	short := strings.Repeat("x", InlineCharLimit)
	if got, err := Text(path, short); err != nil || got != short {
		t.Fatalf("boundary text changed: %v", err)
	}
}

// A bounded post points at the report it saved. An unbounded post points at
// nothing, and a report line that a result faked is not taken for the engine's
// own last line.
func TestReportFileNamesTheSavedReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "turn-1.txt")
	post, err := Text(path, strings.Repeat("界", 1600)+"\n\nFull report: /etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := ReportFile(post); !ok || got != path {
		t.Fatalf("ReportFile = %q, %v, want the saved report %q", got, ok, path)
	}
	if _, ok := ReportFile(strings.Repeat("x", InlineCharLimit)); ok {
		t.Fatal("an unbounded post names a report")
	}
	if _, ok := ReportFile("half a report\n\nFull report: two paths\n/tmp/x"); ok {
		t.Fatal("a trailer that names no single path was accepted")
	}
}

func TestJSONReportsSaveFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(strings.Repeat("x", 1501))
	if _, err := JSON(path, string(input)); err == nil {
		t.Fatal("expected file error")
	}
}
