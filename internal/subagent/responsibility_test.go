package subagent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/goals"
	state "dyna-agent/internal/pr/state"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/runstore/runstoretest"
	"dyna-agent/internal/shells"
)

// A member list counts the work of many open sessions at once. One bulk read
// must give each session exactly what Responsibilities gives it alone, and
// must not hand one session another's work.
func TestResponsibilityCountsMatchOneSessionReads(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	first, _ := e.start("SLEEP:1 RESPOND: first")
	first, _ = e.wait(first.ID, 1)
	second, _ := e.start("SLEEP:1 RESPOND: second")
	second, _ = e.wait(second.ID, 1)
	e.doneGoals()
	// The first session holds a goal and a live shell job, and has a child
	// that has not finished. The second asked a question.
	if _, err := e.m.goalStore().Add(first.RoomID, "owner", goals.Input{Text: "unfinished", Holders: []string{first.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.questionStore().Ask(questions.Question{Room: second.RoomID, Asker: second.ID, AskerKind: questions.AskerSession, To: "owner", Text: "Which option?", Meanwhile: "continue"}, nil); err != nil {
		t.Fatal(err)
	}
	job := "sh_20261013-184235-2b655443"
	shellStore := shells.Store{Root: filepath.Join(filepath.Dir(e.m.Store.root()), "shells")}
	if err := shellStore.Save(shells.Meta{ID: job, Owner: first.ID, Supervisor: os.Getpid(), Cmd: []string{"true"}, Started: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	stampParent(t, first)
	child, _ := e.start("SLEEP:1 RESPOND: child")
	e.wait(child.ID, 1)

	counts, err := e.m.ResponsibilityCounts(e.ctx, []Info{first, second, child})
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range []Info{first, second, child} {
		items, err := e.m.Responsibilities(e.ctx, info)
		if err != nil {
			t.Fatal(err)
		}
		alone := map[string]int{}
		for _, item := range items {
			alone[string(item.Kind)]++
		}
		if len(alone) != len(counts[info.ID]) {
			t.Fatalf("bulk counts for %s = %v, one-session read = %v", info.Address(), counts[info.ID], alone)
		}
		for kind, n := range alone {
			if counts[info.ID][kind] != n {
				t.Fatalf("bulk counts for %s = %v, one-session read = %v", info.Address(), counts[info.ID], alone)
			}
		}
	}
	for _, kind := range []string{"child", "goal", "shell"} {
		if counts[first.ID][kind] != 1 {
			t.Fatalf("first session counts = %v, want one %s", counts[first.ID], kind)
		}
	}
	if len(counts[second.ID]) != 1 || counts[second.ID]["question"] != 1 {
		t.Fatalf("second session counts = %v, want one question", counts[second.ID])
	}
	// The child delegates from the first session's goal, so it holds no goal of
	// its own and owns exactly one open item: its spawn item. An assigned item
	// stops a session from finishing, so the count must name it.
	if counts[child.ID]["item"] != 1 || len(counts[child.ID]) != 1 {
		t.Fatalf("child session counts = %v, want one assigned item", counts[child.ID])
	}
}

// A count reads the stores once, and reuses that read while no source changes.
// A write to each source, including an item written through its own store
// handle as the CLI does, makes the next count read again.
func TestResponsibilityCountsReuseUntilASourceChanges(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	first, _ := e.start("RESPOND: first")
	first, _ = e.wait(first.ID, 1)
	e.doneGoals()
	goal, err := e.m.goalStore().Add(first.RoomID, "owner", goals.Input{Text: "unfinished", Holders: []string{first.ID}})
	if err != nil {
		t.Fatal(err)
	}
	infos := []Info{first}
	dataDir := filepath.Dir(e.m.Store.root())
	read := func() int {
		t.Helper()
		if _, err := e.m.ResponsibilityCounts(e.ctx, infos); err != nil {
			t.Fatal(err)
		}
		return e.m.countCache().recounts
	}
	if got := read(); got != 1 {
		t.Fatalf("first count read the stores %d times, want 1", got)
	}
	if got := read(); got != 1 {
		t.Fatalf("count with no change read the stores %d times, want 1", got)
	}
	expectReread := func(source string, change func() error) {
		t.Helper()
		before := read()
		if err := change(); err != nil {
			t.Fatal(err)
		}
		if got := read(); got != before+1 {
			t.Fatalf("count after a %s change read the stores %d times, want %d", source, got, before+1)
		}
		if got := read(); got != before+1 {
			t.Fatalf("count after a %s change read again with no change: %d reads, want %d", source, got, before+1)
		}
	}
	expectReread("goal", func() error {
		_, err := e.m.goalStore().Add(first.RoomID, "owner", goals.Input{Text: "another goal"})
		return err
	})
	expectReread("item", func() error {
		_, err := e.m.goalStore().AddItem(first.RoomID, goal.ID, "owner", "step", first.ID)
		return err
	})
	expectReread("question", func() error {
		_, err := e.m.questionStore().Ask(questions.Question{Room: first.RoomID, Asker: first.ID, AskerKind: questions.AskerSession, To: "owner", Text: "Which option?", Meanwhile: "continue"}, nil)
		return err
	})
	expectReread("PR state", func() error {
		return state.Write(state.Root(dataDir), state.State{Version: 1})
	})
	expectReread("run", func() error {
		runstoretest.Write(t, dataDir, runstore.Meta{ID: "wf_first", Status: "running", ParentSession: first.ID, RoomID: first.RoomID})
		return nil
	})
	expectReread("session", func() error {
		third, _ := e.start("RESPOND: third")
		e.wait(third.ID, 1)
		return nil
	})
}
