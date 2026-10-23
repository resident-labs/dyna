package watch

import (
	"context"
	"testing"

	"dyna-agent/internal/room"
)

// A PR event keeps the routing and wake of the notice it replaces: it goes to
// the author, else the nearest open ancestor, else the owner, and only a post
// that wakes the author (or asks root) starts a turn.
func TestPREventRoutingAndWake(t *testing.T) {
	e := newEscalationEnv(t)
	e.track()
	e.session(e.author.ID, "", true)
	author := e.rec.Name + ":" + e.author.ID[len(e.author.ID)-8:]
	for _, tc := range []struct {
		name    string
		kind    string
		facts   noticeFacts
		target  string
		wantTo  string
		needs   bool
		wantWak bool
	}{
		{"ci-failed", "", noticeFacts{Type: room.PRCIFailed, Summary: "unit"}, e.author.ID, author, false, true},
		{"changes", "", noticeFacts{Type: room.PRChanges, Verdict: "changes"}, e.author.ID, author, false, true},
		{"returned", "", noticeFacts{Type: room.PRReturned, Summary: "rebase"}, e.author.ID, author, false, true},
		{"approved", "", noticeFacts{Type: room.PRApproved, Verdict: "approve"}, e.author.ID, author, false, true},
		{"ready", "", noticeFacts{Type: room.PRReady}, e.author.ID, author, false, true},
		{"merged", "", noticeFacts{Type: room.PRMerged}, e.author.ID, author, false, true},
		{"closed", "", noticeFacts{Type: room.PRClosed}, e.author.ID, author, false, true},
		{"comment", "", noticeFacts{Type: room.PRComment, Commenter: "coderabbitai[bot]"}, e.author.ID, author, false, true},
		// An informational notice waits for the author's next turn.
		{"update", "informational", noticeFacts{Type: room.PRUpdate}, e.author.ID, author, false, false},
		// With no target the owner receives it, and only root's own post wakes.
		{"owner quiet", "", noticeFacts{Type: room.PRComment}, "", e.rec.Name, false, false},
		{"owner needs root", "informational", noticeFacts{Type: room.PRUpdate}, "", e.rec.Name, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := e.record()
			noticeTyped(&r, tc.target, "notice "+tc.name, tc.name+":"+r.Head, tc.facts)
			n := r.Notices[len(r.Notices)-1]
			n.NeedsRoot = tc.needs
			if _, err := e.s.deliver(context.Background(), r, n); err != nil {
				t.Fatal(err)
			}
			msg, err := e.rooms.Message(e.rec.ID, n.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(msg.To) != 1 || msg.To[0] != tc.wantTo {
				t.Fatalf("recipients = %v, want %s", msg.To, tc.wantTo)
			}
			if msg.Wake != tc.wantWak || room.WakesRoot(msg, e.rec.Name) != tc.needs {
				t.Fatalf("wake = %v (want %v), wakes root = %v", msg.Wake, tc.wantWak, room.WakesRoot(msg, e.rec.Name))
			}
			ev, ok := msg.PR()
			if !ok || ev.Head != r.Head || ev.ChangeID != changeID(r) || ev.Label != changeLabel(r) {
				t.Fatalf("payload = %+v, %v", ev, ok)
			}
		})
	}
	// Only the post that asks root may start root's turn.
	if len(e.starts) != 0 {
		t.Fatalf("a notice started an agent: %+v", e.starts)
	}
}

// One notice posts once, whatever the retry, and a second comment on the same
// head is its own event.
func TestPREventRetryKeepsOnePostPerNotice(t *testing.T) {
	e := newEscalationEnv(t)
	e.track()
	e.session(e.author.ID, "", true)
	r := e.record()
	noticeTyped(&r, r.Author, "GitHub comment from alice (comment c1):\nlooks good", "owner-comments:"+r.Head+":c1", noticeFacts{Type: room.PRComment, Commenter: "alice", Summary: "new comment from alice"})
	first := r.Notices[len(r.Notices)-1]
	first.CommentID = "c1"
	noticeTyped(&r, r.Author, "GitHub comment from bob (comment c2):\nship it", "owner-comments:"+r.Head+":c2", noticeFacts{Type: room.PRComment, Commenter: "bob", Summary: "new comment from bob"})
	second := r.Notices[len(r.Notices)-1]
	second.CommentID = "c2"
	if first.ID == second.ID {
		t.Fatal("two comments on one head share a notice ID")
	}
	for _, n := range []Notice{first, first, second} {
		if _, err := e.s.deliver(context.Background(), r, n); err != nil {
			t.Fatal(err)
		}
	}
	posts, err := e.rooms.Messages(e.rec.ID)
	if err != nil || len(posts) != 2 {
		t.Fatalf("posts = %d, %v", len(posts), err)
	}
	if got, _ := posts[1].PR(); got.Commenter != "bob" || got.CommentID != "c2" {
		t.Fatalf("second post payload = %+v", got)
	}
}
