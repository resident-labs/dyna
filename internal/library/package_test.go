package library

import (
	"slices"
	"testing"
)

func TestPackageOwners(t *testing.T) {
	entries := []Entry{
		{ID: "prs", Name: "Pull requests", Kind: "feature", Summary: "One. Two.", Owns: []string{"internal/pr/**"}},
		{ID: "cli", Name: "CLI", Kind: "feature", Summary: "One. Two.", Owns: []string{"internal/cli/**"}},
		{ID: "web", Name: "Web app", Kind: "feature", Summary: "One. Two.", Owns: []string{"test/src/lib/**"}},
		{ID: "retired", Name: "Old", Kind: "feature", Summary: "One. Two.", Owns: []string{"internal/old/**"}, Retired: true},
	}
	for _, tc := range []struct {
		pkg  string
		want []string
	}{
		{"internal/pr/watch", []string{"prs"}},
		{"dyna-agent/internal/pr/watch", []string{"prs"}}, // a Go report may carry the module
		{"internal/cli/pr", []string{"cli"}},
		{"test/src/lib/prs/mergeQueueScreen.test.js", []string{"web"}},
		{"internal/old/thing", nil},
		{"unmapped/thing", nil},
		{"", nil},
	} {
		if got := PackageOwners(entries, tc.pkg); !slices.Equal(got, tc.want) {
			t.Errorf("PackageOwners(%q) = %q, want %q", tc.pkg, got, tc.want)
		}
	}
}
