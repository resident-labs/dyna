package rolesession

import (
	"context"
	"errors"
	"testing"
	"time"

	"dyna-agent/internal/subagent"
)

func roleInfo() subagent.Info {
	return subagent.Info{Meta: subagent.Meta{ID: "session", RoomID: "room", Role: "keeper", Profile: "a", Turns: 3, Resumable: true, HarnessSession: "native"}, Status: subagent.StatusIdle}
}

func TestLookupReusesAndAdoptsNamedSession(t *testing.T) {
	valid := roleInfo()
	inspect := func(id string) (subagent.Info, subagent.Turn, error) {
		if id != valid.ID {
			return subagent.Info{}, subagent.Turn{}, errors.New("missing")
		}
		return valid, subagent.Turn{N: 3}, nil
	}
	calls := 0
	named := func() (subagent.Info, error) { calls++; return valid, nil }
	got := Lookup(valid.ID, "room", "keeper", inspect, named)
	if !got.Usable || got.Adopted || calls != 0 || got.Turn.N != 3 {
		t.Fatalf("reuse: %+v", got)
	}
	for _, id := range []string{"", "missing"} {
		got = Lookup(id, "room", "keeper", inspect, named)
		if !got.Usable || !got.Adopted || got.Info.ID != valid.ID {
			t.Fatalf("adopt: %+v", got)
		}
	}
}

func TestLookupRejectsInvalidSessions(t *testing.T) {
	for _, kind := range []string{"closed", "wrong-room", "wrong-role", "missing", "inspect-failed"} {
		t.Run(kind, func(t *testing.T) {
			info := roleInfo()
			switch kind {
			case "closed":
				info.Status = subagent.StatusClosed
			case "wrong-room":
				info.RoomID = "other"
			case "wrong-role":
				info.Role = "reviewer"
			}
			inspect := func(string) (subagent.Info, subagent.Turn, error) {
				if kind == "inspect-failed" {
					return info, subagent.Turn{}, errors.New("missing")
				}
				return info, subagent.Turn{}, nil
			}
			named := func() (subagent.Info, error) {
				if kind == "missing" {
					return info, errors.New("missing")
				}
				return info, nil
			}
			got := Lookup("", "room", "keeper", inspect, named)
			if got.Usable || got.Adopted {
				t.Fatalf("invalid adopted: %+v", got)
			}
			if kind != "missing" && Lookup("saved", "room", "keeper", inspect, nil).Usable {
				t.Fatal("invalid saved session reused")
			}
		})
	}
}

