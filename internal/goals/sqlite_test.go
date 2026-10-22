package goals

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// legacyStore returns a store whose goals/state.json holds data.
func legacyStore(t *testing.T, data []byte) *Store {
	t.Helper()
	s := &Store{Root: filepath.Join(t.TempDir(), "goals")}
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Root, "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return s
}

func counts(st State) map[string]map[string]int {
	out := map[string]map[string]int{}
	for id, r := range st.Rooms {
		out[id] = map[string]int{}
		for _, g := range r.Goals {
			out[id][g.State]++
		}
	}
	return out
}

func TestImportCopiesSampleState(t *testing.T) {
	data, err := os.ReadFile("testdata/state-sample.json")
	if err != nil {
		t.Fatal(err)
	}
	s := legacyStore(t, data)
	st, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	got := counts(st)
	if len(got) != 2 || got["room_a"]["done"] != 1 || got["room_a"]["active"] != 2 || got["room_a"]["dropped"] != 1 || got["room_b"]["done"] != 1 {
		t.Fatalf("counts = %v", got)
	}
	a := st.Rooms["room_a"]
	g := a.Goals["g2"]
	// g2's parent g1 is closed, so the fold migration keeps it standalone;
	// fold_test asserts no body keeps a parent field.
	if g.Text != "Fix the lexer." || g.Detail != "Own internal/lex." || g.Setter != "ses_lead" || len(g.Holders) != 1 || g.Holders[0] != "ses_worker" ||
		!g.WaitUntil.Equal(time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)) || len(g.Edits) != 1 || g.Edits[0].By != "ses_worker" {
		t.Fatalf("g2 = %+v", g)
	}
	if done := a.Goals["g1"].Done; done == nil || done.Summary != "Merged." {
		t.Fatalf("g1 outcome = %+v", done)
	}
	if a.NextID != 4 || a.NextRuleID != 1 || a.Rules["r1"].Text != "Keep tests fast." || len(a.Rules["r1"].Edits) != 1 {
		t.Fatalf("room = %+v", a.RuleSet)
	}
	if a.Keeper.Session != "ses_keeper" || a.Keeper.Assignments != 2 || len(a.Keeper.FailedProfiles) != 1 {
		t.Fatalf("keeper = %+v", a.Keeper)
	}
	if len(a.Notices) != 1 || a.Notices[0].Kind != "holder_closed" || a.Notices[0].To != "ses_lead" {
		t.Fatalf("notices = %+v", a.Notices)
	}
	if st.Global == nil || st.Global.Rules["u1"].Text != "One implementation per job." {
		t.Fatalf("global = %+v", st.Global)
	}
	if rules, err := s.Rules(Global); err != nil || len(rules) != 1 {
		t.Fatal(rules, err)
	}
}

