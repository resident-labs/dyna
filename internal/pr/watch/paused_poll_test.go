package watch

import (
	"context"
	"dyna-agent/internal/room"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPausedRoomMakesNoGitHubRequests(t *testing.T) {
	for _, tc := range []struct {
		repo     string
		watching bool
	}{
		{"owner/repo", true}, {"owner/repo", false}, {"owner/other", true}, {"owner/other", false},
	} {
		watching := tc.watching
		name := "watching"
		if !watching {
			name = "unwatched"
		}
		t.Run(tc.repo+"/"+name, func(t *testing.T) {
			e := newEscalationEnv(t)
			e.session(e.author.ID, e.author.ParentSession, true)
			e.setCI("success", 1)
			r := e.testEnv.track()
			oldKey := r.Key
			r.Repo = tc.repo
			r.Key = refRecord(r).Key()
			r.ChangeID = r.Key
			r.Watching = watching
			if err := e.s.Store.update(func(st *State) error {
				delete(st.Records, oldKey)
				delete(st.Responses, oldKey)
				st.Records[r.Key] = &r
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := e.rooms.SetSessionsPaused(context.Background(), e.rec.ID, true); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			base := e.s.GH.run
			e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error { calls.Add(1); return base(ctx, cmd) }
			e.tick()
			if calls.Load() != 0 {
				t.Fatalf("paused room made %d GitHub requests", calls.Load())
			}
			if watching {
				if err := e.s.ConfirmHeads(context.Background()); err != nil {
					t.Fatal(err)
				}
				if err := e.s.ConfirmHeads(context.Background(), r.Key); err == nil {
					t.Fatal("explicit confirmation accepted a paused room")
				}
				if calls.Load() != 0 {
					t.Fatal("head confirmation polled a paused room")
				}
			}
			if !watching {
				if err := e.s.refreshUnwatched(context.Background(), r.Key, true, nil); err != nil {
					t.Fatal(err)
				}
				if calls.Load() != 0 {
					t.Fatal("forced unwatched refresh polled a paused room")
				}
			}
			if _, err := e.rooms.SetSessionsPaused(context.Background(), e.rec.ID, false); err != nil {
				t.Fatal(err)
			}
			if watching {
				e.tick()
			} else if err := e.s.refreshUnwatched(context.Background(), r.Key, true, nil); err != nil {
				t.Fatal(err)
			}
			if calls.Load() == 0 {
				t.Fatal("resume did not restore GitHub polling")
			}
		})
	}
}

func TestHeadConfirmationSkipsPausedRoomWithoutBlockingOtherRooms(t *testing.T) {
	e := newEscalationEnv(t)
	e.session(e.author.ID, e.author.ParentSession, true)
	paused := e.testEnv.track()
	activeRoom, err := e.rooms.Create(room.CreateInput{Name: "active"})
	if err != nil {
		t.Fatal(err)
	}
	active := paused
	active.Number++
	active.Key = Ref{Host: active.Host, Repo: active.Repo, Number: active.Number}.Key()
	active.ChangeID = active.Key
	active.RoomID = activeRoom.ID
	if err := e.s.Store.update(func(st *State) error { st.Records[active.Key] = &active; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.rooms.SetSessionsPaused(context.Background(), e.rec.ID, true); err != nil {
		t.Fatal(err)
	}
	base := e.s.GH.run
	queries := 0
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if strings.Contains(strings.Join(cmd.Args, " "), "graphql") {
			queries++
			if strings.Contains(strings.Join(cmd.Args, " "), "p17:pullRequest") {
				t.Error("paused PR was included in batched head query")
			}
		}
		return base(ctx, cmd)
	}
	if err := e.s.ConfirmHeads(context.Background()); err != nil {
		t.Fatal(err)
	}
	if queries != 1 {
		t.Fatalf("head queries=%d, want one active-room batch", queries)
	}
	current, err := e.s.Store.Get(active.Key)
	if err != nil || !headVerified(current) {
		t.Fatalf("active room head=%+v, %v", current, err)
	}
}

func TestPollingPassSharesRoomPauseObservation(t *testing.T) {
	e := newEscalationEnv(t)
	if _, err := e.rooms.SetSessionsPaused(context.Background(), e.rec.ID, true); err != nil {
		t.Fatal(err)
	}
	cache := map[string]bool{}
	paused, err := e.s.roomPausedCached(e.rec.ID, cache)
	if err != nil || !paused {
		t.Fatalf("initial pause=%v, %v", paused, err)
	}
	if _, err = e.rooms.SetSessionsPaused(context.Background(), e.rec.ID, false); err != nil {
		t.Fatal(err)
	}
	paused, err = e.s.roomPausedCached(e.rec.ID, cache)
	if err != nil || !paused {
		t.Fatalf("same pass reread the room: %v, %v", paused, err)
	}
	paused, err = e.s.roomPausedCached(e.rec.ID, map[string]bool{})
	if err != nil || paused {
		t.Fatalf("next pass did not observe resume: %v, %v", paused, err)
	}
}

func TestRoomResumeForcesOneFullPollDespiteFutureSchedule(t *testing.T) {
	e := newEscalationEnv(t)
	e.clock = time.Now().Add(-time.Minute)
	e.setCI("success", 1)
	r := e.testEnv.track()
	if _, err := e.s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.rooms.SetSessionsPaused(t.Context(), e.rec.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key].Polling.Next = time.Now().Add(time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var all, rest atomic.Int32
	base := e.s.GH.run
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		all.Add(1)
		if !strings.Contains(strings.Join(cmd.Args, " "), "graphql") {
			rest.Add(1)
		}
		return base(ctx, cmd)
	}
	if _, err := e.s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if all.Load() != 0 {
		t.Fatalf("paused room made %d calls", all.Load())
	}
	resumed, err := e.rooms.SetSessionsPaused(t.Context(), e.rec.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	e.clock = resumed.ResumedAt.Add(time.Second)
	if _, err := e.s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if rest.Load() == 0 {
		t.Fatal("future schedule blocked the resume full poll")
	}
	after := rest.Load()
	if _, err := e.s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if rest.Load() != after {
		t.Fatal("resume triggered a second immediate full poll")
	}
	if !e.record().Watching {
		t.Fatal("room pause lost the watch")
	}
}
