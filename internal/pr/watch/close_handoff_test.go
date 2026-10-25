package watch

import (
	"context"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionclose"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestCloseKeepsQuotaPausedVerdictForReplay(t *testing.T) {
	e := newEnv(t)
	accountGH(t, e)
	e.setCI("success", 1)
	e.track()
	e.tick()
	before := e.record()
	if err := e.s.Store.update(func(st *State) error {
		st.RateLimits = map[string]RateLimit{(RateLimitScope{Host: "github.com", Account: "fixture", Resource: "core"}).Key(): {PauseUntil: e.clock.Add(time.Hour)}, (RateLimitScope{Host: "github.com", Account: "fixture", Resource: "graphql"}).Key(): {PauseUntil: e.clock.Add(time.Hour)}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Verdict(context.Background(), before.Key, before.Reviewer, before.Head, "approve", "Reviewed this exact head", 0); err == nil {
		t.Fatal("paused verdict was not saved")
	}
	// A required reviewer has a second saved submission in the same chain.
	const other = "ses_20261017-000007-1234abcd"
	recordtest.Write(t, e.s.sessionStore().Root, other, subagent.Meta{ID: other, Name: "required", RoomID: before.RoomID, Room: e.author.Room})
	if err := e.s.RequireReviewers(before.Key, before.Author, before.RoomID, []string{other}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Verdict(t.Context(), before.Key, other, before.Head, "approve", "Also reviewed this head", 0); err == nil {
		t.Fatal("required review was not saved")
	}
	from := subagent.Info{Meta: subagent.Meta{ID: before.Reviewer, RoomID: before.RoomID, Room: e.author.Room}}
	recordtest.Write(t, e.s.sessionStore().Root, from.ID, from.Meta)
	if _, err := e.s.CloseSession(context.Background(), from, subagent.Info{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.CloseSession(t.Context(), subagent.Info{Meta: subagent.Meta{ID: other, RoomID: before.RoomID}}, subagent.Info{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{from.ID, other} {
		meta, err := e.s.sessionStore().ReadMeta(id)
		if err != nil {
			t.Fatal(err)
		}
		meta.Closed = e.clock
		recordtest.Write(t, e.s.sessionStore().Root, id, meta)
	}
	closed := e.record()
	if closed.Reviewer != before.Reviewer || closed.PendingVerdict == nil || closed.PendingVerdict.Next == nil {
		t.Fatalf("close dropped the saved verdicts or the reviewer's authority: %+v", closed)
	}
	if err := e.s.Store.update(func(st *State) error {
		st.RateLimits = map[string]RateLimit{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	after := e.record()
	if after.Verdict != "approve" || len(after.ReviewRounds) != 2 || after.PendingVerdict != nil {
		t.Fatalf("saved verdict was not replayed: %+v", after)
	}
	if after.Reviewer != "" || len(after.ReleaseReviewersAfterReplay) != 0 || after.LastReviewer != before.Reviewer || len(after.RequiredReviewers) != 0 {
		t.Fatalf("reviewer was not released after replay: %+v", after)
	}
}

func TestCloseHandsQueuedUnreadNoticeToHeir(t *testing.T) {
	for _, overlap := range []bool{false, true} {
		name := "already delivered"
		if overlap {
			name = "delivery overlaps close"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.s.Notify = notify.New(room.DefaultStore(), subagent.NewManager())
			r := e.track()
			author, heir := e.author, subagent.Info{Meta: subagent.Meta{ID: "ses_20261017-000003-cccccccc", RoomID: e.author.RoomID, Room: e.author.Room, Name: "heir"}}
			author.Name = "author"
			recordtest.Write(t, e.s.sessionStore().Root, author.ID, author.Meta)
			if _, err := e.s.Notify.Rooms.SetSessionsPaused(t.Context(), r.RoomID, true); err != nil {
				t.Fatal(err)
			}
			if err := e.s.Store.update(func(st *State) error {
				st.Records[r.Key].Notices = append(st.Records[r.Key].Notices, Notice{ID: "msg_00000000000000a1", Target: author.ID, Text: "Fix CI"})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			posted, release := make(chan struct{}), make(chan struct{})
			deliver := e.s.Deliver
			e.s.Deliver = func(ctx context.Context, rec Record, n Notice) (string, error) {
				if n.Target != author.ID {
					return deliver(ctx, rec, n)
				}
				post, err := e.s.Notify.Rooms.Post(r.RoomID, room.PostInput{ID: n.ID, From: "owner", To: []string{author.Address()}, Text: n.Text})
				if err != nil {
					return "", err
				}
				if overlap {
					close(posted)
					<-release
				}
				return post.ID, nil
			}
			if overlap {
				done := make(chan error, 1)
				go func() { done <- e.s.flush(t.Context(), r.Key) }()
				select {
				case <-posted:
				case err := <-done:
					t.Fatalf("delivery ended before posting: %v", err)
				}
				// Commit Done only after close has scanned the pending notice.
				e.s.beforeCloseTransfer = func(string) {
					close(release)
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				}
			} else if err := e.s.flush(t.Context(), r.Key); err != nil {
				t.Fatal(err)
			}
			if _, err := e.s.CloseSession(t.Context(), author, heir); err != nil {
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
		})
	}
}

func TestCloseSkipsPRTransferredToAnotherAuthor(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	heir := subagent.Info{Meta: subagent.Meta{ID: "ses_20261017-000003-cccccccc", RoomID: r.RoomID, Name: "heir"}}
	explicit := subagent.Info{Meta: subagent.Meta{ID: "ses_20261017-000004-dddddddd", RoomID: r.RoomID, Name: "explicit"}}
	collected, release := make(chan struct{}), make(chan struct{})
	e.s.beforeCloseTransfer = func(key string) {
		close(collected)
		<-release
	}
	type result struct {
		summary []sessionclose.Outcome
		err     error
	}
	done := make(chan result, 1)
	go func() {
		summary, err := e.s.CloseSession(context.Background(), e.author, heir)
		done <- result{summary, err}
	}()
	select {
	case <-collected:
	case early := <-done:
		t.Fatalf("close ended before the transfer: %v", early.err)
	}
	err := e.s.Transfer(context.Background(), r.Key, "author", explicit, "")
	close(release)
	closed := <-done
	if err != nil || closed.err != nil {
		t.Fatalf("explicit transfer: %v; close: %v", err, closed.err)
	}
	if got := e.record(); got.Author != explicit.ID || len(got.FormerAuthors) != 1 {
		t.Fatalf("close stole an explicitly transferred PR: %+v", got)
	}
	if len(closed.summary) != 0 {
		t.Fatalf("stale handoff reported as moved: %v", closed.summary)
	}
}
