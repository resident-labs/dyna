package watch

import (
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

// A merge event reaches the session its kind addresses, and only the events
// that need an author or root wake one. Every other event stays in the room
// feed, so a trial's progress cannot start a session.
func TestMergeEventRoutingAndWakePerType(t *testing.T) {
	e, _, _ := mergeEnv(t, 1)
	e.s.Deliver = nil
	key := "github.com/owner/repo#1"
	r, err := e.s.Store.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	e.s.Notify.Sessions.Store = &subagent.Store{Root: t.TempDir()}
	recordtest.Write(t, e.s.Notify.Sessions.Store.Root, e.author.ParentSession, subagent.Meta{
		ID: e.author.ParentSession, Name: "lead", Room: "merge-work", RoomID: r.RoomID,
		Resumable: true, HarnessSession: "lead",
	})
	recordtest.Write(t, e.s.Notify.Sessions.Store.Root, r.Author, subagent.Meta{
		ID: r.Author, Name: "builder", Room: "merge-work", RoomID: r.RoomID,
		ParentSession: e.author.ParentSession, Resumable: true, HarnessSession: "builder",
	})
	author := "merge-work:builder"
	prs := []room.MergePR{{Number: 1, Title: "Fix the parser", URL: r.URL}}
	for _, tc := range []struct {
		name      string
		step      room.MergeStep
		target    string
		needsRoot bool
		to        []string
		wake      bool
		wakesRoot bool
	}{
		{name: "queued", step: room.MergeQueued, to: []string{"merge-work"}},
		{name: "trial", step: room.MergeTrial, to: []string{"merge-work"}},
		{name: "checks", step: room.MergeChecks, to: []string{"merge-work"}},
		{name: "landed", step: room.MergeLanded, to: []string{"merge-work"}},
		{name: "failed to its author", step: room.MergeFailed, target: author, to: []string{author}, wake: true},
		{name: "failed batch to root", step: room.MergeFailed, needsRoot: true, to: []string{"merge-work"}, wake: true, wakesRoot: true},
		{name: "returned", step: room.MergeReturned, target: author, to: []string{author}, wake: true},
		{name: "conflict", step: room.MergeConflict, target: author, to: []string{author}, wake: true},
		{name: "rolled back", step: room.MergeRolledBack, to: []string{"merge-work"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := room.MergeEvent{Type: tc.step, TrialID: "dyna/merge-trial/1", Repo: "owner/repo", Base: "main", PRs: prs}
			if err := e.s.postMergeNotice(t.Context(), r, "routing/"+tc.name, Notice{Target: tc.target, NeedsRoot: tc.needsRoot, Merge: &ev}); err != nil {
				t.Fatal(err)
			}
			msg, err := e.s.Notify.Rooms.Message(r.RoomID, mergeNoticeID(r, "routing/"+tc.name))
			if err != nil {
				t.Fatal(err)
			}
			if msg.Kind != string(room.FamilyMerge) || msg.Notice.Kind != string(tc.step) {
				t.Fatalf("post = %s/%s", msg.Kind, msg.Notice.Kind)
			}
			if len(msg.To) != len(tc.to) || msg.To[0] != tc.to[0] {
				t.Fatalf("recipients = %v, want %v", msg.To, tc.to)
			}
			if msg.Wake != tc.wake || room.WakesRoot(msg, "merge-work") != tc.wakesRoot {
				t.Fatalf("wake = %v, wakes root = %v", msg.Wake, room.WakesRoot(msg, "merge-work"))
			}
			if got, ok := msg.Merge(); !ok || got.Type != tc.step || got.TrialID != "dyna/merge-trial/1" || len(got.PRs) != 1 {
				t.Fatalf("payload = %+v, %v", got, ok)
			}
		})
	}
}
