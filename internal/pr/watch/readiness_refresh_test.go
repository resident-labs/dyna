package watch

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestVerdictRefreshesRemoteReadinessFacts(t *testing.T) {
	for _, tc := range []struct {
		name         string
		draft        bool
		mergeability string
	}{
		{"draft", true, "MERGEABLE"}, {"unknown", false, "UNKNOWN"}, {"conflict", false, "CONFLICTING"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.setCI("success", 1)
			e.track()
			e.tick()
			r := e.record()
			if r.Mergeable == nil || !*r.Mergeable || r.IsDraft {
				t.Fatal("saved readiness is not green")
			}
			e.f.Draft, e.f.Mergeability = tc.draft, tc.mergeability
			e.write()
			if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "approve", "Reviewed this head", 0); err != nil {
				t.Fatal(err)
			}
			if len(e.landNotices()) != 0 {
				t.Fatal("fresh remote hold inherited saved green readiness")
			}
		})
	}
}

func TestMergeabilityTransitionSendsReadyNotice(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	e.f.Mergeability = "UNKNOWN"
	e.track()
	e.tick()
	r := e.record()
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key].Verdict = "approve"
		st.Records[r.Key].Polling.Next = e.clock.Add(time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if r.Mergeable != nil || r.CI != "passed" {
		t.Fatal("bad unknown-mergeability fixture")
	}
	e.f.Mergeability = "MERGEABLE"
	e.tick()
	if len(e.landNotices()) != 1 {
		t.Fatal("same-head mergeability confirmation did not send readiness")
	}
	e.tick()
	if len(e.landNotices()) != 1 {
		t.Fatal("same readiness was delivered twice")
	}
}

func TestReviewerHelpersStayInRequestedRoom(t *testing.T) {
	e := newEnv(t)
	rooms := &room.Store{Root: t.TempDir()}
	first, err := rooms.Create(room.CreateInput{Name: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := rooms.Create(room.CreateInput{Name: "second"})
	if err != nil {
		t.Fatal(err)
	}
	mgr := subagent.NewManager()
	mgr.Store = &subagent.Store{Root: t.TempDir()}
	e.s.Notify = notify.New(rooms, mgr)
	live := map[string]subagent.Info{}
	for i, rec := range []room.Record{first, second} {
		reviewer := []string{"reviewer-first", "reviewer-second"}[i]
		id := []string{"ses_20261018-000001-aaaaaaaa", "ses_20261018-000002-bbbbbbbb"}[i]
		meta := subagent.Meta{ID: id, Name: "helper", RoomID: rec.ID, Room: rec.Name, ParentSession: reviewer}
		recordtest.Write(t, mgr.Store.Root, id, meta)
		live[reviewer] = subagent.Info{Meta: subagent.Meta{ID: reviewer, Role: "reviewer", RoomID: rec.ID, Room: rec.Name, Turns: 1}, Status: subagent.StatusRunning, CurrentTurn: 1}
		if err := e.s.Store.update(func(st *State) error {
			st.Records[reviewer] = &Record{Key: reviewer, RoomID: rec.ID, Head: "head", RoundHead: "head", Reviewer: reviewer, State: "open", Watching: true}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	e.s.ReviewerInfo = func(id string) (subagent.Info, subagent.Turn, error) {
		return live[id], subagent.Turn{N: 1, Status: subagent.TurnRunning}, nil
	}
	status, err := e.s.ReviewerStatus(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Helpers) != 1 || status.Helpers[0].ID != "ses_20261018-000001-aaaaaaaa" {
		t.Fatalf("another room's helper leaked: %+v", status.Helpers)
	}
}

func TestHeadReadinessUsesFreshBaseBeforeFullPoll(t *testing.T) {
	for _, queued := range []bool{false, true} {
		name := "new readiness"
		if queued {
			name = "queued readiness"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.setCI("success", 1)
			if !queued {
				e.f.Mergeability = "UNKNOWN"
			}
			e.track()
			e.tick()
			r := e.record()
			if err := e.s.Store.update(func(st *State) error {
				live := st.Records[r.Key]
				live.Verdict = "approve"
				live.Polling.Next = e.clock.Add(time.Hour)
				if queued {
					noticeIfLandable(*st, live)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			e.f.Mergeability = "MERGEABLE"
			e.f.Pull.Base.Ref = "other-branch"
			e.tick()
			if len(e.landNotices()) != 0 {
				t.Fatal("fresh non-default base inherited saved readiness")
			}
			if e.record().BaseRef != "main" {
				t.Fatal("head confirmation bypassed safe retarget processing")
			}
		})
	}
}
