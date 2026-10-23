package watch

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/pr/facts"
)

func sendTwoComments(t *testing.T, e *testEnv) Record {
	t.Helper()
	r, _ := localChange(t, e)
	comments := []DiffComment{
		{File: "code.txt", Line: 3, Quote: "+x := a[i]", Text: "Check the index"},
		{File: "main.go", Line: 12, Quote: " return nil", Text: "Why nil?"},
	}
	if err := e.s.SendDiffComments(context.Background(), r.Key, comments); err != nil {
		t.Fatal(err)
	}
	r, _ = e.s.Store.Get(r.Key)
	return r
}

func TestSendDiffCommentsMakesOneNoticeAndOwnerFindings(t *testing.T) {
	e := newEnv(t)
	r := sendTwoComments(t, e)
	if len(r.Findings) != 2 || r.Findings[0].ID != "o1" || r.Findings[1].ID != "o2" || r.Findings[0].Severity != "owner" || r.Findings[0].Patch != "+x := a[i]" {
		t.Fatalf("findings: %+v", r.Findings)
	}
	pending := 0
	for _, n := range r.Notices {
		if !n.Done && n.Target == r.Author {
			pending++
			if !strings.Contains(n.Text, "code.txt:3") || !strings.Contains(n.Text, "main.go:12") || !strings.Contains(n.Text, "Check the index") {
				t.Fatalf("notice: %s", n.Text)
			}
		}
	}
	if pending != 1 {
		t.Fatalf("want one notice, got %d: %+v", pending, r.Notices)
	}
	if err := e.s.SendDiffComments(context.Background(), r.Key, []DiffComment{{File: "a.go", Line: 1, Text: "More"}}); err != nil {
		t.Fatal(err)
	}
	r, _ = e.s.Store.Get(r.Key)
	if r.Findings[2].ID != "o3" {
		t.Fatalf("ids: %+v", r.Findings)
	}
}

func TestOwnerFindingStaysOpenUntilAnswered(t *testing.T) {
	for _, tc := range []struct {
		actor, disposition string
		open               bool
	}{
		{"author", "question", true},
		{"author", "fixed", false},
		{"author", "wont-fix", false},
		{"", "resolved", false},
	} {
		e := newEnv(t)
		r := sendTwoComments(t, e)
		if !facts.OpenOwnerFinding(r) {
			t.Fatal("new owner comment is not open")
		}
		by := ""
		if tc.actor == "author" {
			by = r.Author
		}
		for _, id := range []string{"o1", "o2"} {
			if err := e.s.ReplyFinding(context.Background(), r.Key, r.Author, by, id, tc.disposition, "ok"); err != nil {
				t.Fatalf("%s: %v", tc.disposition, err)
			}
		}
		r, _ = e.s.Store.Get(r.Key)
		if got := facts.OpenOwnerFinding(r); got != tc.open {
			t.Fatalf("%s: open = %v", tc.disposition, got)
		}
	}
}

func TestReviewVerdictKeepsOwnerComments(t *testing.T) {
	e := newEnv(t)
	r := sendTwoComments(t, e)
	e.tick()
	r, _ = e.s.Store.Get(r.Key)
	findings := []Finding{{Severity: "blocking", File: "code.txt", Line: 1, Why: "Bounds", Fix: "Check"}}
	if err := e.s.VerdictWithFindings(context.Background(), r.Key, r.Reviewer, r.Head, "changes", "Fix", 0, findings); err != nil {
		t.Fatal(err)
	}
	r, _ = e.s.Store.Get(r.Key)
	if len(r.Findings) != 3 || r.Findings[2].ID != "o2" || !facts.OpenOwnerFinding(r) {
		t.Fatalf("findings: %+v", r.Findings)
	}
}

func TestMergeQueueShowsOwnerCommentReason(t *testing.T) {
	e := newEnv(t)
	r := sendTwoComments(t, e)
	st, _ := e.s.Store.Read()
	if st.RepositoryFacts == nil {
		st.RepositoryFacts = map[string]RepositoryFacts{}
	}
	st.RepositoryFacts[defaultRepo(r)] = RepositoryFacts{MergeTrialCI: true}
	rows := MergeQueueSnapshot(st)
	if len(rows) != 1 || rows[0].Reason != "waiting on replies to owner comments" {
		t.Fatalf("rows: %+v", rows)
	}
}
