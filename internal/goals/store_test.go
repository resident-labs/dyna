package goals

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Root: filepath.Join(t.TempDir(), "goals"), Now: func() time.Time { return time.Date(2026, 10, 17, 12, 0, 0, 0, time.Local) }}
}
func TestStoreLifecycle(t *testing.T) {
	s := testStore(t)
	g, err := s.Add("r", "owner", Input{Text: "Ship the relay", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	if g.ID != "g1" || g.State != "active" {
		t.Fatal(g)
	}
	child, err := s.Add("r", "agent", Input{Text: "Test reconnect", Holders: []string{"agent", "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(child.Holders) != 1 {
		t.Fatal(child)
	}
	for _, drop := range []bool{false, true} {
		g, err = s.Finish("r", g.ID, "agent", "Shipped", drop)
		if err != nil {
			t.Fatal(err)
		}
		if g.Open() || g.Done.By != "agent" {
			t.Fatal(g)
		}
		g, err = s.Edit("r", g.ID, "owner", "Ship version two", nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if !g.Open() || g.Done != nil || len(g.Edits) == 0 {
			t.Fatal(g)
		}
	}
	loaded := &Store{Root: s.Root}
	got, err := loaded.Get("r", g.ID)
	if err != nil || got.Text != g.Text {
		t.Fatal(got, err)
	}
	if _, err := s.Get("other", g.ID); err == nil {
		t.Fatal("room isolation")
	}
}

func TestRestingGoalLoadsAsActive(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if err := os.WriteFile(filepath.Join(s.Root, "state.json"), []byte(`{"version":1,"rooms":{"r":{"goals":{"g1":{"id":"g1","state":"resting"}}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	g, err := s.Get("r", "g1")
	if err != nil || g.State != "active" || !g.Open() {
		t.Fatal(g, err)
	}
}

func TestValidation(t *testing.T) {
	s := testStore(t)
	for _, text := range []string{" ", strings.Repeat("界", 2001)} {
		if _, err := s.Add("r", "owner", Input{Text: text}); err == nil {
			t.Fatal("accepted invalid text")
		}
	}
	// A room holds any number of open goals: the old cap blocked new agents.
	for i := 0; i < 51; i++ {
		if _, err := s.Add("r", "owner", Input{Text: "goal"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Finish("r", "g1", "owner", "", true); err != nil {
		t.Fatal(err)
	}
	if g, err := s.Add("r", "owner", Input{Text: "new"}); err != nil || g.ID != "g52" {
		t.Fatal(g, err)
	}
	if _, err := s.Edit("r", "g1", "owner", "reopen", nil, false); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentIDs(t *testing.T) {
	s := testStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Add("r", "owner", Input{Text: "goal"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	all, err := s.List("r")
	if err != nil || len(all) != 20 {
		t.Fatal(len(all), err)
	}
	seen := map[string]bool{}
	for _, g := range all {
		if seen[g.ID] {
			t.Fatal("reused ID")
		}
		seen[g.ID] = true
	}
}
func TestCloseSession(t *testing.T) {
	s := testStore(t)
	g, _ := s.Add("r", "setter", Input{Text: "goal", Holders: []string{"holder"}})
	if err := s.CloseSession("r", "holder", "", map[string]bool{"setter": true}); err != nil {
		t.Fatal(err)
	}
	g, _ = s.Get("r", g.ID)
	if !g.HeldBy("setter") {
		t.Fatal(g)
	}
	if err := s.CloseSession("r", "setter", "parent", map[string]bool{"parent": true}); err != nil {
		t.Fatal(err)
	}
	g, _ = s.Get("r", g.ID)
	if g.State != "dropped" || g.Done == nil || g.Shared {
		t.Fatal("setter closed its own returned goal without dropping it", g)
	}
}
func TestCloseSessionReturnsAssignedGoalToSetterBeforeHeir(t *testing.T) {
	s := testStore(t)
	g, _ := s.Add("r", "setter", Input{Text: "goal", Holders: []string{"absorbed"}})
	if err := s.CloseSession("r", "absorbed", "kept", map[string]bool{"setter": true, "kept": true}); err != nil {
		t.Fatal(err)
	}
	if g, _ = s.Get("r", g.ID); g.HeldBy("kept") || !g.HeldBy("setter") || g.Setter != "setter" {
		t.Fatal(g)
	}
}
func TestFrame(t *testing.T) {
	s := testStore(t)
	if f := Frame(nil, "a", nil, nil); !strings.Contains(f, "None open for you") || !strings.Contains(f, "dyna goal --for") {
		t.Fatalf("no-goal frame should nudge toward goals: %q", f)
	}
	for i := 0; i < 8; i++ {
		s.Add("r", "owner", Input{Text: fmt.Sprintf("Goal %d %s", i, strings.Repeat("界", 180)), Holders: []string{"a"}})
	}
	all, _ := s.List("r")
	frame := Frame(all, "a", nil, nil)
	if len(strings.Split(frame, "\n")) != 5 || !strings.Contains(frame, "+5 more: dyna goal") {
		t.Fatal(frame)
	}
	if strings.Contains(Frame(all, "other", nil, nil), "Goal ") {
		t.Fatal("unrelated goals")
	}
	if len([]rune(Clip(all[0].Text, 160))) != 160 {
		t.Fatal("unicode clipping")
	}
}

func TestFrameHidesDoneGoalsAndListsHeldFirst(t *testing.T) {
	s := testStore(t)
	for i := 0; i < 4; i++ {
		g, _ := s.Add("r", "a", Input{Text: fmt.Sprintf("Done %d", i), Holders: []string{"b"}})
		if _, err := s.Finish("r", g.ID, "a", "finished", false); err != nil {
			t.Fatal(err)
		}
	}
	s.Add("r", "a", Input{Text: "Child work", Holders: []string{"b"}})
	s.Add("r", "owner", Input{Text: "My work", Holders: []string{"a"}})
	all, _ := s.List("r")
	frame := Frame(all, "a", nil, nil)
	if strings.Contains(frame, "Done ") {
		t.Fatalf("done goals listed: %q", frame)
	}
	lines := strings.Split(frame, "\n")
	if len(lines) != 3 || !strings.Contains(lines[1], "My work") || !strings.Contains(lines[2], "Child work") {
		t.Fatalf("want held goal first, then set goal: %q", frame)
	}
}

func TestGoalConcerns(t *testing.T) {
	for _, tc := range []struct {
		name string
		g    Goal
		want bool
	}{
		{"holder", Goal{Holders: []string{"a"}}, true},
		{"setter", Goal{Setter: "a"}, true},
		{"shared", Goal{Shared: true}, true},
		{"other", Goal{Setter: "b", Holders: []string{"b"}}, false},
	} {
		if got := tc.g.Concerns("a"); got != tc.want {
			t.Errorf("%s: Concerns = %v, want %v", tc.name, got, tc.want)
		}
	}
}
