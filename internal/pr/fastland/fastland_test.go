package fastland

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		area  Area
		ok    bool
	}{
		{"docs markdown", []string{"README.md", "docs/pr-tracking.md"}, Docs, true},
		{"docs folder", []string{"docs/design/events.txt"}, Docs, true},
		{"guide text", []string{"internal/guidance/root.md", "internal/guidance/skill_body.md"}, Guide, true},
		{"guide code is not text", []string{"internal/guidance/topics.go"}, "", false},
		{"a rename keeps both paths", []string{"docs/notes.md", "internal/room/events.go"}, "", false},
		{"tests", []string{"internal/pr/watch/merge_test.go", "web/src/lib/prs/prCard.test.ts"}, Tests, true},
		{"web test folder", []string{"web/tests/sidebar.ts"}, Tests, true},
		{"mixed docs and tests needs the test checks", []string{"README.md", "internal/x_test.go"}, Tests, true},
		{"one code path is not low risk", []string{"README.md", "internal/room/events.go"}, "", false},
		{"go.mod is not low risk", []string{"go.mod"}, "", false},
		{"go.sum is not low risk", []string{"go.sum"}, "", false},
		{"a generated file is not low risk", []string{"web/src/lib/rooms/events.gen.ts"}, "", false},
		{"a generated markdown file is not low risk", []string{"docs/generated/notes.md"}, "", false},
		{"a source file is not low risk", []string{"internal/pr/state/types.go"}, "", false},
		{"an absolute path is not low risk", []string{"/etc/passwd"}, "", false},
		{"a path escaping the repo is not low risk", []string{"../secrets.md"}, "", false},
		{"an empty change is not low risk", nil, "", false},
		{"a dot path is not low risk", []string{"."}, "", false},
		{"a path with no extension is not low risk", []string{"Makefile"}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			area, ok := Classify(c.paths)
			if ok != c.ok || area != c.area {
				t.Fatalf("Classify(%v) = %q, %v; want %q, %v", c.paths, area, ok, c.area, c.ok)
			}
		})
	}
}
