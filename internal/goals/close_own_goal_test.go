package goals

import (
	"strings"
	"testing"
)

func TestClosingSoleHolderDropsItsGoalWithANote(t *testing.T) {
	for _, heir := range []string{"", "parent"} {
		t.Run("heir="+heir, func(t *testing.T) {
			s := lifecycleStore(t)
			g, err := s.Add("r", "child", Input{Text: "Record this review verdict", Holders: []string{"child"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.CloseSession("r", "child", heir, map[string]bool{"parent": true}); err != nil {
				t.Fatal(err)
			}
			got, err := s.Get("r", g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != "dropped" || got.Shared || got.Done == nil || !strings.Contains(got.Done.Summary, "child") {
				t.Fatalf("closed sole holder left live or unexplained work: %+v", got)
			}
		})
	}
}

func TestClosingOneOfSeveralHoldersKeepsOtherHolder(t *testing.T) {
	s := lifecycleStore(t)
	g, err := s.Add("r", "owner", Input{Text: "Shared implementation", Holders: []string{"child", "other"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CloseSession("r", "child", "", nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("r", g.ID)
	if err != nil || !got.Open() || !got.HeldBy("other") || got.HeldBy("child") {
		t.Fatalf("remaining holder lost work: %+v %v", got, err)
	}
}

func TestClosingAssignedHolderReturnsToSetterOrHeir(t *testing.T) {
	for _, tc := range []struct {
		name        string
		setterLive  bool
		heir        string
		wantHolder  string
		wantDropped bool
	}{
		{"live setter", true, "another-heir", "setter", false},
		{"live heir only", false, "another-heir", "another-heir", false},
		{"nobody left", false, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := lifecycleStore(t)
			g, err := s.Add("r", "setter", Input{Text: "Delegated task", Holders: []string{"child"}})
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := s.CloseSession("r", "child", tc.heir, map[string]bool{"setter": tc.setterLive, "another-heir": true}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.Get("r", g.ID)
			if err != nil {
				t.Fatal(err)
			}
			// A goal never waits for the owner: it moves to a live session or
			// closes with a note.
			if tc.wantDropped {
				if got.State != "dropped" || got.Done == nil || len(got.Holders) != 0 || got.Shared {
					t.Fatalf("goal left unheld for the owner: %+v", got)
				}
				return
			}
			if !got.Open() || got.Shared || !got.HeldBy(tc.wantHolder) {
				t.Fatalf("incorrect handback: %+v", got)
			}
		})
	}
}
