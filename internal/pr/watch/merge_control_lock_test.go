package watch

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
)

func holdTestPublisher(t *testing.T, s *Service) func() {
	t.Helper()
	unlock, err := s.mergeLock()
	if err != nil {
		t.Fatal(err)
	}
	release := sync.OnceFunc(unlock)
	t.Cleanup(release)
	return release
}

func TestMergeControlWaitsForPublisherAndKeepsAutomaticTickNonblocking(t *testing.T) {
	e, _, _ := mergeEnv(t, 1)
	id := e.author.RoomID
	if err := e.s.MergeControl(t.Context(), id, "pause", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	release := holdTestPublisher(t, e.s)
	if _, err := e.s.MergeTick(t.Context()); err == nil || !strings.Contains(err.Error(), "publisher is active") {
		t.Fatalf("automatic tick waited or entered another publisher: %v", err)
	}
	guard, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	contended := false
	ctx := store.WithFlockContentionHook(guard, func(string) {
		contended = true
		release()
		// The waiting control must get the next pass, even after publication
		// releases the lock and before the control retries it.
		if _, err := e.s.MergeTick(t.Context()); err == nil {
			t.Error("automatic worker overtook a waiting control")
		}
	})
	if err := e.s.MergeControl(ctx, id, "resume", nil, nil, nil); err != nil {
		t.Fatalf("resume did not wait for the short publisher: %v", err)
	}
	if !contended {
		t.Fatal("resume never contended on the existing publisher lock")
	}
	st, err := e.s.Store.Read()
	if err != nil || st.MergePauses[id].Reason != "" {
		t.Fatalf("resume did not commit after acquiring the lock: pauses=%v error=%v", st.MergePauses, err)
	}
	unlock, err := e.s.mergeLock()
	if err != nil {
		t.Fatalf("completed control retained the publisher lock: %v", err)
	}
	unlock()
}

func TestRoomPassDoesNotLandWhileAnotherRoomHasTrial(t *testing.T) {
	e, _, merged := mergeEnv(t, 1, 2)
	other, err := e.s.Notify.Rooms.Create(room.CreateInput{Name: "other-merge-room"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ghclient.SetAccess(e.s.Notify.Rooms, other.ID, ghclient.ReadWrite); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Store.update(func(st *State) error {
		first := st.Records["github.com/owner/repo#1"]
		second := st.Records["github.com/owner/repo#2"]
		second.RoomID = other.ID
		second.Verdict = state.VerdictApproveFast
		st.MergeTrials = map[string]*state.MergeTrial{
			defaultRepo(*first): {Phase: state.MergeTrialPhaseAwaitingRoot, Changes: []state.MergeTrialChange{trialChange(*first)}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(t.Context(), automaticMergeRoom{}, true)
	if _, err := e.s.mergeTickLocked(ctx, other.ID, nil); err != nil {
		t.Fatal(err)
	}
	if len(*merged) != 0 {
		t.Fatalf("landed changes while another room owns the trial: %v", *merged)
	}
}

func TestMergeControlCancellationKeepsTheCurrentPublisher(t *testing.T) {
	e, _, _ := mergeEnv(t, 1)
	id := e.author.RoomID
	if err := e.s.MergeControl(t.Context(), id, "pause", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	release := holdTestPublisher(t, e.s)
	guard, stop := context.WithTimeout(t.Context(), 30*time.Second)
	defer stop()
	cancellable, cancel := context.WithCancel(guard)
	defer cancel()
	ctx := store.WithFlockContentionHook(cancellable, func(string) { cancel() })
	if err := e.s.MergeControl(ctx, id, "resume", nil, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled lock wait returned %v; want context cancellation", err)
	}
	if unlock, err := e.s.mergeLock(); err == nil {
		unlock()
		t.Fatal("cancelled waiter released another publisher's lock")
	}
	st, err := e.s.Store.Read()
	if err != nil || st.MergePauses[id].Reason == "" {
		t.Fatalf("cancelled waiter changed pause state: pauses=%v error=%v", st.MergePauses, err)
	}
	release()
	retry, stopRetry := context.WithTimeout(t.Context(), 30*time.Second)
	defer stopRetry()
	if err := e.s.MergeControl(retry, id, "resume", nil, nil, nil); err != nil {
		t.Fatalf("fresh control could not acquire the released lock: %v", err)
	}
}

func TestMergeControlRechecksRoomAccessAfterWaiting(t *testing.T) {
	e, _, _ := mergeEnv(t, 1)
	id := e.author.RoomID
	if err := e.s.MergeControl(t.Context(), id, "pause", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	release := holdTestPublisher(t, e.s)
	guard, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ctx := store.WithFlockContentionHook(guard, func(string) {
		if err := ghclient.SetAccess(e.s.Notify.Rooms, id, ghclient.ReadOnly); err != nil {
			t.Fatal(err)
		}
		release()
	})
	if err := e.s.MergeControl(ctx, id, "resume", nil, nil, nil); err == nil || !strings.Contains(err.Error(), "room is read-only") {
		t.Fatalf("changed room access was not checked after waiting: %v", err)
	}
	st, err := e.s.Store.Read()
	if err != nil || st.MergePauses[id].Reason == "" {
		t.Fatalf("read-only control changed pause state: pauses=%v error=%v", st.MergePauses, err)
	}
}

func TestMergeControlRefreshesCachedRootAfterWaiting(t *testing.T) {
	testMergeControlSessionChanges(t, authority.Root, subagent.RoleRoot)
}

func testMergeControlSessionChanges(t *testing.T, role authority.Role, storedRole string) {
	t.Helper()
	for _, change := range []string{"role", "room", "closed"} {
		t.Run(change, func(t *testing.T) {
			e, _, _ := mergeEnv(t, 1)
			id := e.author.RoomID
			if err := e.s.MergeControl(t.Context(), id, "pause", nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			before, err := e.s.Store.Read()
			if err != nil {
				t.Fatal(err)
			}
			const session = "ses_20261021-000010-abcdabcd"
			rm, err := e.s.Notify.Rooms.Resolve(id)
			if err != nil {
				t.Fatal(err)
			}
			meta := subagent.Meta{ID: session, Role: storedRole, RoomID: id, Room: rm.Name}
			recordtest.Write(t, e.s.sessionStore().Root, session, meta)
			resolved, err := e.s.sessionStore().SessionActor(session)
			if err != nil || resolved.Role != role || resolved.Room != id {
				t.Fatalf("invalid stored caller fixture: actor=%+v error=%v", resolved, err)
			}
			release := holdTestPublisher(t, e.s)
			guard, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cached := authority.WithActor(guard, authority.Actor{Role: role, Session: session, Room: id})
			changed := false
			ctx := store.WithFlockContentionHook(cached, func(string) {
				changed = true
				switch change {
				case "role":
					meta.Role = ""
				case "room":
					meta.RoomID, meta.Room = "another-room", "another-room"
				case "closed":
					meta.Closed = e.clock
				}
				recordtest.Write(t, e.s.sessionStore().Root, session, meta)
				release()
			})
			if err := e.s.MergeControl(ctx, id, "resume", nil, nil, nil); err == nil || !changed {
				t.Errorf("cached %s accepted after stored %s changed: changed=%v error=%v", role, change, changed, err)
			}
			after, err := e.s.Store.Read()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Errorf("refused control changed PR state: error=%v", err)
			}
			unlock, err := e.s.mergeLock()
			if err != nil {
				t.Fatalf("refused control retained the publisher lock: %v", err)
			}
			unlock()
		})
	}
}