func TestRecreation(t *testing.T) {
	for _, tc := range []struct {
		name, status, message   string
		resumable, native, want bool
	}{
		{"healthy", subagent.TurnOK, "", true, true, false},
		{"local error", subagent.TurnError, "bad input", true, true, true},
		{"quota", subagent.TurnError, "quota", true, true, false},
		{"quota without native session", subagent.TurnError, "quota", false, false, false},
		{"cannot resume", subagent.TurnOK, "", false, true, true},
		{"no native session", subagent.TurnStopped, "", true, false, true},
		{"still running", subagent.TurnRunning, "", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := roleInfo()
			info.Resumable = tc.resumable
			if !tc.native {
				info.HarnessSession = ""
			}
			if got := NeedsRecreation(info, subagent.Turn{Status: tc.status, Error: tc.message}); got != tc.want {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func TestAssignmentDecisions(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name   string
		change func(*subagent.Info, *subagent.Turn, *Policy)
		want   Decision
	}{
		{"ready", func(*subagent.Info, *subagent.Turn, *Policy) {}, Ready},
		{"running", func(i *subagent.Info, _ *subagent.Turn, _ *Policy) { i.Status = subagent.StatusRunning }, WaitBusy},
		{"queued", func(i *subagent.Info, _ *subagent.Turn, _ *Policy) { i.Status = subagent.StatusQueued }, WaitBusy},
		{"transfer", func(i *subagent.Info, _ *subagent.Turn, _ *Policy) {
			i.Transfer = &subagent.TransferState{Phase: "starting"}
		}, WaitBusy},
		{"same turn", func(_ *subagent.Info, _ *subagent.Turn, p *Policy) { p.Assigned = true; p.AssignedTurn = 3 }, WaitTurn},
		{"older observed turn", func(_ *subagent.Info, _ *subagent.Turn, p *Policy) { p.Assigned = true; p.AssignedTurn = 4 }, WaitTurn},
		{"new turn", func(_ *subagent.Info, _ *subagent.Turn, p *Policy) { p.Assigned = true; p.AssignedTurn = 2 }, Ready},
		{"assignment count", func(_ *subagent.Info, _ *subagent.Turn, p *Policy) { p.Assignments = 20 }, Rotate},
		{"input tokens", func(_ *subagent.Info, t *subagent.Turn, _ *Policy) { t.Usage.InputTokens = 150000 }, Rotate},
		{"new profile", func(_ *subagent.Info, _ *subagent.Turn, p *Policy) { p.Profile = "b" }, Rotate},
		{"deadline", func(_ *subagent.Info, _ *subagent.Turn, p *Policy) { p.Deadline = now }, DeadlineExceeded},
		{"busy deadline", func(i *subagent.Info, _ *subagent.Turn, p *Policy) {
			i.Status = subagent.StatusRunning
			p.Deadline = now
		}, DeadlineExceeded},
		{"future deadline", func(_ *subagent.Info, _ *subagent.Turn, p *Policy) { p.Deadline = now.Add(time.Second) }, Ready},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := roleInfo()
			turn := subagent.Turn{}
			policy := Policy{Profile: "a", MaxAssignments: 20, MaxInputTokens: 150000, Now: now}
			tc.change(&info, &turn, &policy)
			if got := DecideAssignment(info, turn, policy); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestActionLock(t *testing.T) {
	root := t.TempDir()
	first, err := Lock(root, "keeper", "room")
	if err != nil {
		t.Fatal(err)
	}
	defer Unlock(first)
	if second, err := Lock(root, "keeper", "room"); err == nil {
		Unlock(second)
		t.Fatal("concurrent assignment lock succeeded")
	}
	for _, pair := range [][2]string{{"reviewer", "room"}, {"keeper", "other"}} {
		other, err := Lock(root, pair[0], pair[1])
		if err != nil {
			t.Fatal(err)
		}
		Unlock(other)
	}
	// Check release without double-unlocking the first file.
	separate, err := Lock(root, "keeper", "released")
	if err != nil {
		t.Fatal(err)
	}
	Unlock(separate)
	next, err := Lock(root, "keeper", "released")
	if err != nil {
		t.Fatal(err)
	}
	Unlock(next)
}

func TestAssignmentContextHardDeadline(t *testing.T) {
	deadline := time.Now().Add(-time.Second)
	ctx, cancel := AssignmentContext(context.Background(), deadline)
	defer cancel()
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("expired context: %v", ctx.Err())
	}
	parent, stop := context.WithCancel(context.Background())
	child, cancelChild := AssignmentContext(parent, time.Time{})
	defer cancelChild()
	stop()
	if !errors.Is(child.Err(), context.Canceled) {
		t.Fatal("parent cancellation lost")
	}
}

// A canceled engine call must not remain blocked behind another process.
func TestLockWaitHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	held, err := Lock(root, "librarian-smell", "repo")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan struct{})
	var lockErr error
	go func() {
		defer close(done)
		unlock, err := LockWait(ctx, root, "librarian-smell", "repo")
		lockErr = err
		if unlock != nil {
			unlock()
		}
	}()
	// Release the held lock on every exit, including the old blocking path.
	defer func() { Unlock(held); <-done }()
	deadline, stop := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer stop()
	select {
	case <-done:
		if !errors.Is(lockErr, context.Canceled) {
			t.Fatalf("waiting lock = %v", lockErr)
		}
	case <-deadline.Done():
		t.Fatal("canceled caller remained blocked behind the held lock")
	}
}
