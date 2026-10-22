package runstore

import (
	"strings"
	"testing"
)

// Summary turns a result into one readable line from its own fields.
func TestSummary(t *testing.T) {
	long := strings.Repeat("word ", 100)
	for _, tc := range []struct{ name, in, want string }{
		{"summary field", `{"tool":"Tern","summary":"A native Mac app."}`, "A native Mac app."},
		{"title field", `{"title":"Fix the parser","n":2}`, "Fix the parser"},
		{"first string field", `{"count":3,"tool":"Tern","note":"x"}`, "Tern"},
		{"records", `{"results":[{"tool":"T3","summary":"one"},{"tool":"Tern"}]}`, "2 results: one"},
		{"array", `[{"name":"a"},{"name":"b"},{"name":"c"}]`, "3 items: a"},
		{"plain text", "First line\nsecond line", "First line"},
		{"json string", `"just text"`, "just text"},
		{"number", `42`, "42"},
	} {
		if got := Summary(tc.in); got != tc.want {
			t.Errorf("%s: Summary = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := Summary(`{"summary":"` + long + `"}`); len([]rune(got)) > 201 || !strings.HasSuffix(got, "…") {
		t.Errorf("a long summary is not clipped: %q", got)
	}
}
