package watch

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

// A PR event keeps the routing and wake of the notice it replaces: it goes to
// the author, else the nearest open ancestor, else the owner, and it wakes the
// session it is addressed to unless the watcher marks it informational or the
// owner receives it without a root decision.
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
		// A merge conflict has no type of its own yet, and it must still reach
		// the author with a wake: the author has a rebase to do.
		{"merge conflict", "", noticeFacts{}, e.author.ID, author, false, true},
		// An informational notice waits for the author's next turn.
		{"update", "informational", noticeFacts{Type: room.PRUpdate}, e.author.ID, author, false, false},
		// With no target the owner receives it, and only root's own post wakes.
		{"owner quiet", "", noticeFacts{Type: room.PRComment}, "", e.rec.Name, false, false},
		{"owner needs root", "", noticeFacts{Type: room.PRComment}, "", e.rec.Name, true, true},
		// A held comment waits in the owner's feed and wakes nobody.
		{"held comment", "held-comment", noticeFacts{Type: room.PRHeldComment, Commenter: "drive-by"}, "", e.rec.Name, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := e.record()
			token := tc.name + ":" + r.Head
			if tc.kind == "informational" {
				token = "informational:" + token
			}
			noticeTyped(&r, tc.target, "notice "+tc.name, token, tc.facts)
			n := r.Notices[len(r.Notices)-1]
			n.NeedsRoot = tc.needs
			n.CommentID = "c1"
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
			if !ok || ev.Head != r.Head || ev.ChangeID != changeID(r) || ev.Label != changeLabel(r) || ev.Location != changeLocation(r) {
				t.Fatalf("payload = %+v, %v", ev, ok)
			}
		})
	}
	// A PR notice never starts an agent for this head.
	if len(e.starts) != 0 {
		t.Fatalf("a notice started an agent: %+v", e.starts)
	}
}

// The agent text says each fact once: the header, the fact line of the type,
// and then only what the fact line does not say. The log tail stays its own
// block.
func TestPREventTextKeepsTheWatcherBody(t *testing.T) {
	e := newEscalationEnv(t)
	e.track()
	e.session(e.author.ID, "", true)
	r := e.record()
	r.Failures = 2
	noticeTyped(&r, r.Author, "CI failed (2/3): unit, build. Fix the failure and push to this PR.", "ci:"+r.Head, noticeFacts{Type: room.PRCIFailed, Summary: "unit, build"})
	r.Notices[len(r.Notices)-1].Text += "\n\nLog tail:\nboom"
	n := r.Notices[len(r.Notices)-1]
	if _, err := e.s.deliver(context.Background(), r, n); err != nil {
		t.Fatal(err)
	}
	msg, err := e.rooms.Message(e.rec.ID, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(msg.Text, "Fix the failure and push to this PR."); got != 1 {
		t.Fatalf("the agent text repeats its fact %d times: %q", got, msg.Text)
	}
	if got := strings.Count(msg.Text, "CI failed (2/3)"); got != 1 {
		t.Fatalf("the CI fact appears %d times: %q", got, msg.Text)
	}
	for _, want := range []string{changeLocation(r) + "\nHead: " + r.Head, "\n\nCI failed (2/3): unit, build.", "\n\nLog tail:\nboom"} {
		if !strings.Contains(msg.Text, want) {
			t.Fatalf("agent text %q does not hold %q", msg.Text, want)
		}
	}
}

// A verdict keeps the reviewer's markdown below its fact line, and a merged
// change keeps the cleanup sentences the watcher adds.
func TestPREventDetailHoldsWhatTheFactLineDoesNot(t *testing.T) {
	e := newEscalationEnv(t)
	e.track()
	e.session(e.author.ID, "", true)
	r := e.record()
	noticeTyped(&r, r.Author, changeLabel(r)+" at "+r.Head+" approved by its Dyna reviewer:\n\nNo open findings.\n\nDyna merges automatically when approved and green.", "verdict:"+r.Head, noticeFacts{Type: room.PRApproved, Verdict: "approve"})
	n := r.Notices[len(r.Notices)-1]
	if _, err := e.s.deliver(context.Background(), r, n); err != nil {
		t.Fatal(err)
	}
	msg, err := e.rooms.Message(e.rec.ID, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	ev, ok := msg.PR()
	if !ok || ev.Detail != "No open findings.\n\nDyna merges automatically when approved and green." {
		t.Fatalf("approved detail = %+v", ev)
	}
	noticeTyped(&r, r.Author, changeLabel(r)+" merged. Mark its goal done if it has no other open PRs. Clean up its worktree and end your turn. "+changeLocation(r), "outcome:"+r.Head, noticeFacts{Type: room.PRMerged})
	merged := r.Notices[len(r.Notices)-1]
	if _, err := e.s.deliver(context.Background(), r, merged); err != nil {
		t.Fatal(err)
	}
	msg, err = e.rooms.Message(e.rec.ID, merged.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(msg.Text, changeLabel(r)+" merged."); got != 1 {
		t.Fatalf("the merged fact appears %d times: %q", got, msg.Text)
	}
	if !strings.Contains(msg.Text, "Mark its goal done") {
		t.Fatalf("merged text lost the cleanup sentences: %q", msg.Text)
	}
}

// One notice posts once, whatever the retry, and a second comment on the same
// head is its own event.
func TestPREventRetryKeepsOnePostPerNotice(t *testing.T) {
	e := newEscalationEnv(t)
	e.track()
	e.session(e.author.ID, "", true)
	r := e.record()
	noticeTyped(&r, r.Author, "GitHub comment from alice (comment c1):\nlooks good", "owner-comments:"+r.Head+":c1", noticeFacts{Type: room.PRComment, Commenter: "alice"})
	first := r.Notices[len(r.Notices)-1]
	first.CommentID = "c1"
	noticeTyped(&r, r.Author, "GitHub comment from bob (comment c2):\nship it", "owner-comments:"+r.Head+":c2", noticeFacts{Type: room.PRComment, Commenter: "bob"})
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
