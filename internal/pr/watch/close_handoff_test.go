package watch

import (
	"context"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestCloseKeepsQuotaPausedVerdictForReplay(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	e.track()
	e.tick()
	before := e.record()
	if err := e.s.Store.update(func(st *State) error {
		st.LegacyPause = e.clock.Add(time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Verdict(context.Background(), before.Key, before.Reviewer, before.Head, "approve", "Reviewed this exact head", 0); err == nil {
		t.Fatal("paused verdict was not saved")
	}
	// A second reviewer's saved submission follows the first in the chain.
	if err := e.s.Store.update(func(st *State) error {
		st.Records[before.Key].PendingVerdict.Next = &state.PendingVerdict{Actor: "other-reviewer", Head: before.Head, Verdict: "approve", Summary: "Also fine"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	from := subagent.Info{Meta: subagent.Meta{ID: before.Reviewer, RoomID: before.RoomID}}
	if _, err := e.s.CloseSession(context.Background(), from, subagent.Info{}); err != nil {
		t.Fatal(err)
	}
	closed := e.record()
	if closed.Reviewer != before.Reviewer || closed.PendingVerdict == nil || closed.PendingVerdict.Next == nil {
		t.Fatalf("close dropped the saved verdicts or the reviewer's authority: %+v", closed)
	}
	if err := e.s.Store.update(func(st *State) error {
		st.LegacyPause = time.Time{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	after := e.record()
	if after.Verdict != "approve" || len(after.ReviewRounds) != 1 || after.PendingVerdict != nil {
		t.Fatalf("saved verdict was not replayed: %+v", after)
	}
	if after.Reviewer != "" || after.ReleaseReviewerAfterReplay || after.LastReviewer != before.Reviewer {
		t.Fatalf("reviewer was not released after replay: %+v", after)
	}
}

func TestCloseHandsQueuedUnreadNoticeToHeir(t *testing.T) {
	e := newEnv(t)
	e.s.Notify = notify.New(room.DefaultStore(), subagent.NewManager())
	r := e.track()
	author, heir := e.author, subagent.Info{Meta: subagent.Meta{ID: "ses_20261017-000003-cccccccc", RoomID: e.author.RoomID, Room: e.author.Room, Name: "heir"}}
	author.Name = "author"
	recordtest.Write(t, e.s.sessionStore().Root, author.ID, author.Meta)
	// The room is paused: flush marked the notice Done, but the author never read its post.
	if _, err := e.s.Notify.Rooms.Post(r.RoomID, room.PostInput{ID: "msg_00000000000000a1", From: "owner", To: []string{author.Address()}, Text: "Fix CI"}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key].Notices = append(st.Records[r.Key].Notices, Notice{ID: "msg_00000000000000a1", Target: author.ID, Text: "Fix CI", PostID: "msg_00000000000000a1", Done: true})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.CloseSession(context.Background(), author, heir); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range e.sent {
		if n.Target == heir.ID {
			got = append(got, n.Text)
		}
	}
	if !strings.Contains(strings.Join(got, "\n"), "Fix CI") {
		t.Fatalf("queued notice did not reach the heir: %q", got)
	}
}

func TestCloseSkipsPRTransferredToAnotherAuthor(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	heir := subagent.Info{Meta: subagent.Meta{ID: "ses_20261017-000003-cccccccc", RoomID: r.RoomID, Name: "heir"}}
	explicit := subagent.Info{Meta: subagent.Meta{ID: "ses_20261017-000004-dddddddd", RoomID: r.RoomID, Name: "explicit"}}
	e.s.beforeCloseTransfer = func(key string) {
		if err := e.s.Transfer(context.Background(), key, "author", explicit, ""); err != nil {
			t.Error(err)
		}
	}
	summary, err := e.s.CloseSession(context.Background(), e.author, heir)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.record(); got.Author != explicit.ID || len(got.FormerAuthors) != 1 {
		t.Fatalf("close stole an explicitly transferred PR: %+v", got)
	}
	if len(summary) != 0 {
		t.Fatalf("stale handoff reported as moved: %v", summary)
	}
}
