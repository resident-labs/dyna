package state

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseIssueRefsAndFixLines(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want string
		err  bool
	}{
		{name: "both trackers", text: "D-5,#207", want: "D-5, #207"},
		{name: "spaces and repeats", text: "d-5 d5 D5 , #207", want: "D-5, #207"},
		{name: "bare number is ambiguous", text: "207", err: true},
		{name: "unknown tracker", text: "GH-7", err: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseIssueRefs(tc.text)
			if tc.err {
				if err == nil {
					t.Fatalf("ParseIssueRefs(%q) = %v, want an error", tc.text, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if FixesLabel(got) != tc.want {
				t.Errorf("ParseIssueRefs(%q) = %s, want %s", tc.text, FixesLabel(got), tc.want)
			}
		})
	}

	body := "This fixes the parser.\n\nFixes D-5, #207\ncloses: D-6\nFixer D-9 stays prose\nresolves #208\n- Fixes D-7.\n* Closes (D-8)"
	got := ParseFixLines(body)
	if want := "D-5, #207, D-6, #208, D-7, D-8"; FixesLabel(got) != want {
		t.Errorf("ParseFixLines = %s, want %s", FixesLabel(got), want)
	}

	// D-281: a negated sentence after the closing reference links nothing.
	negated := "Closes D-220 on merge. This does not close D-91 or D-92.\nFixes D-1 and D-2. Not D-3.\nFixes #4 but not D-5"
	if got, want := FixesLabel(ParseFixLines(negated)), "D-220, D-1, D-2, #4"; got != want {
		t.Errorf("ParseFixLines on negated prose = %s, want %s", got, want)
	}

	// One change may not close an unbounded list.
	var many []string
	for i := 1; i <= MaxFixes+1; i++ {
		many = append(many, fmt.Sprintf("#%d", i))
	}
	if _, err := ParseIssueRefs(strings.Join(many, ",")); err == nil {
		t.Error("ParseIssueRefs accepted more issues than the limit")
	}
	var lines []string
	for i := 1; i <= MaxFixes+1; i++ {
		lines = append(lines, fmt.Sprintf("Fixes D-%d", i))
	}
	if n := len(ParseFixLines(strings.Join(lines, "\n"))); n != MaxFixes {
		t.Errorf("ParseFixLines kept %d issues, want the limit of %d", n, MaxFixes)
	}
}
