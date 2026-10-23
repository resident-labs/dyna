package goals

import (
	"fmt"
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

func TestClosingAssignedHolderReturnsToSetterOrRootOnce(t *testing.T) {
	for _, setterLive := range []bool{true, false} {
		t.Run(fmt.Sprint(setterLive), func(t *testing.T) {
			s := lifecycleStore(t)
			g, err := s.Add("r", "setter", Input{Text: "Delegated task", Holders: []string{"child"}})
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := s.CloseSession("r", "child", "another-heir", map[string]bool{"setter": setterLive, "another-heir": true}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.Get("r", g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Open() || got.Shared || (setterLive && !got.HeldBy("setter")) || (!setterLive && len(got.Holders) != 0) {
				t.Fatalf("incorrect handback: %+v", got)
			}
		})
	}
}
