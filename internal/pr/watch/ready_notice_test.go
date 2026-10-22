package watch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestReadyNoticeStoresStructuredFields(t *testing.T) {
	e := newEscalationEnv(t)
	e.session(e.author.ID, "", true)
	head := strings.Repeat("a", 40)
	mergeable := true
	r := Record{Key: "github.com/owner/repo#17", Number: 17, URL: "https://github.com/owner/repo/pull/17", RoomID: e.rec.ID,
		BaseRef: "main", DefaultBranch: "main", Mergeable: &mergeable,
		Author: e.author.ID, Head: head, RoundHead: head, CI: "passed", Verdict: "approve", Watching: true, State: "open"}
	noticeIfLandable(State{}, &r)
	if len(r.Notices) != 1 {
		t.Fatalf("ready notices: %+v", r.Notices)
	}
	for attempt := 0; attempt < 2; attempt++ {
		id, err := e.s.deliver(context.Background(), r, r.Notices[0])
		if err != nil {
			t.Fatal(err)
		}
		message, err := e.rooms.Message(e.rec.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		var post struct {
			Kind   string `json:"kind"`
			Notice struct {
				Kind     string `json:"kind"`
				Number   int    `json:"number"`
				Head     string `json:"head"`
				ChangeID string `json:"changeId"`
			} `json:"notice"`
		}
		if err := json.Unmarshal(data, &post); err != nil {
			t.Fatal(err)
		}
		if post.Kind != "pr" || post.Notice.Kind != "ready" || post.Notice.Number != 17 || post.Notice.Head != head || post.Notice.ChangeID != r.Key {
			t.Fatalf("structured ready fields missing: %s", data)
		}
		if !strings.Contains(message.Text, "PR #17 is ready to land at "+head+".") || strings.Contains(message.Text, "Land it with:") {
			t.Fatalf("agent text lost the full head or kept the command hint: %q", message.Text)
		}
	}
}

func TestNoticeRetryKeepsEscalatedParent(t *testing.T) {
	e := newEscalationEnv(t)
	mergeable := true
	r := Record{BaseRef: "main", DefaultBranch: "main", Mergeable: &mergeable, Key: "github.com/owner/repo#17", Number: 17, RoomID: e.rec.ID,
		Author: e.author.ID, Head: strings.Repeat("a", 40), CI: "passed", Verdict: "approve", Watching: true, State: "open"}
	r.RoundHead = r.Head
	noticeIfLandable(State{}, &r)
	if len(r.Notices) != 1 {
		t.Fatalf("ready notices: %+v", r.Notices)
	}
	n := r.Notices[0]
	id, err := e.s.deliver(context.Background(), r, n)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := e.mgr.Store.Info(e.author.ParentSession)
	if err != nil {
		t.Fatal(err)
	}
	first, err := e.rooms.Message(e.rec.ID, id)
	if err != nil || len(first.To) != 1 || first.To[0] != parent.Address() || first.SessionIDs[parent.Address()] != parent.ID {
		t.Fatalf("first notice did not route to parent: %+v, %v", first, err)
	}
	for _, parentOpen := range []bool{true, false} {
		e.session(parent.ID, "", parentOpen)
		retried, err := e.s.deliver(context.Background(), r, n)
		if err != nil || retried != id {
			t.Fatalf("retry with parent open=%v: id=%q, err=%v", parentOpen, retried, err)
		}
		messages, err := e.rooms.Messages(e.rec.ID)
		if err != nil || len(messages) != 1 || messages[0].ID != id || messages[0].To[0] != parent.Address() || messages[0].SessionIDs[parent.Address()] != parent.ID {
			t.Fatalf("retry changed stored recipient: %+v, %v", messages, err)
		}
	}
}
