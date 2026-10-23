package subagent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/shells"
)

// A turn that ends with no work left makes the session dormant, and a message
// wakes it again. Its own next turn ends dormant once more.
func TestATurnThatEndsWithNoWorkMakesTheSessionDormant(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:300 RESPOND: done")
	info = e.waitStatus(info.ID, StatusRunning)
	// The spawn goal is finished while the turn still runs, so the turn ends
	// with nothing left to do.
	e.doneGoals()
	info = e.waitStatus(info.ID, StatusDormant)
	if info.Finished.IsZero() || info.Turns != 1 {
		t.Fatalf("dormant session = %+v; want turn 1 and a dormancy time", info)
	}
	if _, err := e.send(info.ID, "SLEEP:1 RESPOND: woken", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	again := e.waitStatus(info.ID, StatusDormant)
	if again.Turns != 2 || again.Finished.Before(info.Finished) {
		t.Fatalf("session after the waking turn = %+v; want turn 2 and dormant", again)
	}
}

// The shell job a turn runs in ends with that turn, so it is not unfinished
// work. Any other live job the session owns still keeps it awake.
func TestTheTurnsOwnShellJobIsNotUnfinishedWork(t *testing.T) {
	for _, own := range []bool{true, false} {
		t.Run(map[bool]string{true: "own job", false: "another job"}[own], func(t *testing.T) {
			e := newTestEnv(t)
			e.joinRunners()
			info, _ := e.start("SLEEP:300 RESPOND: done")
			info = e.waitStatus(info.ID, StatusRunning)
			job := "sh_20261022-120000-aaaaaaaa"
			store := shells.Store{Root: filepath.Join(filepath.Dir(e.m.Store.root()), "shells")}
			if err := store.Save(shells.Meta{ID: job, Owner: info.ID, Supervisor: os.Getpid(), Cmd: []string{"true"}, Started: time.Now()}, nil); err != nil {
				t.Fatal(err)
			}
			if own {
				t.Setenv(shells.JobEnv, job)
			} else {
				t.Setenv(shells.JobEnv, "sh_20261022-120000-bbbbbbbb")
			}
			e.doneGoals()
			want := StatusDormant
			if !own {
				want = StatusIdle
			}
			if got := e.waitStatus(info.ID, want); got.Status != want {
				t.Fatalf("session status = %s, want %s", got.Status, want)
			}
		})
	}
}
