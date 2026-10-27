package subagent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/questions"
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
