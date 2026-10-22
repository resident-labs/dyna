package links

import (
	prstate "dyna-agent/internal/pr/state"
	"os"
	"path/filepath"
	"testing"
)

func TestConsolidationPreservesReviewerSettingsAndReviewHistory(t *testing.T) {
	root := legacyRoot(t)
	raw := `{"version":1,"records":{"active":{"roomId":"room","state":"open","watching":true,"authorSession":"author","reviewerSession":"reviewer","reviewHead":"head"}},"reviewers":{"room":{"profile":"judge"}}}`
	if err := os.WriteFile(filepath.Join(root, "index.json"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	absorbed := map[string]bool{"reviewer": true, "author": true}
	if err := Consolidate(root, "room", "target", absorbed); err != nil {
		t.Fatal(err)
	}
	st, err := prstate.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	r := st.Records["active"]
	if r.Author != "target" || r.Reviewer != "" || r.RoundHead != "" || len(r.ReviewHistory) != 1 || st.Reviewers["room"].Profile != "judge" {
		t.Fatalf("consolidation: %+v %+v", r, st.Reviewers["room"])
	}
	first, _ := prstate.Revision(root)
	if err := Consolidate(root, "room", "target", absorbed); err != nil {
		t.Fatal(err)
	}
	second, _ := prstate.Revision(root)
	if first != second {
		t.Fatal("consolidation is not idempotent")
	}
}

func TestConsolidationMovesOpenPRAuthorsRegardlessOfWatch(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		watching    bool
		owner       string
	}{
		{"watched", "open", true, "kept"},
		{"stopped watch", "open", false, "kept"},
		{"closed history", "closed", false, "source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := legacyRoot(t)
			r := &prstate.Record{Key: "pr", RoomID: "room", Author: "source", Parent: "source", State: tc.state, Watching: tc.watching,
				Notices: []prstate.Notice{{ID: "pending", Target: "source"}},
			}
			if err := prstate.Write(root, prstate.State{Version: 1, Records: map[string]*prstate.Record{"pr": r}}); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := Consolidate(root, "room", "kept", map[string]bool{"source": true}); err != nil {
					t.Fatal(err)
				}
			}
			st, err := prstate.Read(root)
			if err != nil {
				t.Fatal(err)
			}
			got := st.Records["pr"]
			if got.Author != tc.owner || got.Parent != tc.owner || got.Notices[0].Target != tc.owner || got.Watching != tc.watching || got.State != tc.state {
				t.Fatalf("ownership or watch state changed incorrectly: %+v", got)
			}
			if tc.state == "open" && (len(got.FormerAuthors) != 1 || got.FormerAuthors[0] != "source") {
				t.Fatalf("author history not preserved: %v", got.FormerAuthors)
			}
		})
	}
}