func TestImportRunsOnceAndNeverChangesStateFile(t *testing.T) {
	data, err := os.ReadFile("testdata/state-sample.json")
	if err != nil {
		t.Fatal(err)
	}
	s := legacyStore(t, data)
	if _, err := s.Add("room_a", "owner", Input{Text: "New work"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finish("room_a", "g2", "owner", "Done.", false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root, "state.json")
	if after, _ := os.ReadFile(path); !bytes.Equal(after, data) {
		t.Fatal("a write changed state.json")
	}
	// A later change to state.json, such as an older binary's write, is not imported again.
	if err := os.WriteFile(path, []byte(`{"version":1,"rooms":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := (&Store{Root: s.Root}).Read()
	if err != nil {
		t.Fatal(err)
	}
	r := st.Rooms["room_a"]
	if r == nil || len(r.Goals) != 5 || r.Goals["g5"].Text != "New work" || r.Goals["g2"].State != "done" {
		t.Fatalf("room_a = %+v", r)
	}
}

func TestImportRejectsInvalidState(t *testing.T) {
	for _, data := range []string{`{`, `{"version":2}`} {
		if _, err := legacyStore(t, []byte(data)).Read(); err == nil {
			t.Fatalf("imported %s", data)
		}
	}
}

func TestRevisionMovesOnlyWhenRowsChange(t *testing.T) {
	s := testStore(t)
	if _, err := s.Add("r", "owner", Input{Text: "Ship"}); err != nil {
		t.Fatal(err)
	}
	before, err := s.Revision()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(st *State) error { st.Room("r"); return nil }); err != nil {
		t.Fatal(err)
	}
	if now, _ := s.Revision(); now != before {
		t.Fatalf("no-op update moved the revision from %s to %s", before, now)
	}
	if _, err := s.Finish("r", "g1", "owner", "Shipped.", false); err != nil {
		t.Fatal(err)
	}
	if now, _ := s.Revision(); now == before {
		t.Fatal("a change did not move the revision")
	}
}

func TestUpdateDeletesRemovedRows(t *testing.T) {
	s := testStore(t)
	if _, err := s.Add("r", "owner", Input{Text: "Ship"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(st *State) error { delete(st.Rooms, "r"); return nil }); err != nil {
		t.Fatal(err)
	}
	if st, err := s.Read(); err != nil || len(st.Rooms) != 0 {
		t.Fatal(st.Rooms, err)
	}
}

func TestScopedWritesKeepClosedGoals(t *testing.T) {
	s := testStore(t)
	parent, err := s.Add("r", "owner", Input{Text: "Parent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finish("r", parent.ID, "owner", "Done.", false); err != nil {
		t.Fatal(err)
	}
	// A scoped edit keeps the closed goal although it loads only open goals.
	child, err := s.Add("r", "owner", Input{Text: "Child"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateOpen(func(st *State) error {
		if st.Rooms["r"].Goals[parent.ID] != nil {
			t.Error("UpdateOpen loaded a closed goal")
		}
		st.Room("r").NextID++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	all, err := s.List("r")
	if err != nil || len(all) != 2 || all[0].State != "done" || all[1].ID != child.ID {
		t.Fatal(all, err)
	}
	if open, err := s.ListOpen("r"); err != nil || len(open) != 1 || open[0].ID != child.ID {
		t.Fatal(open, err)
	}
	if st, _ := s.Read(); st.Rooms["r"].NextID != 3 {
		t.Fatalf("next ID = %d", st.Rooms["r"].NextID)
	}
}

func TestReleasedAtTracksAllHolderRemovalPaths(t *testing.T) {
	for _, change := range []string{"close", "transfer", "session-close"} {
		t.Run(change, func(t *testing.T) {
			s := testStore(t)
			now := s.now()
			s.Now = func() time.Time { return now }
			g, err := s.Add("r", "owner", Input{Text: "Ship", Holders: []string{"worker"}})
			if err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Minute)
			switch change {
			case "close":
				_, err = s.Finish("r", g.ID, "owner", "Done", false)
			case "transfer":
				holders := []string{"other"}
				_, err = s.Edit("r", g.ID, "owner", "", &holders, false)
			case "session-close":
				err = s.CloseSession("r", "worker", "other", map[string]bool{"other": true}, "")
			}
			if err != nil {
				t.Fatal(err)
			}
			st, err := (&Store{Root: s.Root}).ReadOpen()
			if err != nil {
				t.Fatal(err)
			}
			released := st.Room("r").ReleasedAt
			if !released["worker"].Equal(now) || !released["other"].IsZero() {
				t.Fatalf("release times after %s: %+v", change, released)
			}
			// Text edits must not move a finished release.
			now = now.Add(time.Minute)
			if change != "close" {
				if _, err := s.Edit("r", g.ID, "owner", "New text", nil, false); err != nil {
					t.Fatal(err)
				}
			}
			st, err = s.ReadOpen()
			if err != nil {
				t.Fatal(err)
			}
			if got := st.Room("r").ReleasedAt["worker"]; !got.Equal(now.Add(-time.Minute)) {
				t.Fatalf("unrelated update moved release: %v", got)
			}
		})
	}
}
